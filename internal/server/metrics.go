package server

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type serverMetrics struct {
	registry          *prometheus.Registry
	routes            map[string]string
	requests          *prometheus.CounterVec
	requestDuration   *prometheus.HistogramVec
	sessionsEnrolled  prometheus.Counter
	sessionsCompleted prometheus.Counter
	attemptsStarted   prometheus.Counter
	attemptsFinished  *prometheus.CounterVec
	eventsAccepted    prometheus.Counter
}

func newServerMetrics(s *Server) *serverMetrics {
	m := &serverMetrics{
		registry: prometheus.NewRegistry(),
		routes:   make(map[string]string),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "treetest_http_requests_total",
			Help: "Total application HTTP requests by route, method and response code.",
		}, []string{"route", "method", "code"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "treetest_http_request_duration_seconds",
			Help:    "Application HTTP request duration in seconds.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"route", "method"}),
		sessionsEnrolled: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "treetest_sessions_enrolled_total",
			Help: "New participant sessions committed by this process.",
		}),
		sessionsCompleted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "treetest_sessions_completed_total",
			Help: "Participant session completions committed by this process.",
		}),
		attemptsStarted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "treetest_attempts_started_total",
			Help: "Task attempts started and committed by this process.",
		}),
		attemptsFinished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "treetest_attempts_finished_total",
			Help: "Task attempt finishes committed by this process, by submitted outcome.",
		}, []string{"outcome"}),
		eventsAccepted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "treetest_events_accepted_total",
			Help: "New participant events committed by this process, including terminal submit events.",
		}),
	}
	for _, outcome := range []string{"selected", "gave_up", "skipped"} {
		m.attemptsFinished.WithLabelValues(outcome)
	}
	// Each server owns its registry, including when tests or embedders open
	// multiple databases in the same process. Scrapes never query study data.
	m.registry.MustRegister(
		m.requests, m.requestDuration, m.sessionsEnrolled, m.sessionsCompleted,
		m.attemptsStarted, m.attemptsFinished, m.eventsAccepted,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewDBStatsCollector(s.db, "treetest"),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "treetest_http_requests_in_flight",
			Help: "Application HTTP requests currently being handled, excluding metrics scrapes.",
		}, func() float64 { return float64(s.active.Load()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "treetest_database_size_bytes",
			Help: "Combined SQLite database, WAL and shared-memory file size in bytes; zero for an in-memory database.",
		}, func() float64 { return float64(s.databaseBytes()) }),
	)
	return m
}

func (s *Server) handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
	path := pattern
	if _, p, ok := strings.Cut(pattern, " "); ok {
		path = p
	}
	s.metrics.routes[pattern] = path
}

func (m *serverMetrics) instrument(next http.Handler) http.Handler {
	route := promhttp.WithLabelFromRequest("route", func(r *http.Request) string {
		// Resolve after ServeMux has matched the request. Only registered
		// templates are labels, even for redirects and unrecognized methods.
		if path, ok := m.routes[r.Pattern]; ok {
			return path
		}
		return "unmatched"
	})
	return promhttp.InstrumentHandlerDuration(m.requestDuration,
		promhttp.InstrumentHandlerCounter(m.requests, next, route), route)
}

// MetricsHandler serves GET /metrics for a separate operational listener.
// It is independent of the public/admin API and exports only aggregate metrics.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	return mux
}
