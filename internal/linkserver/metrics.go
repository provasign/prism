package linkserver

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics counts requests for /metrics in the Prometheus text format.
type Metrics struct {
	store *Store

	mu         sync.Mutex
	requests   map[[2]string]uint64 // endpoint, code
	buckets    map[string][]uint64  // endpoint -> cumulative counts per bound
	sum        map[string]float64
	count      map[string]uint64
	uploads    map[string]uint64 // ok | rejected | error
	limited    uint64
	authFailed uint64
	started    time.Time
}

var latencyBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func newMetrics(s *Store) *Metrics {
	return &Metrics{store: s, requests: map[[2]string]uint64{}, buckets: map[string][]uint64{},
		sum: map[string]float64{}, count: map[string]uint64{}, uploads: map[string]uint64{}, started: time.Now()}
}

func (m *Metrics) observe(endpoint string, code int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[[2]string{endpoint, fmt.Sprint(code)}]++
	b := m.buckets[endpoint]
	if b == nil {
		b = make([]uint64, len(latencyBounds))
		m.buckets[endpoint] = b
	}
	sec := d.Seconds()
	for i, bound := range latencyBounds {
		if sec <= bound {
			b[i]++
		}
	}
	m.sum[endpoint] += sec
	m.count[endpoint]++
}

func (m *Metrics) upload(code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case code == http.StatusOK:
		m.uploads["ok"]++
	case code >= 500:
		m.uploads["error"]++
	default:
		m.uploads["rejected"]++
	}
}

func (m *Metrics) rateLimited() { m.mu.Lock(); m.limited++; m.mu.Unlock() }
func (m *Metrics) authFailure() { m.mu.Lock(); m.authFailed++; m.mu.Unlock() }

func (m *Metrics) serve(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	m.mu.Lock()
	b.WriteString("# HELP prism_link_requests_total Requests by endpoint and HTTP status.\n# TYPE prism_link_requests_total counter\n")
	keys := make([][2]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1] })
	for _, k := range keys {
		fmt.Fprintf(&b, "prism_link_requests_total{endpoint=%q,code=%q} %d\n", k[0], k[1], m.requests[k])
	}
	b.WriteString("# HELP prism_link_request_seconds Request latency.\n# TYPE prism_link_request_seconds histogram\n")
	eps := make([]string, 0, len(m.buckets))
	for e := range m.buckets {
		eps = append(eps, e)
	}
	sort.Strings(eps)
	for _, e := range eps {
		for i, bound := range latencyBounds {
			fmt.Fprintf(&b, "prism_link_request_seconds_bucket{endpoint=%q,le=\"%g\"} %d\n", e, bound, m.buckets[e][i])
		}
		fmt.Fprintf(&b, "prism_link_request_seconds_bucket{endpoint=%q,le=\"+Inf\"} %d\n", e, m.count[e])
		fmt.Fprintf(&b, "prism_link_request_seconds_sum{endpoint=%q} %g\n", e, m.sum[e])
		fmt.Fprintf(&b, "prism_link_request_seconds_count{endpoint=%q} %d\n", e, m.count[e])
	}
	b.WriteString("# HELP prism_link_uploads_total Bundle uploads by result.\n# TYPE prism_link_uploads_total counter\n")
	for _, r := range []string{"ok", "rejected", "error"} {
		fmt.Fprintf(&b, "prism_link_uploads_total{result=%q} %d\n", r, m.uploads[r])
	}
	fmt.Fprintf(&b, "# HELP prism_link_rate_limited_total Requests refused by the rate limit.\n# TYPE prism_link_rate_limited_total counter\nprism_link_rate_limited_total %d\n", m.limited)
	fmt.Fprintf(&b, "# HELP prism_link_auth_failures_total Requests with a missing, unknown, revoked or expired token.\n# TYPE prism_link_auth_failures_total counter\nprism_link_auth_failures_total %d\n", m.authFailed)
	started := m.started
	m.mu.Unlock()
	st := m.store.Stats()
	fmt.Fprintf(&b, "# HELP prism_link_repositories Repositories in the catalog.\n# TYPE prism_link_repositories gauge\nprism_link_repositories %d\n", st.Repositories)
	fmt.Fprintf(&b, "# HELP prism_link_cached_bundles Bundles held in memory.\n# TYPE prism_link_cached_bundles gauge\nprism_link_cached_bundles %d\n", st.CachedBundles)
	fmt.Fprintf(&b, "# HELP prism_link_cached_bytes Size of the bundles held in memory, as stored.\n# TYPE prism_link_cached_bytes gauge\nprism_link_cached_bytes %d\n", st.CachedBytes)
	fmt.Fprintf(&b, "# HELP prism_link_uptime_seconds Seconds since start.\n# TYPE prism_link_uptime_seconds gauge\nprism_link_uptime_seconds %.0f\n", time.Since(started).Seconds())
	_, _ = w.Write([]byte(b.String()))
}
