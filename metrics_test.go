package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/steinfletcher/apitest"
)

func TestMetricsEndpoint(t *testing.T) {
	proxy := testProxy(t)

	apitest.New().
		Handler(proxy.router()).
		Get("/metrics").
		Expect(t).
		Status(http.StatusOK).
		Assert(func(resp *http.Response, req *http.Request) error {
			body := make([]byte, 64*1024)
			n, _ := resp.Body.Read(body)
			text := string(body[:n])

			for _, name := range []string{
				"spongix_requests_total",
				"spongix_request_duration_seconds",
				"spongix_cache_jobs_total",
				"spongix_cache_jobs_active",
			} {
				if !strings.Contains(text, name) {
					t.Errorf("metrics output missing %q", name)
				}
			}
			return nil
		}).
		End()
}

func TestMetricsRequestHit(t *testing.T) {
	proxy := testProxy(t)

	// PUT a narinfo so we get a cache hit on the subsequent GET
	apitest.New().
		Handler(proxy.router()).
		Put(nsNarinfo).
		Body(fixtureNarinfoNone).
		Expect(t).
		Status(http.StatusCreated).
		End()

	before := metricRequests(testNamespace, narinfoPrefix, "hit").Get()

	apitest.New().
		Handler(proxy.router()).
		Get(nsNarinfo).
		Expect(t).
		Status(http.StatusOK).
		End()

	after := metricRequests(testNamespace, narinfoPrefix, "hit").Get()
	if after != before+1 {
		t.Errorf("expected hit counter to increment by 1, got %d -> %d", before, after)
	}
}

func TestMetricsRequestMiss(t *testing.T) {
	proxy := testProxy(t)

	before := metricRequests(testNamespace, narinfoPrefix, "miss").Get()

	// GET without a prior PUT triggers a miss (no upstream mocks either)
	apitest.New().
		Handler(proxy.router()).
		Get(nsNarinfo).
		Expect(t).
		Status(http.StatusNotFound).
		End()

	after := metricRequests(testNamespace, narinfoPrefix, "miss").Get()
	if after != before+1 {
		t.Errorf("expected miss counter to increment by 1, got %d -> %d", before, after)
	}
}

func TestMetricsRequestError(t *testing.T) {
	proxy := testProxy(t)
	proxy.s3Indices[testNamespace] = &errorFakeIndex{
		fakeIndex: newFakeIndex(),
		getError:  errors.New("test error"),
	}

	before := metricRequests(testNamespace, narinfoPrefix, "error").Get()

	apitest.New().
		Handler(proxy.router()).
		Get(nsNarinfo).
		Expect(t).
		Status(http.StatusInternalServerError).
		End()

	after := metricRequests(testNamespace, narinfoPrefix, "error").Get()
	if after != before+1 {
		t.Errorf("expected error counter to increment by 1, got %d -> %d", before, after)
	}
}

func TestMetricsRequestUploaded(t *testing.T) {
	proxy := testProxy(t)

	before := metricRequests(testNamespace, narinfoPrefix, "uploaded").Get()

	apitest.New().
		Handler(proxy.router()).
		Put(nsNarinfo).
		Body(fixtureNarinfoNone).
		Expect(t).
		Status(http.StatusCreated).
		End()

	after := metricRequests(testNamespace, narinfoPrefix, "uploaded").Get()
	if after != before+1 {
		t.Errorf("expected uploaded counter to increment by 1, got %d -> %d", before, after)
	}
}

func TestMetricsDuration(t *testing.T) {
	proxy := testProxy(t)

	_, countBefore, _ := metricRequestDuration(testNamespace, narinfoPrefix).Get(nil)

	apitest.New().
		Handler(proxy.router()).
		Get(nsNarinfo).
		Expect(t).
		Status(http.StatusNotFound).
		End()

	_, countAfter, _ := metricRequestDuration(testNamespace, narinfoPrefix).Get(nil)
	if countAfter != countBefore+1 {
		t.Errorf("expected duration histogram count to increment by 1, got %d -> %d", countBefore, countAfter)
	}
}

func TestMetricsCacheJobSuccess(t *testing.T) {
	proxy := testProxy(t)
	fakeIdx := newFakeIndex()
	proxy.s3Indices[testNamespace] = fakeIdx

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fixtureNarinfoNone))
	}))
	defer ts.Close()

	before := metricCacheJobs("success").Get()

	proxy.doCache(&cacheRequest{
		namespace: testNamespace,
		url:       ts.URL,
		location:  "indices/narinfo/test/test",
	})

	after := metricCacheJobs("success").Get()
	if after != before+1 {
		t.Errorf("expected cache job success counter to increment by 1, got %d -> %d", before, after)
	}
}

func TestMetricsCacheJobError(t *testing.T) {
	proxy := testProxy(t)

	before := metricCacheJobs("error").Get()

	// Use an invalid URL to force an error in doCache
	proxy.doCache(&cacheRequest{
		namespace: testNamespace,
		url:       "http://127.0.0.1:1/nonexistent",
		location:  "indices/narinfo/test/test",
	})

	after := metricCacheJobs("error").Get()
	if after != before+1 {
		t.Errorf("expected cache job error counter to increment by 1, got %d -> %d", before, after)
	}
}

func TestMetricsCacheJobsActive(t *testing.T) {
	proxy := testProxy(t)
	fakeIdx := newFakeIndex()
	proxy.s3Indices[testNamespace] = fakeIdx

	started := make(chan struct{})
	release := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fixtureNarinfoNone))
	}))
	defer ts.Close()

	before := metricCacheJobsActive.Get()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		proxy.doCache(&cacheRequest{
			namespace: testNamespace,
			url:       ts.URL,
			location:  "indices/narinfo/test/test",
		})
	}()

	<-started
	during := metricCacheJobsActive.Get()
	if during != before+1 {
		t.Errorf("expected active gauge to be %d during cache job, got %d", before+1, during)
	}

	close(release)
	wg.Wait()

	after := metricCacheJobsActive.Get()
	if after != before {
		t.Errorf("expected active gauge to return to %d after cache job, got %d", before, after)
	}
}
