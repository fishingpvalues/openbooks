package server

// PotatoStack v5.1.2: the metrics endpoint.
//
// GET <basepath>api/v1/metrics serves Prometheus-format text - deliberately
// the hand-rolled form, not client_golang: the stack already scrapes
// node/cadvisor/Grafana, the consumer is the standard prometheus text
// parser (no SDK client needed), and a metrics SDK in a 30MB distroless
// image for four gauges would be a dependency decision for little gain.
// The endpoint is token-gated like every /api/v1 route; the text format is
// what prometheus's scrape_configs expect.

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// metrics counters. Status codes are a fixed set so the label space stays
// small (the map is for iteration, not cardinality).
var (
	apiMetricsMu      sync.Mutex
	apiHTTPStatuses   = map[string]uint64{}
	apiSearchCount    uint64
	apiDownloadCount  uint64
	apiDownloadErrors uint64
	apiIRCSessions    uint64
)

// metricsHandler is GET /api/v1/metrics.
func (server *server) metricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := server.api
		state.mu.Lock()
		connected := state.connected
		downloads := len(state.downloads)
		callbacks := len(state.downloadCallbacks)
		state.mu.Unlock()

		apiMetricsMu.Lock()
		statuses := make(map[string]uint64, len(apiHTTPStatuses))
		for k, v := range apiHTTPStatuses {
			statuses[k] = v
		}
		s := atomic.LoadUint64(&apiSearchCount)
		d := atomic.LoadUint64(&apiDownloadCount)
		de := atomic.LoadUint64(&apiDownloadErrors)
		cs := atomic.LoadUint64(&apiIRCSessions)
		apiMetricsMu.Unlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP openbooks_up 1 when the server answers\n")
		fmt.Fprintf(w, "# TYPE openbooks_up gauge\n")
		fmt.Fprintf(w, "openbooks_up 1\n")
		fmt.Fprintf(w, "# HELP openbooks_version The running version\n")
		fmt.Fprintf(w, "# TYPE openbooks_version gauge\n")
		fmt.Fprintf(w, "openbooks_version{version=%q} 1\n", server.config.Version)
		fmt.Fprintf(w, "# HELP openbooks_irc_connected Whether the shared api IRC session is up\n")
		fmt.Fprintf(w, "# TYPE openbooks_irc_connected gauge\n")
		if connected {
			fmt.Fprintf(w, "openbooks_irc_connected 1\n")
		} else {
			fmt.Fprintf(w, "openbooks_irc_connected 0\n")
		}
		fmt.Fprintf(w, "# HELP openbooks_irc_sessions_total IRC sessions established by the api client\n")
		fmt.Fprintf(w, "# TYPE openbooks_irc_sessions_total counter\n")
		fmt.Fprintf(w, "openbooks_irc_sessions_total %d\n", cs)
		fmt.Fprintf(w, "# HELP openbooks_searches_total Searches started via the api (search + torznab)\n")
		fmt.Fprintf(w, "# TYPE openbooks_searches_total counter\n")
		fmt.Fprintf(w, "openbooks_searches_total %d\n", s)
		fmt.Fprintf(w, "# HELP openbooks_downloads_total Book downloads requested via the api\n")
		fmt.Fprintf(w, "# TYPE openbooks_downloads_total counter\n")
		fmt.Fprintf(w, "openbooks_downloads_total %d\n", d)
		fmt.Fprintf(w, "# HELP openbooks_download_errors_total Book download requests rejected by validation\n")
		fmt.Fprintf(w, "# TYPE openbooks_download_errors_total counter\n")
		fmt.Fprintf(w, "openbooks_download_errors_total %d\n", de)
		fmt.Fprintf(w, "# HELP openbooks_downloads_completed Books the api session has received (persist mode)\n")
		fmt.Fprintf(w, "# TYPE openbooks_downloads_completed gauge\n")
		fmt.Fprintf(w, "openbooks_downloads_completed %d\n", downloads)
		fmt.Fprintf(w, "# HELP openbooks_callback_queue_size Queued download-completion webhooks\n")
		fmt.Fprintf(w, "# TYPE openbooks_callback_queue_size gauge\n")
		fmt.Fprintf(w, "openbooks_callback_queue_size %d\n", callbacks)

		keys := make([]string, 0, len(statuses))
		for k := range statuses {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, code := range keys {
			fmt.Fprintf(w, "# HELP openbooks_http_requests_total API requests by response status\n")
			fmt.Fprintf(w, "# TYPE openbooks_http_requests_total counter\n")
			fmt.Fprintf(w, "openbooks_http_requests_total{status=%q} %d\n", code, statuses[code])
		}
	}
}

// recordAPIMetrics is called by the api handlers at their decision points
// (not as middleware - chi middleware would count before the handler's own
// status, and the 404-from-persist-off cases matter most).
func recordAPISearch() {
	atomic.AddUint64(&apiSearchCount, 1)
}

func recordAPIDownload() {
	atomic.AddUint64(&apiDownloadCount, 1)
}

func recordAPIDownloadError() {
	atomic.AddUint64(&apiDownloadErrors, 1)
}

func recordAPIIRCSession() {
	atomic.AddUint64(&apiIRCSessions, 1)
}

// recordAPIStatus counts one /api/v1 response by its final status code.
func recordAPIStatus(code int) {
	apiMetricsMu.Lock()
	apiHTTPStatuses[fmt.Sprint(code)]++
	apiMetricsMu.Unlock()
}
