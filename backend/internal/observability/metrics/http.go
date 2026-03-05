package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// HTTPMetrics holds collectors for route-level HTTP request instrumentation.
type HTTPMetrics struct {
	InFlight *prometheus.GaugeVec
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
}

func newHTTPMetrics(reg *prometheus.Registry) HTTPMetrics {
	m := HTTPMetrics{
		InFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_http_in_flight_requests",
			Help: "Number of HTTP requests currently being served.",
		}, []string{"route"}),

		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_http_requests_total",
			Help: "Total HTTP requests by method, route and status code.",
		}, []string{"method", "route", "code"}),

		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omo_http_request_duration_seconds",
			Help:    "Histogram of HTTP request durations in seconds.",
			Buckets: []float64{0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"method", "route"}),
	}
	reg.MustRegister(m.InFlight, m.Requests, m.Duration)
	return m
}

// ---------------------------------------------------------------------------
// InstrumentedMux
// ---------------------------------------------------------------------------

// InstrumentedMux wraps an [http.ServeMux] and records per-route HTTP metrics
// for every handler registered through Handle / HandleFunc.
type InstrumentedMux struct {
	Mux     *http.ServeMux
	Metrics *Metrics
}

// Handle registers a named route with automatic metrics instrumentation.
func (im *InstrumentedMux) Handle(route string, h http.Handler) {
	im.Mux.Handle(route, im.instrument(route, h))
}

// HandleFunc registers a named route handler function with automatic metrics.
func (im *InstrumentedMux) HandleFunc(route string, fn http.HandlerFunc) {
	im.Handle(route, fn)
}

// instrument wraps a handler to record in-flight, request count, and latency.
func (im *InstrumentedMux) instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		im.Metrics.HTTP.InFlight.WithLabelValues(route).Inc()
		defer im.Metrics.HTTP.InFlight.WithLabelValues(route).Dec()

		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		code := strconv.Itoa(rw.status)
		im.Metrics.HTTP.Requests.WithLabelValues(r.Method, route, code).Inc()
		im.Metrics.HTTP.Duration.WithLabelValues(r.Method, route).Observe(duration)
	})
}

// statusWriter captures the HTTP status code written by the handler.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if !sw.wroteHeader {
		sw.status = code
		sw.wroteHeader = true
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if !sw.wroteHeader {
		sw.wroteHeader = true
	}
	return sw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher for SSE compatibility.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter for correct type assertions.
func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

// String renders InstrumentedMux for debugging.
func (im *InstrumentedMux) String() string {
	return fmt.Sprintf("InstrumentedMux{mux: %v}", im.Mux)
}
