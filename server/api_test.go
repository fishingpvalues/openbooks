package server

// API handler tests for the v5 /api/v1 REST surface: the handlers that
// routes_test.go and integrations_test.go do not cover (health, the v5
// library list/file/delete handlers, search + download request validation,
// safeJoin) plus the auth 401 contract and the public-route boundaries
// (SPA + openapi.json stay open, everything else needs the token).
//
// Network-free by construction: the IRC-dependent paths (real search
// results, DCC completion) run against a dead IRC address and assert the
// 502/validation behavior; the shared state (rate limit, single-flight)
// is seeded directly, the same way integrations_test.go does it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// apiRouter mounts the real /api/v1 tree (minus the SPA) under the real
// token middleware, the same shape registerRoutes produces.
func apiRouter(s *server) chi.Router {
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
	})
	return r
}

// newTokenServer is a server with a token, a dead IRC address, and the
// v5.1.1 version stamp health reports.
func newTokenServer(t *testing.T) *server {
	t.Helper()
	s := newTestServer(t, true)
	s.config.Token = "testtok"
	s.config.Version = "5.1.2"
	s.config.Server = "127.0.0.1:1" // IRC connect fails fast
	return s
}

// TestHealthHandler: the token probe. Exact JSON contract and both
// persist states.
func TestHealthHandler(t *testing.T) {
	for _, persist := range []bool{true, false} {
		p := persist
		s := newTokenServer(t)
		s.settings.SetPersist(p)
		router := apiRouter(s)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("persist=%v: health -> %d, want 200 (body %s)", p, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("persist=%v: content-type %q, want application/json", p, ct)
		}
		var out HealthResponse
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("persist=%v: decode: %v (body %s)", p, err, w.Body.String())
		}
		if out.Name != "openbooks" || out.Version != "5.1.2" || out.Persist != p {
			t.Errorf("persist=%v: health = %+v, want {openbooks 5.1.2 %v}", p, out, p)
		}
		// No api session has been started in this server: ircConnected is
		// false (the HTTP probe is alive but the IRC session has not been
		// established yet - the field is the signal that distinguishes
		// the two).
		if out.IRCConnected {
			t.Errorf("persist=%v: ircConnected = true before any search, want false", p)
		}
	}
}

// TestPublicRouteBoundaries pins which routes stay open with a token
// configured: the SPA catch-all and /openapi.json serve without one, every
// /api/v1 route requires it.
func TestPublicRouteBoundaries(t *testing.T) {
	s := newTokenServer(t)
	router := apiRouter(s)

	// openapi.json: public documentation.
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /openapi.json (no token) -> %d, want 200", w.Code)
	}
	var spec struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.NewDecoder(w.Body).Decode(&spec); err != nil {
		t.Fatalf("openapi.json does not parse: %v", err)
	}
	if spec.Info.Version != "5.1.2" {
		t.Errorf("openapi version = %q, want 5.1.2", spec.Info.Version)
	}

	// Every /api/v1 route requires the token.
	for _, p := range []string{
		"/api/v1/health",
		"/api/v1/library",
		"/api/v1/downloads",
		"/api/v1/metrics",
		"/api/v1/settings",
		"/api/v1/integrations",
	} {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s (no token) -> %d, want 401", p, w.Code)
		}
	}
}

// TestUnauthorizedContract: the 401 carries a JSON error body and the
// WWW-Authenticate header the UI (and curl) rely on.
func TestUnauthorizedContract(t *testing.T) {
	s := newTokenServer(t)
	router := apiRouter(s)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token -> %d, want 401", w.Code)
	}
	if wa := w.Header().Get("WWW-Authenticate"); wa != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", wa)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("401 body is not JSON: %v (%q)", err, w.Body.String())
	}
	if body["error"] != "unauthorized" || body["message"] == "" {
		t.Errorf("401 body = %v, want error=unauthorized with a message", body)
	}
}

// TestLibraryListHandlerV5: the v5 listing - 404 JSON when persist is off,
// otherwise a filtered list with the v5 field set.
func TestLibraryListHandlerV5(t *testing.T) {
	// persist off -> 404 with a JSON error.
	s := newTestServer(t, false)
	w := httptest.NewRecorder()
	s.libraryListHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/library", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("persist=off -> %d, want 404", w.Code)
	}
	var errBody map[string]string
	if err := json.NewDecoder(w.Body).Decode(&errBody); err != nil {
		t.Fatalf("404 body not JSON: %v (%q)", err, w.Body.String())
	}

	// persist on -> list with the v5 contract.
	s = newTokenServer(t)
	books := filepath.Join(s.settings.GetDownloadDir(), "books")
	if err := os.WriteFile(filepath.Join(books, "gatsby.epub"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(books, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, ".hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "wip.temp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	w = httptest.NewRecorder()
	s.libraryListHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/library", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("persist=on -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var out []BookFile
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	if len(out) != 1 {
		t.Fatalf("listed %d books (%+v), want only gatsby.epub", len(out), out)
	}
	b := out[0]
	if b.Name != "gatsby.epub" || b.DownloadLink != "/api/v1/library/gatsby.epub" || b.Size != 5 {
		t.Errorf("book entry = %+v, want name gatsby.epub, v5 link, size 5", b)
	}
	if b.ModifiedAt.IsZero() {
		t.Errorf("modifiedAt zero, want the file mtime")
	}
}

// TestLibraryFileHandlerV5: subfolder download works, traversal is a 400,
// a missing file is a 404, persist-off is a 404.
func TestLibraryFileHandlerV5(t *testing.T) {
	s := newTokenServer(t)
	books := filepath.Join(s.settings.GetDownloadDir(), "books")
	sub := filepath.Join(books, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a b.epub"), []byte("nested book"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Subfolder book (percent-encoded space) serves.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/library/sub/a%20b.epub", nil)
	w := httptest.NewRecorder()
	s.libraryFileHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("subfolder book -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "nested book" {
		t.Errorf("served %q, want %q", w.Body.String(), "nested book")
	}

	// Traversal: the URL percent-encodes the separators, so PathUnescape
	// turns them back into slashes AFTER routing - the attack form the
	// legacy delete endpoint shipped with. Must be a 400.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/library/..%2F..%2Fetc%2Fpasswd", nil)
	w = httptest.NewRecorder()
	s.libraryFileHandler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("traversal -> %d, want 400", w.Code)
	}

	// Missing file is a 404 (ServeFile), not a 500.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/library/nope.epub", nil)
	w = httptest.NewRecorder()
	s.libraryFileHandler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing file -> %d, want 404", w.Code)
	}

	// persist off -> 404 even for an existing file.
	s2 := newTestServer(t, false)
	w = httptest.NewRecorder()
	s2.libraryFileHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/library/sub/a%20b.epub", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("persist=off -> %d, want 404", w.Code)
	}
}

// TestLibraryDeleteHandlerV5: legitimate delete, traversal rejection,
// dotfile rejection (the v5 handler used to accept dotfiles while the
// listing and the legacy handler did not), and a missing file -> 500.
func TestLibraryDeleteHandlerV5(t *testing.T) {
	s := newTokenServer(t)
	router := chi.NewRouter()
	router.Delete("/library/{name}", s.libraryDeleteHandler())

	books := filepath.Join(s.settings.GetDownloadDir(), "books")
	victim := filepath.Join(books, "doomed.epub")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Traversal and dotfile attacks: 400, and the outside file survives.
	for _, a := range []string{
		".." + "%2F" + ".." + "%2F" + "secret.txt",
		"%2E%2E%2F" + "secret.txt",
		".hidden",
		"..",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/library/"+a, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("DELETE /library/%s -> %d, want 400", a, w.Code)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the library was deleted: %v", err)
	}

	// Missing file -> 500 (os.Remove error path).
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/library/never-was.epub", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("DELETE missing file -> %d, want 500", w.Code)
	}

	// Legitimate delete -> 204 and the file is gone.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/library/doomed.epub", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE doomed.epub -> %d, want 204", w.Code)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Errorf("doomed.epub still exists after delete")
	}
}

// TestSearchHandlerValidation: the request-validation layer of
// POST /api/v1/search - bad/missing query is a 400 before any IRC use, a
// dead IRC server is a 502, the rate limit is a 429 (with Retry-After)
// and a search already in flight is a 409 (both for wait=true and
// wait=false).
func TestSearchHandlerValidation(t *testing.T) {
	s := newTokenServer(t)
	router := apiRouter(s)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	if code := post(`{`).Code; code != http.StatusBadRequest {
		t.Errorf("bad JSON -> %d, want 400", code)
	}
	if code := post(`{}`).Code; code != http.StatusBadRequest {
		t.Errorf("missing query -> %d, want 400", code)
	}
	if code := post(`{"query":"   "}`).Code; code != http.StatusBadRequest {
		t.Errorf("blank query -> %d, want 400", code)
	}

	// Dead IRC: connect fails fast -> 502, wait and no-wait alike.
	if code := post(`{"query":"gatsby"}`).Code; code != http.StatusBadGateway {
		t.Errorf("wait=true, IRC down -> %d, want 502", code)
	}
	if code := post(`{"query":"gatsby","wait":false}`).Code; code != http.StatusBadGateway {
		t.Errorf("wait=false, IRC down -> %d, want 502", code)
	}

	// Rate limit: one search just ran, SearchTimeout is a minute away.
	s.config.SearchTimeout = time.Minute
	s.api.mu.Lock()
	s.api.connected = true
	s.api.lastSearch = time.Now()
	s.api.mu.Unlock()

	// wait=true: 429 + Retry-After header (the *arr-client contract).
	w := post(`{"query":"gatsby"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("rate limited -> %d, want 429", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Errorf("429 (wait=true) missing Retry-After header")
	}

	// wait=false: 429 + Retry-After header too.
	w = post(`{"query":"gatsby","wait":false}`)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("rate limited (wait=false) -> %d, want 429", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Errorf("429 (wait=false) missing Retry-After header")
	}

	// Single-flight: a search is already pending -> 409 (no Retry-After;
	// the in-flight search will finish soon and there is no fixed wait).
	s.api.mu.Lock()
	s.api.lastSearch = time.Time{} // lift the rate limit
	s.api.pendingSearch = make(chan searchOutcome, 1)
	s.api.mu.Unlock()
	w = post(`{"query":"gatsby"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("search in flight -> %d, want 409", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "" {
		t.Errorf("409 carries Retry-After %q, want none", ra)
	}
}

// TestTorznabRetryAfterHeader: the /torznab 429 carries Retry-After as
// well - indexer tools (Prowlarr/Readarr) are the primary consumers of
// this endpoint and back off on exactly this header.
func TestTorznabRetryAfterHeader(t *testing.T) {
	s := newTokenServer(t)
	s.config.SearchTimeout = time.Minute
	s.api.mu.Lock()
	s.api.connected = true
	s.api.lastSearch = time.Now()
	s.api.mu.Unlock()

	router := torznabRouter(s)
	req := httptest.NewRequest(http.MethodGet, "/torznab?t=search&q=gatsby&apikey=testtok", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited /torznab -> %d, want 429", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Errorf("429 /torznab missing Retry-After header (headers: %v)", w.Header())
	}
}

// TestDownloadHandlerValidation: the request-validation layer of
// POST /api/v1/download. The callback URL is validated BEFORE the IRC
// session opens, so every 400 here is asserted with a dead IRC server -
// that is the ordering guarantee (a bad callback must not cost a
// connection attempt).
func TestDownloadHandlerValidation(t *testing.T) {
	s := newTokenServer(t) // IRC dead
	router := apiRouter(s)

	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/download", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(`{`); code != http.StatusBadRequest {
		t.Errorf("bad JSON -> %d, want 400", code)
	}
	if code := post(`{"book":"no-bang"}`); code != http.StatusBadRequest {
		t.Errorf("book without ! -> %d, want 400", code)
	}
	for _, cb := range []string{
		"ftp://example.com/hook", // wrong scheme
		"example.com/hook",       // no scheme (relative)
		"https://",               // no host
		"javascript:alert(1)",    // not http(s)
	} {
		body := `{"book":"!x","callbackUrl":` + jsonString(cb) + `}`
		if code := post(body); code != http.StatusBadRequest {
			t.Errorf("callbackUrl %q -> %d, want 400", cb, code)
		}
	}

	// Valid request with the IRC down: the 502 is the expected end of
	// the path (validation passed, connection failed).
	if code := post(`{"book":"!x","callbackUrl":"https://example.com/hook"}`); code != http.StatusBadGateway {
		t.Errorf("valid request, IRC down -> %d, want 502", code)
	}
}

// TestSafeJoin pins the containment helper the v5 library handlers share:
// legal names (including subfolders) join, every traversal form is
// rejected. Backslash is not a separator on POSIX, so a name that
// decodes to "..\\x" is a legitimate single-segment file name there -
// the delete handler rejects backslashes separately (validBookName),
// and the traversal forms the v5 file handler must stop all arrive
// via %2F, which safeJoin rejects.
func TestSafeJoin(t *testing.T) {
	base := "/books"
	valid := []string{
		"1984.epub",
		"sub/a b.epub",
		"a/b/c.d.tar",
	}
	for _, name := range valid {
		if _, ok := safeJoin(base, name); !ok {
			t.Errorf("safeJoin(%q) rejected a legal name", name)
		}
	}
	invalid := []string{
		"",
		".",
		"..",
		"../x",
		"a/../../etc",
		"a\x00b",
	}
	for _, name := range invalid {
		if _, ok := safeJoin(base, name); ok {
			t.Errorf("safeJoin(%q) accepted a traversal name", name)
		}
	}
}

// TestDownloadCallbackRetry: a sink that fails once and succeeds on the
// retry must receive the event (the 1-retry policy in
// fireDownloadCallback). The 502 dead-sink give-up is covered by
// TestDownloadCallbackDeadSink in integrations_test.go.
func TestDownloadCallbackRetry(t *testing.T) {
	var calls int32
	var got bool
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		got = true
		w.WriteHeader(http.StatusOK)
	}))
	defer peer.Close()

	// The SSRF guard (callback_guard.go) refuses loopback targets unless the
	// host is allowlisted, and httptest binds 127.0.0.1 - so this test has to
	// opt in exactly the way an operator does.
	t.Setenv(callbackAllowlistEnv, "127.0.0.1")

	s := newTestServer(t, true)
	done := make(chan struct{})
	go func() {
		s.fireDownloadCallback(downloadCallback{Book: "!x", URL: peer.URL}, "x.epub")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("fireDownloadCallback did not return")
	}
	if !got {
		t.Errorf("callback not delivered on the retry (calls=%d)", calls)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (initial + one retry)", calls)
	}
}

// TestDownloadsHandler: the v5.1.2 completion endpoint - 404 JSON when
// persist is off, a JSON array when on, and the seeded completions come
// back oldest first.
func TestDownloadsHandler(t *testing.T) {
	s := newTestServer(t, false)
	s.config.Token = "testtok"
	router := apiRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("persist off -> %d, want 404 (body %s)", w.Code, w.Body.String())
	}

	s2 := newTestServer(t, true)
	s2.config.Token = "testtok"
	router2 := apiRouter(s2)
	s2.api.mu.Lock()
	s2.api.downloads["b.epub"] = time.Now().Add(time.Second)
	s2.api.downloads["a.epub"] = time.Now()
	s2.api.mu.Unlock()

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
	req2.Header.Set("Authorization", "Bearer "+s2.config.Token)
	w2 := httptest.NewRecorder()
	router2.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("persist on -> %d, want 200 (body %s)", w2.Code, w2.Body.String())
	}
	var out []DownloadedBook
	if err := json.NewDecoder(w2.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w2.Body.String())
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Name != "a.epub" || out[1].Name != "b.epub" {
		t.Errorf("order = [%s %s], want oldest first [a.epub b.epub]", out[0].Name, out[1].Name)
	}
}

// TestMetricsHandler: the v5.1.2 metrics endpoint answers in the
// Prometheus text format and carries the fixed gauges.
func TestMetricsHandler(t *testing.T) {
	s := newTokenServer(t)
	router := apiRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("metrics -> %d, want 200", w.Code)
	}
	body := w.Body.String()
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("content-type %q, want text/plain", ct)
	}
	// Per-server gauges have exact values on this fresh server.
	for _, line := range []string{
		"openbooks_up 1",
		"openbooks_version{version=\"5.1.2\"} 1",
		"openbooks_irc_connected 0",
		"openbooks_downloads_completed 0",
		"openbooks_callback_queue_size 0",
	} {
		if !strings.Contains(body, line) {
			t.Errorf("metrics body missing %q", line)
		}
	}
	// Package-global counters are shared by every test in the package, so
	// assert the format (name + integer value) rather than an absolute
	// zero.
	for _, pat := range []string{
		`openbooks_irc_sessions_total [0-9]+`,
		`openbooks_searches_total [0-9]+`,
		`openbooks_downloads_total [0-9]+`,
		`openbooks_download_errors_total [0-9]+`,
	} {
		if m, _ := regexp.MatchString(pat, body); !m {
			t.Errorf("metrics body missing a line matching %q", pat)
		}
	}
}

// TestAPISessionReap pins the v5.1.2 liveness fix: when the api IRC
// session dies (VPN bounce, server drop) reapSession must flip the
// connected flag so the next search re-establishes the session. Before
// this the flag stayed true over the dead conn: the first search after
// the drop burned the 120s wait and every later search hit the dead conn.
func TestAPISessionReap(t *testing.T) {
	s := newTokenServer(t)

	// Seed a live session. reapSession only touches the flag and the log
	// - the client is never dereferenced - so a bare client is enough.
	s.api.mu.Lock()
	s.api.client = &Client{uuid: apiClientID}
	s.api.connected = true
	s.api.mu.Unlock()

	s.reapSession()

	s.api.mu.Lock()
	connected := s.api.connected
	s.api.mu.Unlock()
	if connected {
		t.Error("reapSession left connected=true; the next search would hit the dead conn")
	}

	// A second reap is a no-op: reapSession only writes the flag under
	// the mutex, so it cannot panic on the channel-close path.
	s.reapSession()
	s.api.mu.Lock()
	connected = s.api.connected
	s.api.mu.Unlock()
	if connected {
		t.Error("double reapSession left connected=true")
	}
}
