package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/config"
	"github.com/input-output-hk/spongix/pkg/store"
	"go.uber.org/zap"
)

var benchDir = flag.String("benchdir", "", "directory for Pebble benchmark data (default: b.TempDir() i.e. tmpfs)")

const benchNamespace = "bench"

func makeNARData(size int) []byte {
	r := rand.New(rand.NewSource(42))
	data := make([]byte, size)
	r.Read(data)
	return data
}

func benchProxy(b *testing.B) *Proxy {
	b.Helper()
	cfg := &config.Config{
		Chunks:     &config.Chunks{},
		Namespaces: map[string]*config.Namespace{benchNamespace: {}},
	}
	if err := cfg.Prepare(); err != nil {
		b.Fatal(err)
	}
	proxy := NewProxy(cfg)
	proxy.s3Store = newFakeStore()
	proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: newFakeIndex()}
	proxy.log = zap.NewNop()
	return proxy
}

func benchProxyDiscard(b *testing.B) *Proxy {
	b.Helper()
	cfg := &config.Config{
		Chunks:     &config.Chunks{},
		Namespaces: map[string]*config.Namespace{benchNamespace: {}},
	}
	if err := cfg.Prepare(); err != nil {
		b.Fatal(err)
	}
	proxy := NewProxy(cfg)
	proxy.s3Store = discardStore{}
	proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: newFakeIndex()}
	proxy.log = zap.NewNop()
	return proxy
}

func pebbleDir(b *testing.B) string {
	b.Helper()
	if *benchDir != "" {
		dir := filepath.Join(*benchDir, b.Name())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { os.RemoveAll(dir) })
		return dir
	}
	return b.TempDir()
}

func benchProxyPebble(b *testing.B) *Proxy {
	b.Helper()
	dir := pebbleDir(b)
	cfg := &config.Config{
		Chunks: &config.Chunks{
			StoreType: "pebble",
			PebbleDir: dir,
		},
		Namespaces: map[string]*config.Namespace{benchNamespace: {StoreType: "pebble"}},
	}
	if err := cfg.Prepare(); err != nil {
		b.Fatal(err)
	}
	db, err := store.Open(dir, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	proxy := NewProxy(cfg)
	proxy.pebbleDB = db
	proxy.s3Store = store.NewChunkStore(db)
	proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: store.NewIndexStore(db, benchNamespace)}
	proxy.log = zap.NewNop()
	return proxy
}

func uniqueData(data []byte, iter int) {
	binary.LittleEndian.PutUint64(data[:8], uint64(iter))
}

func benchLocation(iter int) string {
	return fmt.Sprintf("indices/nar/bench/%016x", iter)
}

// validNarURL returns a URL path matching the mux NAR route pattern.
// The hash is a 52-char Nix base32 string (charset: 0-9 a-d f-n p-s v-z).
func validNarURL() string {
	return "/" + benchNamespace + "/nar/0000000000000000000000000000000000000000000000000000.nar"
}

// BenchmarkChunkerOnly measures isolated chunking cost with no storage.
func BenchmarkChunkerOnly(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20, 100 << 20} {
		name := fmt.Sprintf("%dMB", size>>20)
		b.Run(name, func(b *testing.B) {
			data := makeNARData(size)
			proxy := benchProxyDiscard(b)
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				uniqueData(data, i)
				if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkInsert measures the full insert pipeline with fakeStore.
func BenchmarkInsert(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20, 100 << 20} {
		name := fmt.Sprintf("%dMB", size>>20)
		b.Run(name, func(b *testing.B) {
			data := makeNARData(size)
			proxy := benchProxy(b)
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				uniqueData(data, i)
				if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkInsertThreads measures thread scaling for chunking.
func BenchmarkInsertThreads(b *testing.B) {
	const size = 100 << 20
	data := makeNARData(size)
	for _, threads := range []int{1, 2, 4, 8, 16} {
		b.Run(fmt.Sprintf("threads=%d", threads), func(b *testing.B) {
			cfg := &config.Config{
				Chunks:     &config.Chunks{Threads: threads},
				Namespaces: map[string]*config.Namespace{benchNamespace: {}},
			}
			if err := cfg.Prepare(); err != nil {
				b.Fatal(err)
			}
			proxy := NewProxy(cfg)
			proxy.s3Store = discardStore{}
			proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: newFakeIndex()}
			proxy.log = zap.NewNop()
			b.SetBytes(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				uniqueData(data, i)
				if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkInsertChunkSize measures chunk size impact on throughput.
func BenchmarkInsertChunkSize(b *testing.B) {
	const size = 100 << 20
	data := makeNARData(size)
	for _, avgSize := range []uint64{64 << 10, 256 << 10, 1 << 20, 4 << 20} {
		b.Run(fmt.Sprintf("avg=%dKB", avgSize>>10), func(b *testing.B) {
			cfg := &config.Config{
				Chunks:     &config.Chunks{AvgSize: avgSize},
				Namespaces: map[string]*config.Namespace{benchNamespace: {}},
			}
			if err := cfg.Prepare(); err != nil {
				b.Fatal(err)
			}
			proxy := NewProxy(cfg)
			proxy.s3Store = discardStore{}
			proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: newFakeIndex()}
			proxy.log = zap.NewNop()
			b.SetBytes(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				uniqueData(data, i)
				if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkInsertPebble measures insert throughput with real Pebble storage.
func BenchmarkInsertPebble(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20, 100 << 20} {
		name := fmt.Sprintf("%dMB", size>>20)
		b.Run(name, func(b *testing.B) {
			data := makeNARData(size)
			proxy := benchProxyPebble(b)
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				uniqueData(data, i)
				if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkInsertPebbleThreads measures thread scaling on Pebble backend.
func BenchmarkInsertPebbleThreads(b *testing.B) {
	const size = 100 << 20
	data := makeNARData(size)
	for _, threads := range []int{1, 2, 4, 8, 16} {
		b.Run(fmt.Sprintf("threads=%d", threads), func(b *testing.B) {
			dir := pebbleDir(b)
			cfg := &config.Config{
				Chunks: &config.Chunks{
					Threads:   threads,
					StoreType: "pebble",
					PebbleDir: dir,
				},
				Namespaces: map[string]*config.Namespace{benchNamespace: {StoreType: "pebble"}},
			}
			if err := cfg.Prepare(); err != nil {
				b.Fatal(err)
			}
			db, err := store.Open(dir, nil)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { db.Close() })
			proxy := NewProxy(cfg)
			proxy.pebbleDB = db
			proxy.s3Store = store.NewChunkStore(db)
			proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: store.NewIndexStore(db, benchNamespace)}
			proxy.log = zap.NewNop()
			b.SetBytes(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				uniqueData(data, i)
				if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkHTTPPut measures end-to-end HTTP PUT (no TCP, uses httptest.NewRecorder).
func BenchmarkHTTPPut(b *testing.B) {
	const size = 10 << 20
	data := makeNARData(size)
	proxy := benchProxy(b)
	router := proxy.router()
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		uniqueData(data, i)
		req, err := http.NewRequest(http.MethodPut, validNarURL(), bytes.NewReader(data))
		if err != nil {
			b.Fatal(err)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			b.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
		}
	}
}

// BenchmarkInsertPebbleBatching measures insert throughput with batching on Pebble.
func BenchmarkInsertPebbleBatching(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20, 100 << 20} {
		name := fmt.Sprintf("%dMB", size>>20)
		b.Run(name, func(b *testing.B) {
			data := makeNARData(size)
			dir := pebbleDir(b)
			cfg := &config.Config{
				Chunks: &config.Chunks{
					StoreType: "pebble",
					PebbleDir: dir,
				},
				Namespaces: map[string]*config.Namespace{benchNamespace: {StoreType: "pebble"}},
			}
			if err := cfg.Prepare(); err != nil {
				b.Fatal(err)
			}
			db, err := store.Open(dir, nil)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { db.Close() })
			proxy := NewProxy(cfg)
			proxy.pebbleDB = db
			proxy.s3Store = store.NewChunkStore(db)
			proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: store.NewIndexStore(db, benchNamespace)}
			proxy.log = zap.NewNop()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				uniqueData(data, i)
				if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

var walDir = flag.String("waldir", "", "directory for Pebble WAL (e.g. tmpfs); empty = same as data dir")

// BenchmarkPebbleTuning sweeps MemTableSize, L0CompactionThreshold, and WAL
// placement to find the best configuration for HDD-backed Pebble.
// Run with: go test -bench=BenchmarkPebbleTuning -benchtime=3s -benchmem -run='^$' -benchdir=/big/bench-pebble -waldir=/tmp/bench-wal
func BenchmarkPebbleTuning(b *testing.B) {
	const size = 100 << 20
	data := makeNARData(size)

	memSizes := []uint64{64 << 20, 128 << 20, 256 << 20}
	l0Thresholds := []int{4, 8, 12}
	walOptions := []struct {
		name string
		dir  string
	}{
		{"wal=data", ""},
		{"wal=tmpfs", *walDir},
	}

	for _, mem := range memSizes {
		for _, l0 := range l0Thresholds {
			for _, wal := range walOptions {
				if wal.dir == "" && wal.name == "wal=tmpfs" {
					continue // skip tmpfs WAL if -waldir not set
				}
				name := fmt.Sprintf("mem=%dMB/l0=%d/%s", mem>>20, l0, wal.name)
				b.Run(name, func(b *testing.B) {
					dir := pebbleDir(b)
					walPath := wal.dir
					if walPath != "" {
						walPath = filepath.Join(walPath, b.Name())
						if err := os.MkdirAll(walPath, 0o755); err != nil {
							b.Fatal(err)
						}
						b.Cleanup(func() { os.RemoveAll(walPath) })
					}
					opts := &store.PebbleOptions{
						MemTableSize:       mem,
						L0CompactionThresh: l0,
						WALDir:             walPath,
					}
					cfg := &config.Config{
						Chunks: &config.Chunks{
							StoreType: "pebble",
							PebbleDir: dir,
						},
						Namespaces: map[string]*config.Namespace{benchNamespace: {StoreType: "pebble"}},
					}
					if err := cfg.Prepare(); err != nil {
						b.Fatal(err)
					}
					db, err := store.Open(dir, opts)
					if err != nil {
						b.Fatal(err)
					}
					b.Cleanup(func() { db.Close() })
					proxy := NewProxy(cfg)
					proxy.pebbleDB = db
					proxy.s3Store = store.NewChunkStore(db)
					proxy.s3Indices = map[string]desync.IndexWriteStore{benchNamespace: store.NewIndexStore(db, benchNamespace)}
					proxy.log = zap.NewNop()
					b.SetBytes(size)
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						uniqueData(data, i)
						if err := proxy.insert(context.Background(), benchNamespace, benchLocation(i), bytes.NewReader(data)); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
