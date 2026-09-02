package server

// PotatoStack v5.2.0: the persistent Wanted watchlist.
//
// A "wanted" entry is a book the operator asked for that is not in the
// library yet. Unlike POST /api/v1/search (one-shot, result or nothing),
// a wanted entry is re-searched through the shared api IRC session on a
// timer until a match appears, and is optionally auto-fetched. Research
// note (docs/openbooks/feature-matrix.md, 2026-09-02): every comparable
// downloader-shaped project (Mylar3 watchlist, Shelfmark/ReadMeABook
// request queue) has exactly this lifecycle, and none of them exposes it
// as an API - openbooks is a headless service, so the watchlist is the
// API.
//
// Endpoints (token-gated, under /api/v1):
//
//	POST   /wanted      {query, author?, autoFetch?} -> 201 the entry
//	                 duplicate query -> 409 (case-insensitive)
//	GET    /wanted      -> [entries] oldest added first
//	DELETE /wanted/{query} -> 204
//
// State lives in memory with an optional JSON snapshot under
// <downloadDir>/wanted.json: the snapshot survives restarts, and the
// snapshot dir following the runtime download dir (PUT /settings) is
// deliberate - wanted entries point at books the operator wants in THAT
// library. No snapshot when persist mode is off (a non-persisting
// instance keeps no library to match against).
//
// Safety invariants the poller must respect (all measured, not assumed):
//   - performSearch owns the single-flight rule (pendingSearch) and the
//     rate limit (lastSearch); the poller never sends two searches
//     concurrently and never bypasses the limiter.
//   - a poll round is ONE entry. A 30-entry queue therefore takes 30
//     rounds; with the default interval (5 min) the queue drains over
//     hours. That is the IRC channel's rate budget being respected, not a
//     bug to parallelize away.
//   - autoFetch goes through core.DownloadBook on the same single session
//     and completes through recordAPIDownload like a manual download, so
//     it is visible in GET /api/v1/downloads and fires callback webhooks
//     in order.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/evan-buss/openbooks/core"
)

// wantedPollMin is the floor for the re-search interval: below it the
// poller would beat the 10s search rate limit on every tick.
const wantedPollMin = time.Minute

// wantedItem is one entry of the watchlist.
type wantedItem struct {
	// Query is the operator's search term (what the poller sends to the
	// IRC search bot). Unique case-insensitively.
	Query string `json:"query"`
	// Author is optional enrichment for the query.
	Author string `json:"author,omitempty"`
	// AutoFetch marks the entry as a candidate for an automatic
	// core.DownloadBook as soon as a match appears.
	AutoFetch bool `json:"autoFetch"`
	// AddedAt / FetchedAt / MatchedAt in RFC3339.
	AddedAt   string `json:"addedAt"`
	FetchedAt string `json:"fetchedAt,omitempty"`
	MatchedAt string `json:"matchedAt,omitempty"`
	// Matched is the last set of search results the poller matched this
	// entry against (populated on every successful search round, matched
	// or not - it is the evidence for the match flag).
	Matched []core.BookDetail `json:"matched,omitempty"`
}

// wantedState holds the watchlist. mu guards every field; the snapshot
// write happens under the same lock the REST handlers take, so a file
// write never races a mutation.
type wantedState struct {
	mu    sync.Mutex
	items []*wantedItem
}

func newWantedState() *wantedState {
	return &wantedState{}
}

// wantedSnapshotPath returns the snapshot file for the active download
// dir. Empty when the dir is not set.
func (server *server) wantedSnapshotPath() string {
	dir := server.settings.GetDownloadDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "wanted.json")
}

// saveWantedSnapshot writes the watchlist to disk when persist mode is
// on. Call with w.mu held. The persist check lives here - the single
// source of truth - so a caller cannot forget it: a non-persisting
// instance keeps no library, so it keeps no watchlist file. The snapshot
// is a durability layer over in-memory state; a non-writable download
// dir degrades to in-memory-only (the entries still work for the
// lifetime of the process), and the log says so.
func (server *server) saveWantedSnapshot(w *wantedState) {
	if !server.settings.GetPersist() {
		return
	}
	path := server.wantedSnapshotPath()
	if path == "" {
		return
	}
	body, err := json.MarshalIndent(w.items, "", "  ")
	if err != nil {
		server.log.Printf("wanted: marshal snapshot: %s\n", err)
		return
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		server.log.Printf("wanted: write snapshot %s: %s (in-memory only)\n", path, err)
	}
}

// loadWantedSnapshot restores the watchlist on start. Empty file / parse
// error / missing file all mean "start empty" - a corrupt snapshot must
// never wedge startup, and the in-memory entries created afterwards are
// saved on every change, overwriting the corrupt file.
func (server *server) loadWantedSnapshot() {
	path := server.wantedSnapshotPath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return // no snapshot yet
	}
	var items []*wantedItem
	if err := json.Unmarshal(raw, &items); err != nil {
		server.log.Printf("wanted: corrupt snapshot %s: %s (starting empty)\n", path, err)
		return
	}
	server.apiWanted.mu.Lock()
	server.apiWanted.items = items
	server.apiWanted.mu.Unlock()
	if len(items) > 0 {
		server.log.Printf("wanted: restored %d entries from %s\n", len(items), path)
	}
}

// ── store operations (w.mu held by the caller) ──────────────────────────

// addWanted appends an entry. Returns an error on a duplicate query.
func (w *wantedState) addWanted(query, author string, autoFetch bool) (*wantedItem, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("query is required")
	}
	for _, it := range w.items {
		if strings.EqualFold(it.Query, q) {
			return nil, fmt.Errorf("already wanted: %q", q)
		}
	}
	it := &wantedItem{
		Query:     q,
		Author:    strings.TrimSpace(author),
		AutoFetch: autoFetch,
		AddedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	w.items = append(w.items, it)
	return it, nil
}

// removeWanted drops the entry with the given query (case-insensitive).
// ok is false when no entry matches.
func (w *wantedState) removeWanted(query string) bool {
	for i, it := range w.items {
		if strings.EqualFold(it.Query, query) {
			w.items = append(w.items[:i], w.items[i+1:]...)
			return true
		}
	}
	return false
}

// listWanted returns a copy of the entries in add order.
func (w *wantedState) listWanted() []*wantedItem {
	out := make([]*wantedItem, len(w.items))
	copy(out, w.items)
	return out
}

// ── REST handlers ────────────────────────────────────────────────────────

// wantedAddHandler is POST /api/v1/wanted.
func (server *server) wantedAddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string `json:"query"`
			Author    string `json:"author"`
			AutoFetch bool   `json:"autoFetch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid JSON body"}`))
			return
		}
		ws := server.apiWanted
		ws.mu.Lock()
		it, err := ws.addWanted(req.Query, req.Author, req.AutoFetch)
		if err == nil {
			server.saveWantedSnapshot(ws)
		}
		ws.mu.Unlock()

		if err != nil {
			if strings.HasPrefix(err.Error(), "already wanted") {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(it)
	}
}

// wantedListHandler is GET /api/v1/wanted.
func (server *server) wantedListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws := server.apiWanted
		ws.mu.Lock()
		items := ws.listWanted()
		ws.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
}

// wantedDeleteHandler is DELETE /api/v1/wanted/{query}. The query path
// segment is URL-escaped (queries contain spaces and punctuation); chi
// hands over the escaped form, so unescape before comparing.
func (server *server) wantedDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		segment := chi.URLParam(r, "query")
		query, err := urlUnescapePath(segment)
		if err != nil {
			http.Error(w, "invalid query encoding", http.StatusBadRequest)
			return
		}
		ws := server.apiWanted
		ws.mu.Lock()
		removed := ws.removeWanted(query)
		if removed {
			server.saveWantedSnapshot(ws)
		}
		ws.mu.Unlock()

		if !removed {
			http.Error(w, "not wanted: "+query, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// wantedPollOnce runs one poll round: find the oldest unmatched wanted
// entry (autoFetch candidates first - they are the entries the operator
// asked to fetch, so they drain before the watchlist), send ONE IRC
// search through performSearch, and record the outcome.
//
// Returns the round's status for the log; the poller does not need it.
func (server *server) wantedPollOnce() string {
	state := server.api
	ws := server.apiWanted

	// A search may be in flight for another caller (a manual POST
	// /search or /torznab). The single-flight rule is checked under the
	// state lock; performSearch re-takes the lock briefly to arm
	// pendingSearch.
	state.mu.Lock()
	if state.pendingSearch != nil {
		state.mu.Unlock()
		return "search already in flight (api or poller)"
	}
	state.mu.Unlock()

	var pick *wantedItem
	ws.mu.Lock()
	items := ws.listWanted()
	ws.mu.Unlock()
	for _, it := range items {
		if it.AutoFetch {
			pick = it
			break
		}
	}
	if pick == nil {
		for _, it := range items {
			pick = it
			break
		}
	}
	if pick == nil {
		return "no wanted entries"
	}

	// performSearch blocks up to its own wait (config.SearchTimeout,
	// 120s default) and returns on a dead session (502) rather than
	// hanging, so the poll goroutine is bounded by the same budget as
	// every manual search. The rate limiter inside performSearch is the
	// shared one, so a manual search in the last interval defers this
	// round (the entry stays unmatched and is retried next tick) instead
	// of queueing behind it.
	resp, status, _ := server.performSearch(pick.Query)

	now := time.Now().UTC().Format(time.RFC3339)
	if status == http.StatusBadGateway {
		// The IRC session failed to establish on this round. Record the
		// attempt and keep the entry a candidate for the next round
		// (Matched is untouched).
		// The entry stays a candidate for the next round (Matched untouched);
		// only the log trail records the failed attempt - FetchedAt is
		// reserved for a real auto-fetch.
		server.log.Printf("wanted: search for %q failed (IRC session down)\n", pick.Query)
		return "irc session down"
	}

	ws.mu.Lock()
	matched := server.applyWantedResults(ws, pick, resp.Books, now)
	server.saveWantedSnapshot(ws)
	ws.mu.Unlock()

	if !matched {
		server.log.Printf("wanted: no match for %q\n", pick.Query)
		return fmt.Sprintf("no match for %q", pick.Query)
	}

	// Auto-fetch the FIRST matched result. The identifier is the "!"
	// command line the parser keeps in BookDetail.Full; downloadHandler
	// rejects anything else, so a Full that does not start with "!" is
	// logged and the entry stays matched-but-not-fetched.
	if pick.AutoFetch && len(resp.Books) > 0 {
		full := strings.TrimSpace(resp.Books[0].Full)
		if !strings.HasPrefix(full, "!") {
			server.log.Printf("wanted: %q matched but has no fetchable identifier (%q); not auto-fetched\n",
				pick.Query, resp.Books[0].Full)
			return fmt.Sprintf("matched %q, no fetchable identifier", pick.Query)
		}
		ws.mu.Lock()
		pick.FetchedAt = now
		server.saveWantedSnapshot(ws)
		ws.mu.Unlock()
		if err := server.startAPIClient(); err != nil {
			server.log.Printf("wanted: auto-fetch %q: IRC connect: %s\n", pick.Query, err)
			return fmt.Sprintf("matched %q, auto-fetch failed (IRC connect)", pick.Query)
		}
		// state.client is written under the lock by startAPIClient and
		// never nulled afterwards; read it under the lock so the read
		// does not race a concurrent (re)start.
		state.mu.Lock()
		client := state.client
		state.mu.Unlock()
		core.DownloadBook(client.irc, full)
		recordWantedAutoFetch()
		server.log.Printf("wanted: auto-fetched %q -> %s\n", pick.Query, full)
		return fmt.Sprintf("matched %q, auto-fetched", pick.Query)
	}
	return fmt.Sprintf("matched %q", pick.Query)
}

// applyWantedResults records a search round's outcome on the entry.
// books may be nil (a successful search with zero results). Returns
// whether the entry now has a non-empty match set. Call with ws.mu held.
func (server *server) applyWantedResults(ws *wantedState, pick *wantedItem, books []core.BookDetail, now string) bool {
	pick.Matched = books
	if len(books) == 0 {
		return false
	}
	pick.MatchedAt = now
	recordWantedMatched()
	return true
}

// startWantedPoller runs the re-search loop. One round per tick; an empty
// watchlist returns immediately (the goroutine stays up so entries added
// later are picked up without a restart). interval < wantedPollMin is
// clamped at construction time.
func (server *server) startWantedPoller(ctx context.Context, interval time.Duration) {
	if interval < wantedPollMin {
		interval = wantedPollMin
	}
	server.log.Printf("wanted poller: interval %s (one search per round, %s minimum)\n",
		interval, wantedPollMin)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			status := server.wantedPollOnce()
			server.log.Printf("wanted poller: %s\n", status)
		}
	}
}
