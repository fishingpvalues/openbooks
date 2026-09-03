package server

// v5.3.0 verify regression: pins the live-verified verify contract.
// The library is a tree (/books/books/<file>), so a bare top-level
// fileName must be accepted; recompute=true records a baseline job; a
// second verify without recompute compares against that baseline;
// traversal names stay a 400; and retry on a baseline job is refused
// (a verify baseline has no !-prefixed IRC identifier to re-request).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestVerifyHandlerBaseline(t *testing.T) {
	s := newTestServer(t, true)
	s.config.Token = "testtok"

	fileName := "1984 - George Orwell.epub"
	if err := os.WriteFile(
		filepath.Join(s.settings.GetDownloadDir(), "books", fileName),
		[]byte("the book"), 0o644); err != nil {
		t.Fatal(err)
	}

	doVerify := func(body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/verify",
			strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.verifyHandler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	if c, b := doVerify(`{"fileName":"nope.txt"}`); c != http.StatusNotFound {
		t.Errorf("verify missing file -> %d (%s), want 404", c, b)
	}
	if c, b := doVerify(`{"fileName":"../escape.txt"}`); c != http.StatusBadRequest {
		t.Errorf("verify traversal -> %d (%s), want 400", c, b)
	}
	c, b := doVerify(`{"fileName":"` + fileName + `"}`)
	if c != http.StatusOK || !strings.Contains(b, `"status":"missing"`) {
		t.Errorf("verify no baseline -> %d (%s), want 200 missing", c, b)
	}
	c, b = doVerify(`{"fileName":"` + fileName + `","recompute":true}`)
	if c != http.StatusOK || !strings.Contains(b, `"status":"ok"`) ||
		!strings.Contains(b, `"sha256":"`) {
		t.Fatalf("verify recompute -> %d (%s), want 200 ok + sha256", c, b)
	}
	c, b = doVerify(`{"fileName":"` + fileName + `"}`)
	if c != http.StatusOK || !strings.Contains(b, `"status":"ok"`) {
		t.Errorf("verify against baseline -> %d (%s), want 200 ok", c, b)
	}

	jreq := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	jreq.Header.Set("Authorization", "Bearer "+s.config.Token)
	jw := httptest.NewRecorder()
	s.jobsHandler().ServeHTTP(jw, jreq)
	if jw.Code != http.StatusOK {
		t.Fatalf("GET /jobs -> %d, want 200", jw.Code)
	}
	var jobs []DownloadJob
	if err := json.NewDecoder(jw.Body).Decode(&jobs); err != nil {
		t.Fatalf("jobs decode: %v", err)
	}
	var baselineID string
	for _, j := range jobs {
		if j.SHA256 != "" && strings.HasPrefix(j.ID, "baseline:") {
			baselineID = j.ID
		}
	}
	if baselineID == "" {
		t.Fatalf("no baseline job in %d jobs", len(jobs))
	}

	r := chi.NewRouter()
	r.Post("/jobs/{id}/retry", s.jobRetryHandler())
	rreq := httptest.NewRequest(http.MethodPost,
		"/jobs/"+url.PathEscape(baselineID)+"/retry", nil)
	rreq.Header.Set("Authorization", "Bearer "+s.config.Token)
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, rreq)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("retry baseline job -> %d (%s), want 400", rw.Code, rw.Body.String())
	}
}
