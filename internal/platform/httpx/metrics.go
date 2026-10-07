package httpx

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Metrics counts requests and renders them in the Prometheus text format, without
// pulling in a client library. Labels are the method and status class only, so the
// series count stays small however many URLs exist.
type Metrics struct {
	mu       sync.Mutex
	requests map[string]uint64 // "GET 2xx" -> count
	buckets  []float64
	hist     []atomic.Uint64 // cumulative-on-render counts per bucket (+Inf last)
	sumNanos atomic.Int64
	inflight atomic.Int64
	Gauges   func() map[string]float64 // extra gauges, e.g. queue depth
}

func NewMetrics() *Metrics {
	b := []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
	return &Metrics{requests: map[string]uint64{}, buckets: b, hist: make([]atomic.Uint64, len(b)+1)}
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		m.inflight.Add(1)
		defer func() {
			m.inflight.Add(-1)
			d := time.Since(start)
			m.sumNanos.Add(int64(d))
			i := len(m.buckets)
			for j, le := range m.buckets {
				if d.Seconds() <= le {
					i = j
					break
				}
			}
			m.hist[i].Add(1)
			status := ww.Status()
			if status == 0 {
				status = 200
			}
			m.mu.Lock()
			m.requests[fmt.Sprintf("%s %dxx", r.Method, status/100)]++
			m.mu.Unlock()
		}()
		next.ServeHTTP(ww, r)
	})
}

// Handler serves the metrics. With an empty token the endpoint does not exist,
// so nothing is exposed by default.
func (m *Metrics) Handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.NotFound(w, r)
			return
		}
		var b strings.Builder
		b.WriteString("# TYPE http_requests_total counter\n")
		m.mu.Lock()
		keys := make([]string, 0, len(m.requests))
		for k := range m.requests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			method, class, _ := strings.Cut(k, " ")
			fmt.Fprintf(&b, "http_requests_total{method=%q,status=%q} %d\n", method, class, m.requests[k])
		}
		m.mu.Unlock()
		b.WriteString("# TYPE http_request_duration_seconds histogram\n")
		var cum uint64
		for i, le := range m.buckets {
			cum += m.hist[i].Load()
			fmt.Fprintf(&b, "http_request_duration_seconds_bucket{le=\"%g\"} %d\n", le, cum)
		}
		cum += m.hist[len(m.buckets)].Load()
		fmt.Fprintf(&b, "http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cum)
		fmt.Fprintf(&b, "http_request_duration_seconds_sum %g\nhttp_request_duration_seconds_count %d\n", float64(m.sumNanos.Load())/1e9, cum)
		fmt.Fprintf(&b, "# TYPE http_requests_in_flight gauge\nhttp_requests_in_flight %d\n", m.inflight.Load())
		if m.Gauges != nil {
			g := m.Gauges()
			names := make([]string, 0, len(g))
			for n := range g {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Fprintf(&b, "# TYPE %s gauge\n%s %g\n", n, n, g[n])
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(b.String()))
	})
}
