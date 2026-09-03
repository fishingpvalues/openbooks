package server

// PotatoStack v5 integration tests: the inbound Newznab endpoint, the
// integrations overview/probe handler, the Newznab apikey auth form, and the
// download-completion webhook queue. The IRC search leg of /torznab shares
// performSearch with /api/v1/search and is covered indirectly (502/429 paths
// below); the peer clients are covered by their own httptest-backed tests.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/evan-buss/openbooks/core"
	"github.com/evan-buss/openbooks/server/integrations"
)

// torznabRouter mounts the real auth middleware + handler, the same way
// registerRoutes does.
func torznabRouter(s *server) chi.Router {
	r := chi.NewRouter()
	r.With(s.requireToken).Get("/torznab", s.torznabHandler())
	return r
}

// TestTorznabCaps: capability document, XML content type, and the <api>
// element carrying the deployment token (the Newznab way a tool learns the
// key).
func TestTorznabCaps(t *testing.T) {
	s := newTestServer(t, true)
	s.config.Token = "tok"
	router := torznabRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/torznab?t=caps&apikey=tok", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("t=caps with apikey -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("content-type %q, want xml", ct)
	}
	body := w.Body.String()
	for _, want := range []string{
		"<caps",
		`<searchfield name="q"`,
		`<api key="tok"`,
		"</caps>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("caps XML missing %q (body %s)", want, body)
		}
	}
}

// TestTorznabCapsTokenless: with no token configured the server is
// unauthenticated and caps must still serve (and must not advertise an
// <api> element).
func TestTorznabCapsTokenless(t *testing.T) {
	s := newTestServer(t, true)
	router := torznabRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/torznab?t=caps", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("tokenless t=caps -> %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "<api ") {
		t.Errorf("tokenless caps advertises an api element: %s", w.Body.String())
	}
}

// TestTorznabAuthForms pins every accepted token form on /torznab: the
// Newznab apikey query param (what Prowlarr/Readarr send), the Bearer
// header, and X-OpenBooks-Token - plus rejection of nothing/wrong.
func TestTorznabAuthForms(t *testing.T) {
	s := newTestServer(t, true)
	s.config.Token = "tok"
	router := torznabRouter(s)

	cases := []struct {
		name string
		url  string
		hdr  map[string]string
		want int
	}{
		{"no token", "/torznab?t=caps", nil, http.StatusUnauthorized},
		{"wrong apikey", "/torznab?t=caps&apikey=wrong", nil, http.StatusUnauthorized},
		{"apikey", "/torznab?t=caps&apikey=tok", nil, http.StatusOK},
		{"token param", "/torznab?t=caps&token=tok", nil, http.StatusOK},
		{"bearer header", "/torznab?t=caps", map[string]string{"Authorization": "Bearer " + "tok"}, http.StatusOK},
		{"x-header", "/torznab?t=caps", map[string]string{"X-OpenBooks-Token": "tok"}, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.url, nil)
			for k, v := range c.hdr {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Errorf("%s -> %d, want %d (body %s)", c.name, w.Code, c.want, w.Body.String())
			}
		})
	}
}

// TestTorznabSearchHandlerPaths covers the network-free branches of the
// search path: missing q, unsupported t, no-IRC 502, and the shared rate
// limit (429) - the last proves /torznab and /api/v1/search share one limit.
func TestTorznabSearchHandlerPaths(t *testing.T) {
	s := newTestServer(t, true)
	s.config.Token = "tok"
	// A port with nothing on it: startAPIClient's IRC join must fail
	// fast, which is the 502 path (performSearch cannot reach the bot).
	s.config.Server = "127.0.0.1:1"
	router := torznabRouter(s)

	// missing q
	req := httptest.NewRequest(http.MethodGet, "/torznab?t=search&apikey=tok", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing q -> %d, want 400", w.Code)
	}

	// unsupported t
	req = httptest.NewRequest(http.MethodGet, "/torznab?t=bogus&apikey=tok", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unsupported t -> %d, want 400", w.Code)
	}

	// no IRC available -> 502
	req = httptest.NewRequest(http.MethodGet, "/torznab?t=search&q=gatsby&apikey=tok", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("no IRC -> %d, want 502 (body %s)", w.Code, w.Body.String())
	}

	// shared rate limit: performSearch (the /torznab and /api/v1/search
	// shared core) checks the single api session's rate limit after the
	// connected check. With the session marked connected and a search
	// just run (lastSearch = now), it returns 429 before any IRC use -
	// proving /torznab shares the same single-flight + rate-limit state
	// as POST /api/v1/search.
	s.config.SearchTimeout = time.Minute
	s.api.mu.Lock()
	s.api.connected = true
	s.api.lastSearch = time.Now()
	s.api.mu.Unlock()
	_, status, retryAfter := s.performSearch("gatsby")
	if status != http.StatusTooManyRequests {
		t.Errorf("rate limited performSearch -> %d, want 429", status)
	} else if retryAfter <= 0 || retryAfter > 60 {
		// The 429 carries the backoff seconds so callers can set
		// Retry-After.
		t.Errorf("retryAfter = %d, want 1..60 for a one-minute limit", retryAfter)
	}
}

// TestNewznabItemXML round-trips the search document through the XML
// encoder/decoder to prove titles with XML metacharacters survive.
func TestNewznabItemXML(t *testing.T) {
	details := []core.BookDetail{
		{Title: "The <Great> Gatsby", Author: "Fitzgerald", Size: "1M", Format: "epub", Full: "!the.great.gatsby"},
		{Title: "1984", Author: "Orwell", Size: "900k", Format: "epub", Full: "!1984"},
	}
	s := newTestServer(t, true)
	doc := newznabResponse{Items: []newznabItem{}}
	for _, b := range details {
		doc.Items = append(doc.Items, newznabItem{
			Title:       b.Title,
			Index:       b.Author,
			Size:        b.Size,
			Comments:    b.Format,
			DownloadURL: s.libraryDownloadURL(b.Title, b.Full),
		})
	}
	var buf strings.Builder
	if err := xml.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back newznabResponse
	if err := xml.Unmarshal([]byte(buf.String()), &back); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if len(back.Items) != 2 {
		t.Fatalf("decoded %d items, want 2", len(back.Items))
	}
	if back.Items[0].Title != "The <Great> Gatsby" {
		t.Errorf("round-trip title = %q, want %q", back.Items[0].Title, "The <Great> Gatsby")
	}
	// The download URL points at the token-protected library endpoint with
	// the ! stripped.
	if back.Items[0].DownloadURL != "/api/v1/library/the.great.gatsby" {
		t.Errorf("downloadURL = %q, want /api/v1/library/the.great.gatsby", back.Items[0].DownloadURL)
	}
}

// TestLibraryDownloadURL pins the name derivation: the DCC file name (the
// Full field, !-prefixed) with ! and .temp stripped, spaces %20-encoded.
func TestLibraryDownloadURL(t *testing.T) {
	s := newTestServer(t, true)
	cases := []struct {
		title, full, want string
	}{
		{"Gatsby", "!great-gatsby.epub", "/api/v1/library/great-gatsby.epub"},
		{"Gatsby", "great-gatsby.epub.temp", "/api/v1/library/great-gatsby.epub"},
		{"1984", "", "/api/v1/library/1984"},
		{"A  B", "!a b.epub", "/api/v1/library/a%20b.epub"},
	}
	for _, c := range cases {
		if got := s.libraryDownloadURL(c.title, c.full); got != c.want {
			t.Errorf("libraryDownloadURL(%q, %q) = %q, want %q", c.title, c.full, got, c.want)
		}
	}
}

// integrationsRouter mounts /api/v1/integrations under the token guard.
func integrationsRouter(s *server) chi.Router {
	r := chi.NewRouter()
	r.With(s.requireToken).Get("/api/v1/integrations", s.integrationsOverviewHandler())
	return r
}

// TestIntegrationsOverviewDisabled: with no peer env, every peer reports
// enabled=false and ?probe=1 reports "not configured" without issuing a
// request.
func TestIntegrationsOverviewDisabled(t *testing.T) {
	for _, n := range []string{
		"OPENBOOKS_PROWLARR_URL", "PROWLARR_URL",
		"OPENBOOKS_AUDIOBOOKSHELF_URL", "AUDIOBOOKSHELF_URL",
		"OPENBOOKS_CALIBREWEB_URL", "CALIBREWEB_URL",
		"OPENBOOKS_READARR_URL", "READARR_URL",
		"OPENBOOKS_DOWNLOAD_CALLBACK", "DOWNLOAD_CALLBACK",
	} {
		t.Setenv(n, "")
	}
	s := newTestServer(t, true)
	router := integrationsRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations?probe=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("overview -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	for _, want := range []string{
		`"prowlarr"`,
		`"audiobookshelf"`,
		`"calibreweb"`,
		`"readarr"`,
		`"downloadCallback"`,
		`"probes"`,
		`"not configured"`,
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("overview missing %q (body %s)", want, w.Body.String())
		}
	}
	// None of the four peers may report enabled=true.
	if strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Errorf("overview reports an enabled peer, want all disabled: %s", w.Body.String())
	}
}

// TestIntegrationsOverviewConfigured: a configured peer reports its base URL
// and enabled=true, and a live peer (httptest) reports reachable on probe.
func TestIntegrationsOverviewConfigured(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/indexer"):
			io.WriteString(w, `[{"id":1,"name":"test","implementation":"NZB","protocols":"nzbtorrent","private":false,"supportsSearch":true}]`)
		case strings.HasSuffix(r.URL.Path, "/api/libraries"):
			io.WriteString(w, `{"libraries":[{"id":"1","name":"Books","type":"books","folders":[{"id":"f1","fullPath":"/x"}]}]}`)
		case strings.HasPrefix(r.URL.Path, "/opds"):
			w.Header().Set("Content-Type", "application/atom+xml")
			io.WriteString(w, `<feed xmlns="http://www.w3.org/2005/Atom"><title>root</title></feed>`)
		case strings.HasSuffix(r.URL.Path, "/api/v1/book"):
			io.WriteString(w, `[]`)
		default:
			io.WriteString(w, `{}`)
		}
		w.Header().Set("Content-Type", "application/json")
	}))
	defer peer.Close()

	for _, n := range []string{"PROWLARR_URL", "AUDIOBOOKSHELF_URL", "CALIBREWEB_URL", "READARR_URL"} {
		t.Setenv(n, "")
	}
	t.Setenv("PROWLARR_URL", peer.URL)
	t.Setenv("AUDIOBOOKSHELF_URL", peer.URL)
	t.Setenv("CALIBREWEB_URL", peer.URL)
	t.Setenv("READARR_URL", peer.URL)
	s := newTestServer(t, true)
	router := integrationsRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations?probe=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("overview -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"enabled":true`) {
		t.Errorf("no peer reports enabled=true: %s", body)
	}
	if !strings.Contains(body, `"reachable":true`) {
		t.Errorf("no peer reports reachable=true (live peers should probe ok): %s", body)
	}
}

// --- peer client contract tests (httptest-shaped like the live services) ---

// TestProwlarrClientContract pins the live-verified Prowlarr v1 contract:
// X-Api-Key header, GET /api/v1/search with query/type/limit params, bare
// array of releases.
func TestProwlarrClientContract(t *testing.T) {
	var gotAuth, gotQuery string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Api-Key")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"title":"The Great Gatsby","size":1234,"guid":"g-1","indexerName":"BooksUsenet"}]`)
	}))
	defer peer.Close()

	c := integrations.NewProwlarr(peer.URL, "pkey")
	if !c.Enabled() {
		t.Fatal("client with URL must be enabled")
	}
	results, err := c.SearchBooks(context.Background(), "gatsby", "book", 10)
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(results) != 1 || results[0].Title != "The Great Gatsby" {
		t.Fatalf("parsed %d results (%+v), want 1 titled The Great Gatsby", len(results), results)
	}
	if gotAuth != "pkey" {
		t.Errorf("X-Api-Key = %q, want %q", gotAuth, "pkey")
	}
	for _, want := range []string{"query=gatsby", "type=book", "limit=10"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("search query %q missing %q", gotQuery, want)
		}
	}

	// A disabled client must not issue a request.
	off := integrations.NewProwlarr("", "")
	if off.Enabled() {
		t.Error("empty URL must be disabled")
	}
	if _, err := off.SearchBooks(context.Background(), "x", "book", 1); err == nil {
		t.Error("disabled SearchBooks: want ErrDisabled, got nil")
	}
}

// TestAudiobookshelfClientContract pins the Bearer auth + /api/libraries
// envelope (the {"libraries":[...]} shape, not a bare array).
func TestAudiobookshelfClientContract(t *testing.T) {
	var gotAuth string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"libraries":[{"id":"lib1","name":"Books","type":"books","folders":[{"id":"f1","fullPath":"/x"}]},{"id":"lib2","name":"Audiobooks","type":"audiobooks","folders":[]}]}`)
	}))
	defer peer.Close()

	c := integrations.NewAudiobookshelf(peer.URL, "abs-key")
	libs, err := c.ListLibraries(context.Background())
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 2 || libs[0].Name != "Books" || libs[0].FolderCount != 1 {
		t.Fatalf("parsed %+v, want 2 libs, first Books with 1 folder", libs)
	}
	if gotAuth != "Bearer abs-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer abs-key")
	}
}

// TestCalibreWebClientContract drives SearchOPDS against a canned OPDS feed
// and checks the query-param search form + entry parsing.
func TestCalibreWebClientContract(t *testing.T) {
	var gotPath, gotTerm string
	feed := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Search results</title>
  <entry>
    <title>The Hobbit</title>
    <author><name>Tolkien, J.R.R.</name></author>
    <id>book-1</id>
    <summary>There is a hole in the ground.</summary>
    <link rel="alternate" type="application/epub+zip" href="/books/1/download"/>
  </entry>
  <entry>
    <title>Dune</title>
    <author><name>Frank Herbert</name></author>
    <id>book-2</id>
    <summary>Let me die with my enemy.</summary>
  </entry>
</feed>`
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTerm = r.URL.Query().Get("searchTerm")
		w.Header().Set("Content-Type", "application/atom+xml")
		io.WriteString(w, feed)
	}))
	defer peer.Close()

	c := integrations.NewCalibreWeb(peer.URL)
	feedOut, err := c.SearchOPDS(context.Background(), "hobbit")
	if err != nil {
		t.Fatalf("SearchOPDS: %v", err)
	}
	if len(feedOut.Entries) != 2 {
		t.Fatalf("parsed %d entries, want 2 (%+v)", len(feedOut.Entries), feedOut)
	}
	if feedOut.Entries[0].Title != "The Hobbit" || feedOut.Entries[0].Author != "Tolkien, J.R.R." {
		t.Errorf("first entry = %+v, want The Hobbit / Tolkien", feedOut.Entries[0])
	}
	if gotTerm != "hobbit" {
		t.Errorf("searchTerm = %q, want %q", gotTerm, "hobbit")
	}
	if gotPath != "/opds/" {
		t.Errorf("search path = %q, want /opds/", gotPath)
	}

	// RootFeed hits the OPDS root.
	if _, err := c.RootFeed(context.Background()); err != nil {
		t.Errorf("RootFeed: %v", err)
	}
}

// TestReadarrClientContract pins the v1 lookup shape: X-Api-Key,
// /api/v1/book/lookup?title=, bare array.
func TestReadarrClientContract(t *testing.T) {
	var gotAuth, gotTitle string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Api-Key")
		gotTitle = r.URL.Query().Get("title")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"id":1,"title":"Dune","author":"Frank Herbert","year":1965}]`)
	}))
	defer peer.Close()

	c := integrations.NewReadarr(peer.URL, "rkey")
	matches, err := c.Lookup(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(matches) != 1 || matches[0].Title != "Dune" || matches[0].Author != "Frank Herbert" {
		t.Fatalf("parsed %+v, want 1 Dune / Frank Herbert", matches)
	}
	if gotAuth != "rkey" {
		t.Errorf("X-Api-Key = %q, want %q", gotAuth, "rkey")
	}
	if gotTitle != "Dune" {
		t.Errorf("title = %q, want %q", gotTitle, "Dune")
	}
}

// --- download-completion webhook ---

// TestDownloadCallbackFIFO: two queued callbacks receive two completions.
// The FIFO drain in recordAPIDownload pairs each completion with the
// OLDEST queued callback - the invariant we assert is the pairing
// (a.epub -> !a, b.epub -> !b). Delivery to the test server happens in
// concurrent goroutines, so we check the set of pairs, not arrival order.
func TestDownloadCallbackFIFO(t *testing.T) {
	var mu sync.Mutex
	var delivered []string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev struct {
			Status string `json:"status"`
			Book   string `json:"book"`
			File   string `json:"file"`
		}
		_ = json.Unmarshal(body, &ev)
		mu.Lock()
		delivered = append(delivered, ev.Book+"->"+ev.File)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer peer.Close()

	// The callback guard (callback_guard.go) refuses loopback and private
	// targets unless the host is allowlisted, and httptest binds 127.0.0.1 -
	// so this test has to opt in exactly the way an operator does.
	t.Setenv(callbackAllowlistEnv, "127.0.0.1")

	s := newTestServer(t, true)
	s.api.mu.Lock()
	s.api.downloadCallbacks = []downloadCallback{
		{Book: "!a", URL: peer.URL},
		{Book: "!b", URL: peer.URL},
	}
	s.api.mu.Unlock()

	s.recordAPIDownload("a.epub")
	s.recordAPIDownload("b.epub")

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(delivered)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 2 {
		t.Fatalf("delivered %v, want 2 events", delivered)
	}
	// The FIFO pairing: a.epub completed first, so it must carry the
	// FIRST queued book (!a); b.epub the second (!b). A broken pairing
	// (e.g. both callbacks fired with !a) fails here.
	want := map[string]bool{"!a->a.epub": false, "!b->b.epub": false}
	for _, d := range delivered {
		if _, ok := want[d]; !ok {
			t.Fatalf("unexpected delivery %q in %v", d, delivered)
		}
		want[d] = true
	}
}

// TestDownloadCallbackDeadSink: a sink that always 502s must give up after
// its retries without blocking or panicking.
func TestDownloadCallbackDeadSink(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer dead.Close()

	s := newTestServer(t, true)
	done := make(chan struct{})
	go func() {
		s.fireDownloadCallback(downloadCallback{Book: "!x", URL: dead.URL}, "x.epub")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fireDownloadCallback did not return after a dead sink")
	}
}

// TestDownloadHandlerCallbackValidation: POST /api/v1/download accepts a
// valid callbackUrl and rejects the request at the IRC step (502) when
// the IRC server is unreachable - validation happens BEFORE the IRC
// session opens, so this also proves the 502 is not shadowing a 400.
// The 400 paths for bad callbackUrl forms (wrong scheme, relative URL,
// missing host) are covered by TestDownloadHandlerValidation, which
// asserts them with a dead IRC server to pin that ordering.
func TestDownloadHandlerCallbackValidation(t *testing.T) {
	s := newTestServer(t, true)
	s.config.Server = "127.0.0.1:1" // IRC connect fails fast -> 502

	router := chi.NewRouter()
	router.Post("/api/v1/download", s.downloadHandler())

	// Valid book, valid callback: handler must get PAST the callback check
	// (no 400) and fail at the IRC step (502). Proves the callback URL
	// validation accepts it.
	body := `{"book":"!a","callbackUrl":"https://example.com/hook"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/download", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusBadRequest {
		t.Errorf("valid callbackUrl rejected as 400: %s", w.Body.String())
	}
	if w.Code != http.StatusBadGateway {
		t.Errorf("IRC-down download -> %d, want 502 (body %s)", w.Code, w.Body.String())
	}
}

// TestTokenMatchesApikeyForm pins the Newznab apikey auth form at the
// tokenMatches level (header-free, the way indexer tools call).
func TestTokenMatchesApikeyForm(t *testing.T) {
	s := newTestServer(t, true)
	s.config.Token = "tok"

	if s.tokenMatches(reqWith("/torznab?t=caps", nil)) {
		t.Error("no apikey must not match")
	}
	if s.tokenMatches(reqWith("/torznab?t=caps&apikey=wrong", nil)) {
		t.Error("wrong apikey must not match")
	}
	if !s.tokenMatches(reqWith("/torznab?t=caps&apikey=tok", nil)) {
		t.Error("correct apikey must match")
	}
}

// TestOpenAPISpecDrift pins the embedded OpenAPI document: it must parse,
// carry the patch-line version, and document every v5.x endpoint - the
// embedded spec is what GET /openapi.json serves, and it is easy to add a
// route and forget to document it.
func TestOpenAPISpecDrift(t *testing.T) {
	var spec struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatalf("embedded openapi.json does not parse: %v", err)
	}
	if spec.Info.Version != "5.3.0" {
		t.Errorf("openapi version = %q, want 5.1.2", spec.Info.Version)
	}
	for _, p := range []string{
		"/torznab",
		"/api/v1/integrations",
		"/api/v1/download",
		"/api/v1/downloads",
		"/api/v1/metrics",
		"/api/v1/search",
		"/api/v1/health",
		"/api/v1/library",
		// v5.3.0: per-job download tracking, sha256 verify, the search
		// cache stats + clean.
		"/api/v1/jobs",
		"/api/v1/jobs/{id}/retry",
		"/api/v1/verify",
		"/api/v1/search-cache",
		"/api/v1/search-cache/clean",
	} {
		if _, ok := spec.Paths[p]; !ok {
			t.Errorf("openapi.json missing path %q", p)
		}
	}
}

// reqWith builds a GET request with optional headers.
func reqWith(url string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}
