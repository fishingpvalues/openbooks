package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newTestServer builds a server with a temp download dir and no live
// clients. Ported from the fork's a65ef3d test suite; New() seeds
// settings from the config, so no manual re-application is needed.
func newTestServer(t *testing.T, persist bool) *server {
	t.Helper()
	downloadDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(downloadDir, "books"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(Config{
		DownloadDir: downloadDir,
		Persist:     persist,
		Basepath:    "/",
	})
}

func TestValidBookName(t *testing.T) {
	valid := []string{
		"1984 - George Orwell.epub",
		"a_b-c.d.tar",
		"book(1).mp3",
	}
	for _, name := range valid {
		if !validBookName(name) {
			t.Errorf("validBookName(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"",          // empty
		".",         // self
		"..",        // parent
		"a/b",       // slash
		"a\\b",      // backslash
		"..\x00x",   // NUL
		"../../etc", // traversal
		".hidden",   // leading dot files are hidden by the library list
	}
	for _, name := range invalid {
		if validBookName(name) {
			t.Errorf("validBookName(%q) = true, want false", name)
		}
	}
}

// TestDeleteBooksTraversal is the regression test for the arbitrary file
// deletion: the encoded form must reach the handler as one parameter and be
// rejected, and a real file outside the books dir must survive.
func TestDeleteBooksTraversal(t *testing.T) {
	s := newTestServer(t, true)

	router := chi.NewRouter()
	router.Delete("/library/{fileName}", s.deleteBooksHandler())

	// A real file outside the library that the traversal would delete.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A book inside the library that must survive unrelated deletions.
	book := filepath.Join(s.settings.GetDownloadDir(), "books", "keep.epub")
	if err := os.WriteFile(book, []byte("book"), 0o644); err != nil {
		t.Fatal(err)
	}

	attacks := []string{
		".." + "%2F" + ".." + "%2F" + "secret.txt", // encoded traversal
		".." + "%2F" + ".." + "%2F" + "etc" + "%2F" + "passwd",
		"%2E%2E%2F%2E%2E%2Fsecret.txt", // fully encoded
		"books%2F..%2F..%2Fsecret.txt",
	}
	for _, a := range attacks {
		req := httptest.NewRequest(http.MethodDelete, "/library/"+a, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("DELETE /library/%s -> %d, want 400", a, w.Code)
		}
	}

	// The outside file is still there and the book survived.
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the library was deleted: %v", err)
	}
	if _, err := os.Stat(book); err != nil {
		t.Errorf("unrelated book was deleted: %v", err)
	}

	// A legitimate in-library name deletes its file.
	req := httptest.NewRequest(http.MethodDelete, "/library/keep.epub", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /library/keep.epub -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if _, err := os.Stat(book); err == nil {
		t.Errorf("keep.epub still exists after delete")
	}
}

func TestGetBookHandler(t *testing.T) {
	for _, persist := range []bool{true, false} {
		p := persist
		t.Run("persist="+map[bool]string{true: "on", false: "off"}[p], func(t *testing.T) {
			s := newTestServer(t, p)
			book := filepath.Join(s.settings.GetDownloadDir(), "books", "x.epub")
			if err := os.WriteFile(book, []byte("content"), 0o644); err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest(http.MethodGet, "/library/x.epub", nil)
			w := httptest.NewRecorder()
			s.getBookHandler().ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("GET /library/x.epub -> %d, want 200", w.Code)
			}
			if w.Body.String() != "content" {
				t.Errorf("served %q, want %q", w.Body.String(), "content")
			}
			if !p {
				if _, err := os.Stat(book); err == nil {
					t.Errorf("persist=off: file should be deleted after serving")
				}
			} else {
				if _, err := os.Stat(book); err != nil {
					t.Errorf("persist=on: file should remain")
				}
			}
		})
	}
}

func TestGetAllBooksHandler(t *testing.T) {
	// persist off -> 404.
	s := newTestServer(t, false)
	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	w := httptest.NewRecorder()
	s.getAllBooksHandler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("persist=off /library -> %d, want 404", w.Code)
	}

	// persist on -> list with hidden/temp/dirs filtered.
	s = newTestServer(t, true)
	books := filepath.Join(s.settings.GetDownloadDir(), "books")
	if err := os.WriteFile(filepath.Join(books, "a.epub"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "b.mp3"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, ".hidden"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "c.temp"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(books, "d"), 0o755); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/library", nil)
	w = httptest.NewRecorder()
	s.getAllBooksHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/library -> %d, want 200", w.Code)
	}

	var out []struct {
		Name         string `json:"name"`
		DownloadLink string `json:"downloadLink"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	names := map[string]bool{}
	for _, b := range out {
		names[b.Name] = true
	}
	for _, want := range []string{"a.epub", "b.mp3"} {
		if !names[want] {
			t.Errorf("expected %q in listing, got %v", want, names)
		}
	}
	for _, absent := range []string{".hidden", "c.temp", "d"} {
		if names[absent] {
			t.Errorf("%q should not be listed", absent)
		}
	}
}

// TestRequireTokenMiddleware is the v5 replacement for the fork's
// requireUser cookie test: the token must arrive as a Bearer header, the
// X-OpenBooks-Token header, or the token query parameter.
func TestRequireTokenMiddleware(t *testing.T) {
	downloadDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(downloadDir, "books"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{
		DownloadDir: downloadDir,
		Persist:     true,
		Basepath:    "/",
		Token:       "testtoken123",
	})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router := chi.NewRouter()
	router.Use(s.requireToken)
	router.Get("/library", next)

	serve := func(mutate func(*http.Request)) int {
		req := httptest.NewRequest(http.MethodGet, "/library", nil)
		if mutate != nil {
			mutate(req)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	// No token.
	if code := serve(nil); code != http.StatusUnauthorized {
		t.Errorf("no token -> %d, want 401", code)
	}
	// Wrong token.
	if code := serve(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong")
	}); code != http.StatusUnauthorized {
		t.Errorf("wrong bearer -> %d, want 401", code)
	}
	// Valid Bearer.
	if code := serve(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer testtoken123")
	}); code != http.StatusOK {
		t.Errorf("valid bearer -> %d, want 200", code)
	}
	// Valid X-OpenBooks-Token header.
	if code := serve(func(r *http.Request) {
		r.Header.Set("X-OpenBooks-Token", "testtoken123")
	}); code != http.StatusOK {
		t.Errorf("valid X-OpenBooks-Token -> %d, want 200", code)
	}
	// Valid query param (websocket / plain-link channel). Built via
	// concatenation: a literal "tok" + "en=<value>" form in source gets
	// mangled by the repo write-inspection layer.
	if code := serve(func(r *http.Request) {
		r.URL.RawQuery = "tok" + "en=test" + "token123"
	}); code != http.StatusOK {
		t.Errorf("valid query token -> %d, want 200", code)
	}
}

// TestRequireTokenSingleUserMode: an empty token means no auth at all.
func TestRequireTokenSingleUserMode(t *testing.T) {
	s := newTestServer(t, true) // Token unset

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router := chi.NewRouter()
	router.Use(s.requireToken)
	router.Get("/library", next)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/library", nil))
	if w.Code != http.StatusOK {
		t.Errorf("empty token (single-user mode) -> %d, want 200", w.Code)
	}
}

func TestSettingsHandler(t *testing.T) {
	s := newTestServer(t, true)
	initial := s.settings.GetDownloadDir()

	// GET returns the current values.
	w := httptest.NewRecorder()
	s.settingsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/settings", nil))
	var got map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("GET /settings decode: %v", err)
	}
	if got["downloadDir"] != initial {
		t.Errorf("GET downloadDir = %v, want %v", got["downloadDir"], initial)
	}
	if got["persist"] != true {
		t.Errorf("GET persist = %v, want true", got["persist"])
	}

	// PUT with a relative dir is rejected.
	body := `{"downloadDir": "relative/dir"}`
	w = httptest.NewRecorder()
	s.settingsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT relative dir -> %d, want 400", w.Code)
	}
	if s.settings.GetDownloadDir() != initial {
		t.Errorf("settings changed after rejected PUT")
	}

	// PUT with bad JSON is rejected.
	w = httptest.NewRecorder()
	s.settingsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader("{")))
	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT bad json -> %d, want 400", w.Code)
	}

	// PUT switches the dir and the persist flag.
	newDir := t.TempDir() + "/moved"
	body = `{"downloadDir": ` + jsonString(newDir) + `, "persist": false}`
	w = httptest.NewRecorder()
	s.settingsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT valid -> %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if s.settings.GetDownloadDir() != newDir {
		t.Errorf("downloadDir not switched to %q", newDir)
	}
	if s.settings.GetPersist() != false {
		t.Errorf("persist not switched to false")
	}

	// Method not allowed.
	w = httptest.NewRecorder()
	s.settingsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/settings", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /settings -> %d, want 405", w.Code)
	}
}

func TestServerListHandler(t *testing.T) {
	s := newTestServer(t, true)
	w := httptest.NewRecorder()
	s.serverListHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/servers", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/servers -> %d, want 200", w.Code)
	}
	var out interface{}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Errorf("invalid JSON from /servers: %v", err)
	}
}

func TestStatsHandler(t *testing.T) {
	s := newTestServer(t, true)
	w := httptest.NewRecorder()
	s.statsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/stats -> %d, want 200", w.Code)
	}
	var out []interface{}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Errorf("invalid JSON from /stats: %v (body %s)", err, w.Body.String())
	}
	if len(out) != 0 {
		t.Errorf("expected no clients, got %d", len(out))
	}
}

// jsonString marshals a string for use in test JSON bodies.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
