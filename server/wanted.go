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
// PotatoStack v5.3.0: the entry gains the quality filters the operator
// asked for (a poll round re-searches WITH them - a match that drifts
// shape is a different book), a per-entry backoff after failed rounds
// (Readarr-style failed-release handling, at the entry level), stale
// detection (an entry with no match after wantedStaleAfter is flagged),
// failed-release tracking (a release that already matched is recorded
// and never re-matched), and an ebook sidecar flag (ReadMeABook,
// adoption item 10: the matching ebook is fetched once the audiobook
// match is handled).
//
// Endpoints (token-gated, under /api/v1):
//
//	POST   /wanted      {query, author?, autoFetch?, filters?, withSidecar?}
//	                 -> 201 the entry
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

// wantedBackoffBase is the extra delay after ONE failed round, on top of
// the poller interval. Each consecutive failure doubles it.
const wantedBackoffBase = 5 * time.Minute

// wantedBackoffMax caps the backoff: a title with no release ever must
// still be re-checked daily, not abandoned.
const wantedBackoffMax = 24 * time.Hour

// wantedStaleAfter is how long an entry may go without a match before it
// is flagged stale (still polled - staleness is an operator signal, not
// a lifecycle state).
const wantedStaleAfter = 7 * 24 * time.Hour

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

	// v5.3.0 (adoption items 2, 4, 10): the operator's request shape
	// and its lifecycle state.
	// Filters are the quality controls persisted with the entry; the
	// poller re-searches with them, so a match that drifts shape (a
	// different format, a language the operator did not ask for) is
	// filtered out like a wrong book.
	Filters QualityFilters `json:"filters,omitempty"`
	// WithSidecar asks for the matching ebook alongside the audiobook
	// (ReadMeABook, adoption item 10). Set on a match round, the ebook
	// leg is searched once and its best result auto-fetched.
	WithSidecar bool `json:"withSidecar,omitempty"`
	// Attempts is the number of poll rounds that sent a search for this
	// entry (the evidence for backoff: FailedRounds is the CONSECUTIVE
	// failures, Attempts is the total).
	Attempts int `json:"attempts"`
	// FailedRounds is the consecutive failed-round count driving the
	// backoff; a match or a rate-limited round (not a failure) resets it.
	FailedRounds int `json:"failedRounds,omitempty"`
	// LastAttemptAt / NextAttemptAt in RFC3339. NextAttemptAt is when
	// the backoff ends; until then the poller skips the entry.
	LastAttemptAt string `json:"lastAttemptAt,omitempty"`
	NextAttemptAt string `json:"nextAttemptAt,omitempty"`
	// SeenReleases are the release identifiers (BookDetail.Full) that
	// already matched this entry. The poller never re-matches them -
	// the operator either took them (autoFetch) or left them, and
	// re-offering the same release every round is noise (adoption item
	// 2: failed-release tracking at the entry level).
	SeenReleases map[string]bool `json:"seenReleases,omitempty"`
	// StaleSince is set the first time the entry crosses
	// wantedStaleAfter without a match; empty while fresh.
	StaleSince string `json:"staleSince,omitempty"`
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
// v5.3.0: the entry persists the operator's filters and sidecar flag so
// every poll round re-searches the SAME shape of book.
func (w *wantedState) addWanted(query, author string, autoFetch bool, filters QualityFilters, withSidecar bool) (*wantedItem, error) {
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
		Query:       q,
		Author:      strings.TrimSpace(author),
		AutoFetch:   autoFetch,
		Filters:     normalizeQualityFilters(filters),
		WithSidecar: withSidecar,
		AddedAt:     time.Now().UTC().Format(time.RFC3339),
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
			Query       string         `json:"query"`
			Author      string         `json:"author"`
			AutoFetch   bool           `json:"autoFetch"`
			Filters     QualityFilters `json:"filters"`
			WithSidecar bool           `json:"withSidecar"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		ws := server.apiWanted
		ws.mu.Lock()
		it, err := ws.addWanted(req.Query, req.Author, req.AutoFetch, req.Filters, req.WithSidecar)
		if err == nil {
			server.saveWantedSnapshot(ws)
		}
		ws.mu.Unlock()

		if err != nil {
			if strings.HasPrefix(err.Error(), "already wanted") {
				writeJSONError(w, http.StatusConflict, err.Error())
				return
			}
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, it)
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
			writeJSONError(w, http.StatusBadRequest, "invalid query encoding")
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
			writeJSONError(w, http.StatusNotFound, "not wanted: "+query)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// wantedPollOnce runs one poll round: find the oldest due wanted entry
// (autoFetch candidates first - they are the entries the operator asked
// to fetch, so they drain before the watchlist), send ONE IRC search
// through performSearch (after checking the v5.3.0 search cache - a
// cached answer costs zero IRC traffic), and record the outcome.
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
	var pickAuto *wantedItem
	ws.mu.Lock()
	items := ws.listWanted()
	ws.mu.Unlock()
	for _, it := range items {
		if it.AutoFetch {
			pickAuto = it
			break
		}
	}
	// An autoFetch entry is picked ONLY when it is due (its backoff
	// window has passed): the entry the operator asked to fetch must not
	// starve the queue forever, but neither may it beat the rate limit.
	if pickAuto != nil && wantedDue(pickAuto) {
		pick = pickAuto
	}
	if pick == nil {
		for _, it := range items {
			if wantedDue(it) {
				pick = it
				break
			}
		}
	}
	if pick == nil {
		return "no due wanted entries"
	}

	// v5.3.0: the search cache in front of the channel. An exact hit
	// inside the TTL (same query, same filters) is served with zero IRC
	// traffic; the filters are part of the cache key, so a filtered
	// entry never shares an unfiltered hit.
	f := normalizeQualityFilters(pick.Filters)
	if resp, ok := server.searchCache.lookup(cacheKey(pick.Query, f)); ok {
		resp.Note = "cached (wanted poll)"
		server.handleWantedRound(ws, pick, resp, http.StatusOK, time.Now().UTC(), true)
		return fmt.Sprintf("wanted: %q served from cache", pick.Query)
	}

	// performSearch blocks up to its own wait (config.SearchTimeout,
	// 120s default) and returns on a dead session (502) rather than
	// hanging, so the poll goroutine is bounded by the same budget as
	// every manual search. The rate limiter inside performSearch is the
	// shared one, so a manual search in the last interval defers this
	// round (the entry stays unmatched and is retried next tick) instead
	// of queueing behind it.
	resp, status, _ := server.performSearch(pick.Query)
	now := time.Now().UTC()

	// Cache the live answer (the shared store path: failures are not
	// stored, so a dead round never poisons the entry for the TTL).
	if status == http.StatusOK {
		server.searchCache.store(cacheKey(pick.Query, f), resp)
	}

	server.handleWantedRound(ws, pick, resp, status, now, false)
	if status == http.StatusBadGateway {
		// Keep the operator-facing string the pre-v5.3 tests and logs
		// expect: the session is down, the round is recorded, and the
		// entry is back off until NextAttemptAt.
		return "irc session down"
	}
	return lastWantedStatus(ws, pick)
}

// handleWantedRecord records one poll round's outcome on the entry:
// filters applied to the match set, failed-release tracking, backoff on
// failure, staleness, auto-fetch, and the ebook sidecar. Call with NO
// ws.mu held (it takes and releases it, as every other store path).
func (server *server) handleWantedRound(ws *wantedState, pick *wantedItem, resp APISearchResponse, status int, now time.Time, fromCache bool) {
	f := normalizeQualityFilters(pick.Filters)
	ws.mu.Lock()
	defer ws.mu.Unlock()

	pick.Attempts++
	pick.LastAttemptAt = now.UTC().Format(time.RFC3339)

	if status != http.StatusOK {
		// A failed round (IRC session down): backoff applies. A
		// rate-limited round is NOT a failure (the channel budget is
		// working as designed) - it only defers this round.
		if status == http.StatusBadGateway {
			pick.applyBackoff(now)
		}
		server.log.Printf("wanted: round for %q failed (status %d)\n", pick.Query, status)
		recordWantedFailed()
		server.saveWantedSnapshot(ws)
		return
	}

	// Apply the entry's filters to the match set (a match that drifts
	// shape is a different book), and the failed-release tracking: a
	// release that already matched is dropped from the set.
	books := resp.Books
	if len(books) > 0 && (len(f.Formats) > 0 || f.Language != "" || f.MaxSizeBytes > 0 || f.Prefer != "") {
		unified := make([]unifiedResult, 0, len(books))
		for _, b := range books {
			unified = append(unified, unifiedResult{
				Source: "irc",
				Title:  b.Title,
				Author: b.Author,
				Format: b.Format,
				Size:   b.Size,
				BookID: b.Full,
			})
		}
		kept := applyQualityFilters(unified, f)
		filtered := make([]core.BookDetail, 0, len(kept))
		for _, u := range kept {
			for _, b := range books {
				if b.Full == u.BookID {
					filtered = append(filtered, b)
					break
				}
			}
		}
		books = filtered
	}
	if pick.SeenReleases != nil && len(books) > 0 {
		fresh := make([]core.BookDetail, 0, len(books))
		for _, b := range books {
			if !pick.SeenReleases[strings.TrimSpace(b.Full)] {
				fresh = append(fresh, b)
			}
		}
		books = fresh
	}

	pick.Matched = books
	if len(books) == 0 {
		// No (new) match: this is a failed round for the backoff and
		// the staleness clock.
		pick.applyBackoff(now)
		server.maybeMarkStale(pick, now)
		server.saveWantedSnapshot(ws)
		return
	}

	// A real match: reset the failure state.
	pick.MatchedAt = now.UTC().Format(time.RFC3339)
	pick.FailedRounds = 0
	pick.NextAttemptAt = ""
	pick.StaleSince = ""
	recordWantedMatched()

	// Record the releases as seen: the next round must not re-offer
	// them (the operator either fetched the first one or left them).
	for _, b := range books {
		if pick.SeenReleases == nil {
			pick.SeenReleases = make(map[string]bool)
		}
		pick.SeenReleases[strings.TrimSpace(b.Full)] = true
	}
	server.saveWantedSnapshot(ws)

	// Auto-fetch the FIRST new result. The identifier is the "!"
	// command line the parser keeps in BookDetail.Full; downloadHandler
	// rejects anything else, so a Full that does not start with "!" is
	// logged and the entry stays matched-but-not-fetched.
	if pick.AutoFetch && len(books) > 0 {
		full := strings.TrimSpace(books[0].Full)
		if !strings.HasPrefix(full, "!") {
			server.log.Printf("wanted: %q matched but has no fetchable identifier (%q); not auto-fetched\n",
				pick.Query, books[0].Full)
			return
		}
		pick.FetchedAt = now.UTC().Format(time.RFC3339)
		server.saveWantedSnapshot(ws)
		if err := server.startAPIClient(); err != nil {
			server.log.Printf("wanted: auto-fetch %q: IRC connect: %s\n", pick.Query, err)
			return
		}
		// state.client is written under the lock by startAPIClient and
		// never nulled afterwards; read it under the lock so the read
		// does not race a concurrent (re)start.
		state := server.api
		state.mu.Lock()
		client := state.client
		state.mu.Unlock()
		core.DownloadBook(client.irc, full)
		recordWantedAutoFetch()
		server.log.Printf("wanted: auto-fetched %q -> %s\n", pick.Query, full)
	}

	// The ebook sidecar (adoption item 10): once the audiobook match is
	// handled, fetch the matching ebook from THIS round's results (the
	// ebook class of the same search - a separate live search in the
	// same round would collide with the shared rate limit and always
	// 429, so the sidecar is best-effort on the current result set; a
	// round whose result set holds no ebook-class result simply does
	// not fetch the sidecar this round). The ebook leg applies the
	// entry's filters with prefer=ebook so an audiobook-shaped result
	// cannot satisfy it.
	if pick.WithSidecar && !pick.AutoFetch && len(books) > 0 {
		febook := f
		febook.Prefer = "ebook"
		kept := applyQualityFilters(unifiedFromBooks(books), febook)
		if len(kept) == 0 {
			server.log.Printf("wanted: sidecar for %q: no ebook-class result in this round\n", pick.Query)
			return
		}
		ebook := strings.TrimSpace(kept[0].BookID)
		if !strings.HasPrefix(ebook, "!") {
			server.log.Printf("wanted: sidecar for %q has no fetchable identifier (%q)\n", pick.Query, ebook)
			return
		}
		pick.FetchedAt = now.UTC().Format(time.RFC3339)
		server.saveWantedSnapshot(ws)
		if err := server.startAPIClient(); err != nil {
			server.log.Printf("wanted: sidecar fetch %q: IRC connect: %s\n", pick.Query, err)
			return
		}
		state := server.api
		state.mu.Lock()
		client := state.client
		state.mu.Unlock()
		core.DownloadBook(client.irc, ebook)
		recordWantedAutoFetch()
		server.log.Printf("wanted: sidecar-fetched %q -> %s\n", pick.Query, ebook)
	}
}

// unifiedFromBooks normalizes IRC BookDetails into the unifiedResult
// shape the quality filters operate on (source "irc").
func unifiedFromBooks(books []core.BookDetail) []unifiedResult {
	out := make([]unifiedResult, 0, len(books))
	for _, b := range books {
		out = append(out, unifiedResult{
			Source: "irc",
			Title:  b.Title,
			Author: b.Author,
			Format: b.Format,
			Size:   b.Size,
			BookID: b.Full,
		})
	}
	return out
}

// wantedDue reports whether the entry is out of its backoff window (or
// has no window at all).
func wantedDue(it *wantedItem) bool {
	if it.NextAttemptAt == "" {
		return true
	}
	next, err := time.Parse(time.RFC3339, it.NextAttemptAt)
	if err != nil {
		// A corrupt timestamp must not wedge the entry: treat it as
		// due and let the next round re-set it.
		return true
	}
	return !time.Now().UTC().Before(next)
}

// applyBackoff extends the entry's backoff window after a failed round.
// Call with ws.mu held (it mutates the entry in place).
func (it *wantedItem) applyBackoff(now time.Time) {
	it.FailedRounds++
	delay := wantedBackoffBase
	for i := 1; i < it.FailedRounds; i++ {
		delay *= 2
		if delay > wantedBackoffMax {
			delay = wantedBackoffMax
			break
		}
	}
	it.NextAttemptAt = now.Add(delay).UTC().Format(time.RFC3339)
}

// maybeMarkStale sets StaleSince the first time the entry crosses
// wantedStaleAfter without a match. Call with ws.mu held.
func (server *server) maybeMarkStale(pick *wantedItem, now time.Time) {
	if pick.StaleSince != "" {
		return
	}
	added, err := time.Parse(time.RFC3339, pick.AddedAt)
	if err != nil {
		return
	}
	if now.Sub(added) >= wantedStaleAfter {
		pick.StaleSince = now.UTC().Format(time.RFC3339)
		server.log.Printf("wanted: %q stale (no match since %s)\n", pick.Query, pick.AddedAt)
	}
}

// lastWantedStatus returns a short status string for the entry after a
// round (for the poller's log line). Call with NO ws.mu held.
func lastWantedStatus(ws *wantedState, pick *wantedItem) string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if pick.MatchedAt != "" {
		if len(pick.Matched) > 0 {
			return fmt.Sprintf("wanted: %q matched %d release(s)", pick.Query, len(pick.Matched))
		}
		return fmt.Sprintf("wanted: %q matched (filtered to zero)", pick.Query)
	}
	return fmt.Sprintf("wanted: no match for %q (attempt %d, backoff until %s)",
		pick.Query, pick.Attempts, pick.NextAttemptAt)
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
