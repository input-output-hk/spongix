package main

import "github.com/pascaldekloe/metrics"

var (
	metricRequests        = metrics.Must3LabelCounter("spongix_requests_total", "namespace", "kind", "result")
	metricRequestDuration = metrics.Must2LabelHistogram("spongix_request_duration_seconds", "namespace", "kind",
		0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10)
	metricCacheJobs       = metrics.Must1LabelCounter("spongix_cache_jobs_total", "result")
	metricCacheJobsActive = metrics.MustInteger("spongix_cache_jobs_active", "Background cache jobs in flight")
)

func init() {
	metrics.MustHelp("spongix_requests_total", "Total proxy requests")
	metrics.MustHelp("spongix_request_duration_seconds", "Request latency distribution")
	metrics.MustHelp("spongix_cache_jobs_total", "Background cache jobs")
}
