package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMetricsHandlerEmitsEachFamilyOnce pins the v5.4.2 fix. HELP/TYPE lines
// belong to the metric FAMILY, not to every label set: repeating them makes a
// strict text parser reject the whole exposition with
// `text format parsing error: second HELP line for metric name ...`.
//
// Measured 2026-09-16: node-exporter's textfile collector refused
// openbooks.prom for exactly that reason (openbooks_http_requests_total was
// written with a HELP line per status code) while `GET /api/v1/metrics` itself
// looked perfectly fine - so the service appeared monitored and nothing was
// being collected.
func TestMetricsHandlerEmitsEachFamilyOnce(t *testing.T) {
	s := newTestServer(t, true)

	w := httptest.NewRecorder()
	s.metricsHandler()(w, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", w.Code, http.StatusOK)
	}

	families := map[string]int{}
	samples := 0
	for _, line := range strings.Split(w.Body.String(), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			if fields := strings.Fields(line); len(fields) >= 3 {
				families[fields[2]]++
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			samples++
		}
	}

	if samples == 0 {
		t.Fatal("the exposition carries no samples")
	}
	for name, n := range families {
		if n > 1 {
			t.Errorf("HELP for %s appears %d times; the text format allows exactly one per family", name, n)
		}
	}
}
