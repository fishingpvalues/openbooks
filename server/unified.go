package server

// PotatoStack v5.2.0: the unified multi-source search.
//
// POST /api/v1/search/unified {query, sources?} searches the sources the
// operator configured and returns one normalized result list. This is
// the "bring your own sources" contract Shelfmark and ReadMeABook expose
// (docs/openbooks/feature-matrix.md, 2026-09-02) in the shape openbooks
// already has the parts for: the IRC session (performSearch) and the
// Prowlarr peer (integrations.Prowlarr).
//
// Sources:
//
//	irc       - the shared api IRC session (the existing POST /search
//	            path). Rate-limited by the 10s window and single-flight;
//	            a 429/409 from performSearch is reported in the source's
//	            status, NOT as a failure of the whole request.
//	prowlarr  - the Prowlarr book search across the configured indexers
//	            (the existing peer client). A disabled client reports
//	            "not configured"; an HTTP error reports the status.
//
// Each source's results are normalized to unifiedResult{source, title,
// author, format, size, url} - the fields a caller needs to act on a
// hit. The Prowlarr leg is a release (magnet/NZB/HTTP link), the IRC
// leg is a DCC book identifier; url carries whichever the source
// provides, empty when it provides none (the IRC result is fetched
// through POST /api/v1/download, not a URL).
//
// The request is NOT atomic across sources: sources are queried
// independently and each reports its own status, so one dead source
// never hides the others' results. This matches how a user reasons
// about "find this book" (every source that can answer should answer),
// and keeps the handler honest about which legs actually ran.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// unifiedResult is one normalized hit from any source.
type unifiedResult struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Format string `json:"format,omitempty"`
	Size   string `json:"size,omitempty"`
	// URL is the fetch link when the source exposes one (the Prowlarr
	// leg: magnet or download URL). Empty for sources whose action is
	// to use BookID instead.
	URL string `json:"url,omitempty"`
	// BookID is the fetch identifier when the source exposes one (the
	// IRC leg: the "!"-prefixed DCC line).
	BookID string `json:"bookId,omitempty"`
	// DedupGroup is the number of results (across every source in the
	// response) that share this result's dedup key (title+author, or
	// ISBN when the source exposes one). 1 = unique; >1 = the same book
	// surfaced from multiple sources. Set by annotateDedupe (v5.3.0,
	// adoption item 8) on the unified search response.
	DedupGroup int `json:"dedupGroup,omitempty"`
}

// unifiedSourceStatus is the per-leg outcome of the request. Status is
// one of: ok, not-configured, rate-limited, busy, bad-gateway, error.
type unifiedSourceStatus struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
	// Hits is how many results this leg contributed.
	Hits int `json:"hits"`
}

// unifiedResponse is the POST /api/v1/search/unified body.
type unifiedResponse struct {
	Query   string                `json:"query"`
	Results []unifiedResult       `json:"results"`
	Sources []unifiedSourceStatus `json:"sources"`
	Took    string                `json:"took"`
}

// unifiedSearchRequest is the POST body.
type unifiedSearchRequest struct {
	Query string `json:"query"`
	// Sources restricts the legs; empty/omitted = both sources.
	// Unknown names are rejected with 400 (a typo must not silently
	// shrink the search).
	Sources []string `json:"sources"`
	// Filters are the v5.3.0 quality controls (formats, language,
	// maxSizeBytes, prefer), applied to the normalized result set after
	// both legs answered (see applyQualityFilters). Optional: empty =
	// the v5.2 behavior.
	Filters QualityFilters `json:"filters,omitempty"`
}

// unifiedKnownSources is the valid source set.
var unifiedKnownSources = map[string]bool{"irc": true, "prowlarr": true}

// unifiedSearchHandler is POST /api/v1/search/unified.
func (server *server) unifiedSearchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req unifiedSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid JSON body"}`))
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"query is required"}`))
			return
		}
		for _, s := range req.Sources {
			if !unifiedKnownSources[s] {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"unknown source: ` + s + `"}`))
				return
			}
		}
		wantIRC, wantProwlarr := true, true
		if len(req.Sources) > 0 {
			wantIRC, wantProwlarr = false, false
			for _, s := range req.Sources {
				switch s {
				case "irc":
					wantIRC = true
				case "prowlarr":
					wantProwlarr = true
				}
			}
		}
		f := normalizeQualityFilters(req.Filters)

		start := time.Now()
		out := unifiedResponse{Query: query, Results: []unifiedResult{}}

		if wantIRC {
			books, statuses := server.unifiedIRCLeg(query)
			out.Results = append(out.Results, books...)
			out.Sources = append(out.Sources, statuses...)
		}
		if wantProwlarr {
			books, statuses := server.unifiedProwlarrLeg(query)
			out.Results = append(out.Results, books...)
			out.Sources = append(out.Sources, statuses...)
		}

		// v5.3.0: the quality filters narrow the normalized set (both
		// legs - the Prowlarr search API takes no size/format params, so
		// filtering happens here for every source, which keeps the
		// contract identical across legs), and the dedupe annotation
		// counts same-book hits across sources (adoption item 8).
		if len(out.Results) > 0 {
			out.Results = applyQualityFilters(out.Results, f)
			annotateDedupe(out.Results)
		}
		out.Took = time.Since(start).Round(time.Millisecond).String()

		recordUnifiedSearch(len(out.Sources))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(out)
	}
}

// unifiedIRCLeg runs the IRC source through performSearch and normalizes
// its results. The status slice has exactly one entry describing the
// leg's outcome; the leg never fails the whole request.
func (server *server) unifiedIRCLeg(query string) ([]unifiedResult, []unifiedSourceStatus) {
	resp, status, _ := server.performSearch(query)

	st := unifiedSourceStatus{Source: "irc"}
	switch status {
	case http.StatusOK:
		st.Status = "ok"
		st.Note = resp.Note
	case http.StatusConflict:
		st.Status = "busy"
		st.Note = resp.Note
	case http.StatusTooManyRequests:
		st.Status = "rate-limited"
		st.Note = resp.Note
	case http.StatusBadGateway:
		st.Status = "bad-gateway"
		st.Note = resp.Note
	default:
		st.Status = "error"
		st.Note = resp.Note
	}

	results := make([]unifiedResult, 0, len(resp.Books))
	for _, b := range resp.Books {
		results = append(results, unifiedResult{
			Source: "irc",
			Title:  b.Title,
			Author: b.Author,
			Format: b.Format,
			Size:   b.Size,
			BookID: b.Full,
		})
	}
	st.Hits = len(results)
	return results, []unifiedSourceStatus{st}
}

// unifiedProwlarrLeg runs the Prowlarr source through the peer client and
// normalizes its release results. A disabled client is a
// "not-configured" status, not an error - the caller asked for every
// source, and an unconfigured one is simply off.
func (server *server) unifiedProwlarrLeg(query string) ([]unifiedResult, []unifiedSourceStatus) {
	st := unifiedSourceStatus{Source: "prowlarr"}
	if server.integrations == nil || server.integrations.Prowlarr == nil ||
		!server.integrations.Prowlarr.Enabled() {
		st.Status = "not-configured"
		st.Note = "Prowlarr base URL not set"
		return nil, []unifiedSourceStatus{st}
	}
	// The 30s budget mirrors the integrations client's own defaultTimeout
	// (30000ms): a slow peer cannot outlive the handler.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	releases, err := server.integrations.Prowlarr.SearchBooks(ctx, query, "book", 0)
	if err != nil {
		st.Status = "error"
		st.Note = err.Error()
		return nil, []unifiedSourceStatus{st}
	}
	results := make([]unifiedResult, 0, len(releases))
	for _, rel := range releases {
		results = append(results, unifiedResult{
			Source: "prowlarr",
			Title:  rel.Title,
			Format: rel.Protocol,
			Size:   humanSizeBytes(rel.Size),
			URL:    firstNonEmpty(rel.MagnetURL, rel.DownloadURL),
		})
	}
	st.Status = "ok"
	st.Hits = len(results)
	return results, []unifiedSourceStatus{st}
}

// ── small helpers ───────────────────────────────────────────────────────

// firstNonEmpty returns the first non-empty (after trim) string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// humanSizeBytes renders a byte count as a short human string
// ("1.4MiB"). The string is cosmetic (it goes into a UI field); the
// binary suffix is the ebook convention.
func humanSizeBytes(b int64) string {
	if b <= 0 {
		return "0B"
	}
	const unit = 1024
	div, exp := int64(1), 0
	for n := b; n >= unit*unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	if div == 1 {
		return fmt.Sprintf("%dB", b)
	}
	v := float64(b) / float64(div)
	return fmt.Sprintf("%.1f%s", v, []string{"", "KiB", "MiB", "GiB", "TiB"}[exp])
}
