package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func assertCounter(t *testing.T, counter prometheus.Counter, want float64) {
	t.Helper()
	if got := testutil.ToFloat64(counter); got != want {
		t.Errorf("%s: got %g, want %g", counter.Desc(), got, want)
	}
}

func TestHTTPMetricsRoutesAndIsolation(t *testing.T) {
	s, err := Open(":memory:", testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(":memory:", testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	type requestLabels struct{ route, method, code string }
	want := map[requestLabels]float64{}
	for _, tc := range []struct {
		method, path, route string
		status              int
	}{
		{"GET", "/", "/{$}", 200},
		{"HEAD", "/assets/app.js", "/assets/", 200},
		{"GET", "/assets/app.js", "/assets/", 200},
		{"GET", "/assets/private-file", "/assets/", 404},
		{"GET", "/assets", "/assets/", 307},
		{"GET", "/api/public/private-run", "/api/public/{slug}", 404},
		{"GET", "/api/public/another-private-run?token=private-query", "/api/public/{slug}", 404},
		{"GET", "/admin/api/runs/private-record/results", "/admin/api/runs/{id}/results", 401},
		{"POST", "/api/public/private-run/join", "/api/public/{slug}/join", 403},
		{"GET", "/s//private-run", "/s/{slug}", 307},
		{"GET", "/private-unknown", "unmatched", 404},
		{"GET", "/metrics", "unmatched", 404},
		{"PRIVATE-METHOD", "/private-unknown", "unmatched", 404},
		{"ANOTHER-PRIVATE-METHOD", "/private-unknown", "unmatched", 404},
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(tc.method, testOrigin+tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
		method := strings.ToLower(tc.method)
		if strings.Contains(tc.method, "PRIVATE") {
			method = "unknown"
		}
		want[requestLabels{tc.route, method, fmt.Sprint(tc.status)}]++
	}
	// Also count internal errors, after routing and error conversion.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", testOrigin+"/api/public/private-run", nil))
	if w.Code != 500 {
		t.Fatalf("closed database: %d", w.Code)
	}
	want[requestLabels{"/api/public/{slug}", "get", "500"}]++
	for labels, value := range want {
		assertCounter(t, s.metrics.requests.WithLabelValues(labels.route, labels.method, labels.code), value)
	}
	if got := testutil.CollectAndCount(s.metrics.requests); got != len(want) {
		t.Errorf("request series: got %d, want %d", got, len(want))
	}
	if got := testutil.CollectAndCount(other.metrics.requests); got != 0 {
		t.Errorf("requests leaked to another server: %d series", got)
	}
	wantDurations := map[[2]string]uint64{}
	for labels, value := range want {
		wantDurations[[2]string{labels.route, labels.method}] += uint64(value)
	}
	families, err := s.metrics.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "treetest_http_request_duration_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			key := [2]string{labels["route"], labels["method"]}
			if got := metric.Histogram.GetSampleCount(); got != wantDurations[key] || got == 0 {
				t.Errorf("duration %v: got %d observations, want %d", key, got, wantDurations[key])
			}
			delete(wantDurations, key)
		}
	}
	if len(wantDurations) != 0 {
		t.Errorf("missing durations: %v", wantDurations)
	}
	for _, format := range []string{"text/plain; version=0.0.4", "application/openmetrics-text; version=1.0.0"} {
		r := httptest.NewRequest("GET", "/metrics", nil)
		r.Header.Set("Accept", format)
		w := httptest.NewRecorder()
		s.MetricsHandler().ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), strings.Split(format, ";")[0]) {
			t.Fatalf("scrape: %d %s", w.Code, w.Header())
		}
		body := w.Body.String()
		for _, private := range []string{"private-", "PRIVATE-", "token="} {
			if strings.Contains(body, private) {
				t.Errorf("scrape contains request data %q", private)
			}
		}
		for _, name := range []string{"go_goroutines", "process_cpu_seconds_total", "go_sql_open_connections", "treetest_database_size_bytes 0", "treetest_http_requests_in_flight 0"} {
			if !strings.Contains(body, name) {
				t.Errorf("scrape missing %s", name)
			}
		}
		if strings.HasPrefix(format, "application/openmetrics-text") && !strings.HasSuffix(body, "# EOF\n") {
			t.Error("OpenMetrics response is incomplete")
		}
	}
	for labels, value := range want {
		assertCounter(t, s.metrics.requests.WithLabelValues(labels.route, labels.method, labels.code), value)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/admin/api/session", 404}, {"GET", "/", 404}, {"POST", "/metrics", 405}} {
		w := httptest.NewRecorder()
		s.MetricsHandler().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("metrics listener %s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}

func TestMetricsScrapeWhileDatabaseBusy(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "study.db"), testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.route("GET /blocked", func(w http.ResponseWriter, r *http.Request) error {
		close(started)
		<-release
		return nil
	})
	go func() {
		defer close(finished)
		s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/blocked", nil))
	}()
	defer func() { close(release); <-finished }()
	<-started
	metricsServer := httptest.NewServer(s.MetricsHandler())
	defer metricsServer.Close()
	// Occupy the only database connection. A scrape must still finish and
	// report pool pressure without waiting for participant transactions.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(metricsServer.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || s.databaseBytes() <= 0 {
		t.Fatalf("scrape status=%d database size=%d", response.StatusCode, s.databaseBytes())
	}
	for _, sample := range []string{
		"treetest_http_requests_in_flight 1\n",
		"go_sql_in_use_connections{db_name=\"treetest\"} 1\n",
		fmt.Sprintf("treetest_database_size_bytes %g\n", float64(s.databaseBytes())),
	} {
		if !strings.Contains(string(body), sample) {
			t.Errorf("scrape missing %s", sample)
		}
	}
}
