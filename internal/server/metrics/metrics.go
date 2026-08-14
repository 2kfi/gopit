// Package metrics exposes Prometheus instrumentation for the gopit server:
// HTTP latency/status, WebSocket connections, nodes, DB size, plus the
// default Go runtime + process collectors on the default registry.
package metrics

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gopit", Name: "http_requests_total",
		Help: "HTTP requests processed, by method, path and status",
	}, []string{"method", "path", "status"})
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "gopit", Name: "http_request_duration_seconds",
		Help:    "HTTP request latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
	WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gopit", Name: "ws_connections",
		Help: "Browser WebSocket connections (status/terminal/logs streams)",
	})
	NodesOnline = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gopit", Name: "nodes_online",
		Help: "Nodes with a live agent connection",
	})
	NodesTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gopit", Name: "nodes_total",
		Help: "Nodes known to the store",
	})
	DBSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gopit", Name: "db_size_bytes",
		Help: "SQLite database file size (db + WAL + SHM)",
	})
)

func init() {
	prometheus.MustRegister(HTTPRequests, HTTPDuration, WSConnections, NodesOnline, NodesTotal, DBSize)
}

// Handler serves /metrics from the default registry (includes Go runtime).
func Handler() http.Handler { return promhttp.Handler() }

// pathSeg strips variable segments (UUIDs, container IDs) so label
// cardinality stays bounded by the route set.
var pathSeg = regexp.MustCompile(`/[0-9a-fA-F-]{16,}`)

// Middleware records request count, latency and status for every request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		path := pathSeg.ReplaceAllString(r.URL.Path, "/{id}")
		HTTPRequests.WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).Inc()
		HTTPDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack lets WebSocket upgrades pass through the recording wrapper.
func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	return hj.Hijack()
}
