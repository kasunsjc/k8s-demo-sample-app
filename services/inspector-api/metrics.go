package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
)

type requestKey struct {
	route string
	code  int
}

type Metrics struct {
	mu       sync.Mutex
	requests map[requestKey]uint64
	durSum   map[string]float64
	durCount map[string]uint64
}

func NewMetrics() *Metrics {
	return &Metrics{
		requests: map[requestKey]uint64{},
		durSum:   map[string]float64{},
		durCount: map[string]uint64{},
	}
}

func (m *Metrics) Observe(route string, code int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[requestKey{route, code}]++
	m.durSum[route] += d.Seconds()
	m.durCount[route]++
}

// Write renders metrics in the Prometheus text exposition format.
func (m *Metrics) Write(w io.Writer, gauges map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make([]requestKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].code < keys[j].code
	})
	fmt.Fprintln(w, "# HELP kubeplay_http_requests_total Total HTTP requests by route and status code.")
	fmt.Fprintln(w, "# TYPE kubeplay_http_requests_total counter")
	for _, k := range keys {
		fmt.Fprintf(w, "kubeplay_http_requests_total{route=%s,code=\"%d\"} %d\n", strconv.Quote(k.route), k.code, m.requests[k])
	}

	routes := make([]string, 0, len(m.durCount))
	for r := range m.durCount {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	fmt.Fprintln(w, "# HELP kubeplay_http_request_duration_seconds HTTP request latency.")
	fmt.Fprintln(w, "# TYPE kubeplay_http_request_duration_seconds summary")
	for _, r := range routes {
		fmt.Fprintf(w, "kubeplay_http_request_duration_seconds_sum{route=%s} %g\n", strconv.Quote(r), m.durSum[r])
		fmt.Fprintf(w, "kubeplay_http_request_duration_seconds_count{route=%s} %d\n", strconv.Quote(r), m.durCount[r])
	}

	names := make([]string, 0, len(gauges))
	for n := range gauges {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "# TYPE %s gauge\n%s %g\n", n, n, gauges[n])
	}
}
