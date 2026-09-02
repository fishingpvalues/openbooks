package server

// v5.2.0 handler tests: the Wanted watchlist (REST + poll round +
// snapshot), the Atom feed, the OPDS catalog, and the unified
// multi-source search. Network-free by construction, the same way
// api_test.go does it: the IRC-dependent paths run against a dead IRC
// address (127.0.0.1:1) and assert the 502/validation behavior; the
// Prowlarr leg runs against a disabled client (no env set in tests)
// and asserts its "not-configured" status.

import (
	"bytes"
	"fmt"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// v52Router mounts the whole /api/v1 tree (v5.1 + v5.2) under the real
// token middleware. It duplicates apiRouter's registrations plus the
// v5.2.0 routes because chi cannot nest a second group under an already
// mounted /api/v1 path - and duplicating the group here is the only way
// the v5.2 handlers share the middleware shape registerRoutes produces.
func v52Router(s *server) chi.Router {
	r := chi.NewRouter()
	r.Get("/openapi.json", s.openapiHandler())
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.requireToken)
		r.Get("/health", s.healthHandler())
		r.Get("/library", s.libraryListHandler())
		r.Get("/library/*", s.libraryFileHandler())
		r.Delete("/library/{name}", s.libraryDeleteHandler())
		r.Post("/search", s.searchHandler())
		r.Post("/download", s.downloadHandler())
		r.Get("/downloads", s.downloadsHandler())
		r.Get("/metrics", s.metricsHandler())
		r.Get("/settings", s.settingsHandler())
		r.Put("/settings", s.settingsHandler())
		r.Get("/integrations", s.integrationsOverviewHandler())
		// v5.2.0
		r.Post("/wanted", s.wantedAddHandler())
		r.Get("/wanted", s.wantedListHandler())
		r.Delete("/wanted/{query}", s.wantedDeleteHandler())
		r.Get("/feeds/atom", s.atomFeedHandler())
		r.Post("/search/unified", s.unifiedSearchHandler())
	})
	return r
}

// TestWantedHandlers: the REST contract - add 201, duplicate 409, list,
// delete 204, delete-missing 404, empty query 400.
func TestWantedHandlers(t *testing.T) {
	s := newTokenServer(t)
	s.settings.SetPersist(true)
	router := v52Router(s)
	token := s.config.Token

	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/wanted",
			bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(`{"query":""}`); code != http.StatusBadRequest {
		t.Errorf("empty query -> %d, want 400", code)
	}
	if code := post(`{"query":"the great gatsby"}`); code != http.StatusCreated {
		t.Fatalf("add -> %d, want 201", code)
	}
	if code := post(`{"query":"THE Great Gatsby"}`); code != http.StatusConflict {
		t.Errorf("duplicate (case-insensitive) -> %d, want 409", code)
	}
	if code := post(`{"query":"dune"}`); code != http.StatusCreated {
		t.Errorf("second add -> %d, want 201", code)
	}

	// List: both entries, add order.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wanted", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list -> %d, want 200", w.Code)
	}
	var items []wantedItem
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(items) != 2 || items[0].Query != "the great gatsby" ||
		items[1].Query != "dune" || items[0].AddedAt == "" {
		t.Errorf("list = %+v, want 2 entries in add order with AddedAt", items)
	}

	// Delete (URL-escaped query segment).
	req = httptest.NewRequest(http.MethodDelete,
		"/api/v1/wanted/"+url.PathEscape("the great gatsby"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete -> %d, want 204 (body %s)", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete,
		"/api/v1/wanted/"+url.PathEscape("the great gatsby"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("delete-missing -> %d, want 404", w.Code)
	}
}

// TestWantedPollRoundDeadIRC: with the IRC server unreachable a poll
// round reports the session-down status and leaves the entry as a
// candidate (no false match, no crash).
func TestWantedPollRoundDeadIRC(t *testing.T) {
	s := newTokenServer(t)
	s.settings.SetPersist(true)
	ws := s.apiWanted
	ws.mu.Lock()
	it, err := ws.addWanted("a book that will not resolve", "", false)
	ws.mu.Unlock()
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	status := s.wantedPollOnce()
	if !strings.Contains(status, "irc session down") {
		t.Errorf("poll status = %q, want the irc-session-down report", status)
	}
	ws.mu.Lock()
	if it.MatchedAt != "" || len(it.Matched) != 0 {
		t.Errorf("entry after failed round: MatchedAt=%q Matched=%v, want untouched",
			it.MatchedAt, it.Matched)
	}
	ws.mu.Unlock()
}

// TestWantedSnapshot: persist mode snapshots the watchlist to
// <downloadDir>/wanted.json; a fresh server over the same dir restores
// it. Non-persist mode writes no snapshot.
func TestWantedSnapshot(t *testing.T) {
	downloadDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(downloadDir, "books"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := New(Config{DownloadDir: downloadDir, Persist: true, Basepath: "/"})
	ws := s.apiWanted
	ws.mu.Lock()
	if _, err := ws.addWanted("restored book", "", true); err != nil {
		t.Fatalf("add: %v", err)
	}
	s.saveWantedSnapshot(ws)
	ws.mu.Unlock()

	snapPath := filepath.Join(downloadDir, "wanted.json")
	raw, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	if !strings.Contains(string(raw), "restored book") {
		t.Errorf("snapshot = %s, want the entry", raw)
	}

	// A second server over the same dir restores the entry.
	s2 := New(Config{DownloadDir: downloadDir, Persist: true, Basepath: "/"})
	s2.loadWantedSnapshot()
	items := s2.apiWanted.listWanted()
	if len(items) != 1 || items[0].Query != "restored book" || !items[0].AutoFetch {
		t.Errorf("restored = %+v, want 1 entry with AutoFetch", items)
	}

	// Non-persist mode: add + save must not write the file.
	d2 := t.TempDir()
	os.MkdirAll(filepath.Join(d2, "books"), 0o755)
	s3 := New(Config{DownloadDir: d2, Persist: false, Basepath: "/"})
	ws3 := s3.apiWanted
	ws3.mu.Lock()
	ws3.addWanted("ephemeral", "", false)
	s3.saveWantedSnapshot(ws3)
	ws3.mu.Unlock()
	if _, err := os.Stat(filepath.Join(d2, "wanted.json")); !os.IsNotExist(err) {
		t.Errorf("non-persist snapshot written (err=%v), want none", err)
	}
}

// TestAtomFeed: persist mode + seeded library files + a seeded
// completion produce a valid Atom feed with both entry kinds; the
// content type is Atom; the entry links carry the token.
func TestAtomFeed(t *testing.T) {
	s := newTokenServer(t)
	s.settings.SetPersist(true)
	base := s.libraryBase()
	for _, name := range []string{"a-book.epub", "b-book.pdf"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.api.mu.Lock()
	s.api.downloads["a-book.epub"] = time.Now()
	s.api.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feeds/atom", nil)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	w := httptest.NewRecorder()
	v52Router(s).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("feed -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/atom+xml") {
		t.Errorf("content-type = %q, want application/atom+xml", ct)
	}
	var feed atomFeed
	if err := xml.NewDecoder(w.Body).Decode(&feed); err != nil {
		t.Fatalf("feed does not parse as XML: %v (body %s)", err, w.Body.String())
	}
	if len(feed.Entries) < 2 {
		t.Fatalf("feed has %d entries, want at least 2", len(feed.Entries))
	}
	var foundA, foundB bool
	for _, e := range feed.Entries {
		switch e.Title {
		case "a-book.epub", "b-book.pdf":
			foundA = foundA || e.Title == "a-book.epub"
			foundB = foundB || e.Title == "b-book.pdf"
			if len(e.Link) == 0 {
				t.Errorf("entry %q has no links", e.Title)
			}
			for _, l := range e.Link {
				if l.Rel == "alternate" && !strings.Contains(l.Href, "token=") {
					t.Errorf("entry %q link %q carries no token", e.Title, l.Href)
				}
			}
		}
	}
	if !foundA || !foundB {
		t.Errorf("feed missing library entries (a=%v b=%v)", foundA, foundB)
	}
}

// TestOPDSFeed: the catalog lists the library files with acquisition
// links, ?search= filters case-insensitively, and persist-off is a 404.
func TestOPDSFeed(t *testing.T) {
	s := newTokenServer(t)
	s.settings.SetPersist(true)
	base := s.libraryBase()
	for _, name := range []string{"GREAT-GATSBY.epub", "dune.pdf", ".hidden"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	do := func(path string) (*httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		// /opds sits outside /api/v1: mount it the way registerRoutes does.
		r := chi.NewRouter()
		r.With(s.requireToken).Get("/opds", s.opdsFeedHandler())
		r.ServeHTTP(w, req)
		return w
	}

	w := do("/opds")
	if w.Code != http.StatusOK {
		t.Fatalf("opds -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "opds-catalog") {
		t.Errorf("content-type = %q, want the opds-catalog profile", ct)
	}
	var feed opdsFeed
	if err := xml.NewDecoder(w.Body).Decode(&feed); err != nil {
		t.Fatalf("opds does not parse: %v (body %s)", err, w.Body.String())
	}
	if len(feed.Entries) != 2 {
		t.Fatalf("opds has %d entries, want 2 (hidden excluded)", len(feed.Entries))
	}
	var gatsbyLink string
	for _, e := range feed.Entries {
		if e.Title == "GREAT-GATSBY.epub" && len(e.Links) > 0 {
			gatsbyLink = e.Links[0].Href
		}
	}
	if !strings.Contains(gatsbyLink, "token=") ||
		!strings.Contains(gatsbyLink, "api/v1/library/GREAT-GATSBY.epub") {
		t.Errorf("gatsby link = %q, want a token-carrying library URL", gatsbyLink)
	}
	if got := feed.Links[0].Rel; got != "self" {
		t.Errorf("first feed link rel = %q, want self", got)
	}

	// Search: case-insensitive substring on the title.
	w = do("/opds?search=great")
	if w.Code != http.StatusOK {
		t.Fatalf("opds search -> %d", w.Code)
	}
	var searched opdsFeed
	xml.NewDecoder(w.Body).Decode(&searched)
	if len(searched.Entries) != 1 || searched.Entries[0].Title != "GREAT-GATSBY.epub" {
		t.Errorf("search 'great' = %+v, want only GATSBY", searched.Entries)
	}

	// Persist off: 404.
	s2 := newTokenServer(t)
	s2.settings.SetPersist(false)
	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	req.Header.Set("Authorization", "Bearer "+s2.config.Token)
	ww := httptest.NewRecorder()
	rr := chi.NewRouter()
	rr.With(s2.requireToken).Get("/opds", s2.opdsFeedHandler())
	rr.ServeHTTP(ww, req)
	if ww.Code != http.StatusNotFound {
		t.Errorf("opds persist-off -> %d, want 404", ww.Code)
	}
}

// TestUnifiedSearch: with a dead IRC server and no Prowlarr configured,
// the request is still a 200 whose per-source statuses are honest
// (bad-gateway / not-configured); a bad source name is a 400.
func TestUnifiedSearch(t *testing.T) {
	s := newTokenServer(t)
	router := v52Router(s)

	post := func(body string) (*httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search/unified",
			bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	if w := post(`{"query":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty query -> %d, want 400", w.Code)
	}
	if w := post(`{"query":"x","sources":["nope"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown source -> %d, want 400", w.Code)
	}

	w := post(`{"query":"any book"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("unified -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var out unifiedResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	statusBySource := map[string]string{}
	for _, st := range out.Sources {
		statusBySource[st.Source] = st.Status
	}
	if statusBySource["irc"] != "bad-gateway" {
		t.Errorf("irc status = %q, want bad-gateway (dead IRC in tests)", statusBySource["irc"])
	}
	if statusBySource["prowlarr"] != "not-configured" {
		t.Errorf("prowlarr status = %q, want not-configured (no env in tests)",
			statusBySource["prowlarr"])
	}
	if len(out.Results) != 0 {
		t.Errorf("results = %+v, want none from the dead legs", out.Results)
	}
	if out.Took == "" {
		t.Error("took is empty, want a duration")
	}
}

// TestMetricsV52: the v5.2.0 metric families appear in the scrape and
// the counters move by exactly the recorded amounts. The counters are
// process-global (the handler tests that ran earlier may have moved
// them), so the assertion is a DELTA against a baseline scrape, never
// an absolute value.
func TestMetricsV52(t *testing.T) {
	s := newTokenServer(t)
	s.settings.SetPersist(true)
	scrape := func() string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		v52Router(s).ServeHTTP(w, req)
		return w.Body.String()
	}
	baseline := scrape()
	counterOf := func(body, metric string) int64 {
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, metric+" ") {
				var n int64
				fmt.Sscan(strings.TrimSpace(strings.TrimPrefix(line, metric+" ")), &n)
				return n
			}
		}
		return -1
	}
	if baseline == "" {
		t.Fatal("baseline scrape empty")
	}

	ws := s.apiWanted
	ws.mu.Lock()
	ws.addWanted("pending book", "", false)
	ws.addWanted("matched book", "", false)
	ws.items[1].MatchedAt = time.Now().UTC().Format(time.RFC3339)
	ws.mu.Unlock()
	recordUnifiedSearch(2)
	recordWantedMatched()
	recordWantedAutoFetch()

	body := scrape()
	for _, want := range []string{
		"openbooks_wanted_entries 2",
		"openbooks_wanted_unmatched 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\n---\n%s", want, body)
		}
	}
	for metric, wantDelta := range map[string]int64{
		"openbooks_unified_searches_total": 1,
		"openbooks_wanted_matches_total":   1,
		"openbooks_wanted_autofetches_total": 1,
	} {
		before, after := counterOf(baseline, metric), counterOf(body, metric)
		if before < 0 || after < 0 {
			t.Fatalf("metric %s not found (before=%d after=%d)", metric, before, after)
		}
		if after-before != wantDelta {
			t.Errorf("%s delta = %d (before %d, after %d), want %d",
				metric, after-before, before, after, wantDelta)
		}
	}
}
