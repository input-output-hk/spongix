package main

import (
	"net/http"
	"time"

	"go.uber.org/zap"
)

// LogRecord warps a http.ResponseWriter and records the status
type LogRecord struct {
	http.ResponseWriter
	status int
}

func (r *LogRecord) Write(p []byte) (int, error) {
	return r.ResponseWriter.Write(p)
}

// WriteHeader overrides ResponseWriter.WriteHeader to keep track of the response code
func (r *LogRecord) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// withHTTPLogging adds HTTP request logging to the Handler h
func withHTTPLogging(log *zap.Logger) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			url := r.URL.String()
			isMetric := url == "/metrics"

			start := time.Now()
			record := &LogRecord{
				ResponseWriter: w,
				status:         200,
			}
			h.ServeHTTP(record, r)

			level := log.Debug
			if record.status >= 500 {
				level = log.Error
			}

			if !(isMetric && record.status == 200) {
				level("RES",
					zap.String("ident", r.Host),
					zap.String("method", r.Method),
					zap.String("url", url),
					zap.Int("status_code", record.status),
					zap.Duration("duration", time.Since(start)),
				)
			}
		})
	}
}
