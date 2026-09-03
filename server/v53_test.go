package server

// v5.3.0 handler tests: the scoped-token 403 contract, the TTL search
// cache, the quality filters, and the cross-source dedupe annotation.
// Network-free by construction (same pattern as api_test.go and
// wanted_test.go): the IRC-dependent paths run against a dead address
// (127.0.0.1:1) and assert the validation/bad-gateway behavior; the cache
// and filter functions are exercised directly.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evan-buss/openbooks/core"
)

// TestScopedTokenContract: with a scoped token configured, the wrong
// scope is a 403 (right identity, wrong privilege), the right scope is a
// 200, and the primary token keeps full access to every route. A token
// that matches nothing is still a 401.
func TestScopedTokenContract(t *testing.T) {
	s := newTestServer(t, true)
	s.config.Token = "primary-tok"
	s.config.ScopedTokens = map[string]scopeSet{
		"ui-only":    {by: map[string]bool{"ui": true}},
		"admin-only": {by: map[string]bool{"admin": true}},
	}
	token := "primary-tok"

	do := func(tok, path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		s.requireToken(s.requireScopeMW("ui")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(w, req)
		return w.Code
	}

	// ui scope: a 200 through the ui gate.
	if c := do("ui-only", "/api/v1/library"); c != http.StatusOK {
		t.Errorf("ui token through ui gate -> %d, want 200", c)
	}
	// admin-only token through the ui gate: 403 (not 401).
	if c := do("admin-only", "/api/v1/library"); c != http.StatusForbidden {
		t.Errorf("admin token through ui gate -> %d, want 403", c)
	}
	// No token: 401.
	if c := do("", "/api/v1/library"); c != http.StatusUnauthorized {
		t.Errorf("no token -> %d, want 401", c)
	}
	// Unknown token: 401.
	if c := do("wrong-tok", "/api/v1/library"); c != http.StatusUnauthorized {
		t.Errorf("unknown token -> %d, want 401", c)
	}
	// Primary token: full privilege through every gate.
	if c := do(token, "/api/v1/library"); c != http.StatusOK {
		t.Errorf("primary token through ui gate -> %d, want 200", c)
	}

	// tokenMatches: credential-known bool (the torznab probe shape).
	req := httptest.NewRequest(http.MethodGet, "/torznab?t=caps&apikey=ui-only", nil)
	if !s.tokenMatches(req) {
		t.Errorf("tokenMatches(apikey=ui-only) = false, want true")
	}
	req = httptest.NewRequest(http.MethodGet, "/torznab?t=caps&apikey=wrong", nil)
	if s.tokenMatches(req) {
		t.Errorf("tokenMatches(apikey=wrong) = true, want false")
	}
}

// TestSearchCacheTTL: a store then lookup returns the response with the
// note marked cached; a different filter set is a different key; a
// zero-TTL cache always misses; clean drops every entry.
func TestSearchCacheTTL(t *testing.T) {
	c := newSearchCache(time.Hour)
	resp := APISearchResponse{Books: []core.BookDetail{{Title: "a"}}}

	k := cacheKey("some book", QualityFilters{})
	c.store(k, resp)
	if got, ok := c.lookup(k); !ok || got.Note != "cached" {
		t.Errorf("lookup after store = (%+v, %v), want the response with note cached", got, ok)
	}

	// A different filter set is a different key (the cache is per-request
	// identity, not per-query).
	k2 := cacheKey("some book", QualityFilters{Formats: []string{"epub"}})
	if _, ok := c.lookup(k2); ok {
		t.Errorf("lookup with a different filter set hit, want a miss (different key)")
	}

	// Disabled cache (zero TTL): never stores, never hits.
	d := newSearchCache(0)
	d.store("x", resp)
	if _, ok := d.lookup("x"); ok {
		t.Errorf("zero-TTL cache hit, want a miss (disabled)")
	}

	// stats: entries/hits/misses, and clean returns the dropped count.
	entries, hits, misses, ttl := c.stats()
	if entries != 1 || hits != 1 || misses != 1 || ttl != time.Hour {
		t.Errorf("stats = (%d,%d,%d,%s), want (1,1,1,1h)", entries, hits, misses, ttl)
	}
	if n := c.clean(); n != 1 {
		t.Errorf("clean = %d, want 1", n)
	}
	if entries, _, _, _ := c.stats(); entries != 0 {
		t.Errorf("entries after clean = %d, want 0", entries)
	}
}

// TestApplyQualityFilters: the format, size and prefer filters each
// narrow the set; an empty filter struct is a no-op (the v5.2 behavior).
// Note: sizes use the IRC bot's wire format (no space, 1024 scale:
// "237.78KB", "1.2MB"), which is what core.ParseSizeToBytes parses.
func TestApplyQualityFilters(t *testing.T) {
	in := []unifiedResult{
		{Source: "irc", Title: "audiobook", Author: "x", Format: "mp3", Size: "10MB"},
		{Source: "irc", Title: "ebook", Author: "x", Format: "epub", Size: "1MB"},
		{Source: "prowlarr", Title: "big", Author: "y", Format: "pdf", Size: "900MB"},
	}

	// No filters: everything passes.
	if got := applyQualityFilters(in, QualityFilters{}); len(got) != 3 {
		t.Errorf("no filters -> %d results, want 3", len(got))
	}

	// Format: only the ebook survives.
	got := applyQualityFilters(in, QualityFilters{Formats: []string{"epub"}})
	if len(got) != 1 || got[0].Title != "ebook" {
		t.Errorf("format=epub -> %+v, want only the ebook", got)
	}

	// Max size: the 900MB release is dropped (10MB and 1MB stay).
	got = applyQualityFilters(in, QualityFilters{MaxSizeBytes: 100 * 1024 * 1024})
	if len(got) != 2 {
		t.Errorf("maxSize 100MB -> %d results, want 2 (the 900MB dropped)", len(got))
	}

	// Prefer ebook: the audiobook is dropped, the ebook and the (neither)
	// pdf stay.
	got = applyQualityFilters(in, QualityFilters{Prefer: "ebook"})
	if len(got) != 2 {
		t.Errorf("prefer=ebook -> %d results, want 2 (the mp3 dropped)", len(got))
	}
}

// TestAnnotateDedupe: the same book from two sources is one dedupe group
// of size 2; a lone source is group size 1 (a unique result keeps its
// count - the field is always set, 1 means "no cross-source partner").
func TestAnnotateDedupe(t *testing.T) {
	in := []unifiedResult{
		{Source: "irc", Title: "the same book", Author: "author"},
		{Source: "prowlarr", Title: "the same book", Author: "author"},
		{Source: "irc", Title: "a different book", Author: "author"},
	}
	annotateDedupe(in)
	if in[0].DedupGroup != 2 || in[1].DedupGroup != 2 {
		t.Errorf("dedupe group for the shared book = (%d,%d), want (2,2)",
			in[0].DedupGroup, in[1].DedupGroup)
	}
	if in[2].DedupGroup != 1 {
		t.Errorf("dedupe group for the lone book = %d, want 1 (unique)", in[2].DedupGroup)
	}
}

// TestWantedPollCacheFirst: a wanted entry whose query is already in the
// search cache is served from the cache on a poll round (zero IRC
// traffic), the entry is matched, and the match carries the cache note.
func TestWantedPollCacheFirst(t *testing.T) {
	s := newTokenServer(t)
	s.settings.SetPersist(true)
	// The test server starts with a disabled (zero-TTL) cache; the poll
	// round's cache-first path needs a live TTL to exercise it.
	s.searchCache = newSearchCache(time.Hour)

	// Seed the cache with an answer for the entry's query.
	resp := APISearchResponse{Books: []core.BookDetail{{Title: "cached", Full: "!get cached"}}}
	s.searchCache.store(cacheKey("cached book", QualityFilters{}), resp)

	ws := s.apiWanted
	ws.mu.Lock()
	it, err := ws.addWanted("cached book", "", false, QualityFilters{}, false)
	ws.mu.Unlock()
	if err != nil {
		t.Fatalf("addWanted: %v", err)
	}

	status := s.wantedPollOnce()
	if !strings.Contains(status, "cache") {
		t.Errorf("poll status = %q, want the cache-served report", status)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if it.MatchedAt == "" || len(it.Matched) != 1 {
		t.Errorf("entry after cache-served round: MatchedAt=%q Matched=%v, want 1 match",
			it.MatchedAt, it.Matched)
	}
	if len(it.SeenReleases) != 1 {
		t.Errorf("SeenReleases = %v, want the matched release recorded", it.SeenReleases)
	}
}

// TestWantedBackoff: a failed poll round (dead IRC) sets a NextAttemptAt
// in the future and increments FailedRounds; the entry is then not due
// until the backoff window passes.
func TestWantedBackoff(t *testing.T) {
	s := newTokenServer(t)
	ws := s.apiWanted
	ws.mu.Lock()
	it, err := ws.addWanted("will fail", "", false, QualityFilters{}, false)
	ws.mu.Unlock()
	if err != nil {
		t.Fatalf("addWanted: %v", err)
	}

	s.wantedPollOnce() // dead IRC (127.0.0.1:1) -> a failed round

	ws.mu.Lock()
	defer ws.mu.Unlock()
	if it.FailedRounds != 1 {
		t.Errorf("FailedRounds = %d, want 1", it.FailedRounds)
	}
	if it.NextAttemptAt == "" {
		t.Fatalf("NextAttemptAt empty after a failed round")
	}
	if wantedDue(it) {
		t.Errorf("wantedDue = true, want false (backoff window active)")
	}
	// Push the backoff window into the past: the entry becomes due again.
	it.NextAttemptAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	if !wantedDue(it) {
		t.Errorf("wantedDue = false after the window passed, want true")
	}
}

// TestParseScopedTokens: the env table parses (semicolon-separated
// entries, "token:scope,scope"), an unknown scope name fails loud, and a
// duplicate token is rejected.
func TestParseScopedTokens(t *testing.T) {
	table, err := parseScopedTokens("tok1:ui,search;tok2:newznab")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !table["tok1"].has("ui") || !table["tok1"].has("search") || table["tok1"].has("admin") {
		t.Errorf("tok1 scopes = %+v, want ui+search only", table["tok1"])
	}
	if !table["tok2"].has("newznab") {
		t.Errorf("tok2 scopes = %+v, want newznab", table["tok2"])
	}
	if _, err := parseScopedTokens("tok1:ui,bogus-scope"); err == nil {
		t.Errorf("unknown scope parsed, want an error")
	}
	if _, err := parseScopedTokens("tok1:ui;tok1:search"); err == nil {
		t.Errorf("duplicate token parsed, want an error")
	}
}
