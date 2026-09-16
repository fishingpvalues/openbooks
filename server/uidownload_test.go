package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// jobsFromHandler reads GET /api/v1/jobs the way a client does.
func jobsFromHandler(t *testing.T, s *server) []DownloadJob {
	t.Helper()
	w := httptest.NewRecorder()
	s.jobsHandler()(w, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("jobs status = %d, want %d", w.Code, http.StatusOK)
	}
	var out []DownloadJob
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("jobs body does not decode: %v (%s)", err, w.Body.String())
	}
	return out
}

// TestUIDownloadAppearsInJobs pins the v5.4.3 fix. A Download clicked in the
// web UI runs over the websocket session, which never went through the api
// job log: the Jobs view said "No download jobs yet." right after a successful
// download and the library was the only trace. It is recorded now, with
// source=ui so the two request paths stay distinguishable.
func TestUIDownloadAppearsInJobs(t *testing.T) {
	s := newTestServer(t, true)

	s.recordUIDownload("!Bsk Riddles of the Hobbit - Adam Roberts.epub")

	jobs := jobsFromHandler(t, s)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d entries, want 1", len(jobs))
	}
	if jobs[0].Source != "ui" {
		t.Errorf("source = %q, want \"ui\"", jobs[0].Source)
	}
	if jobs[0].Status != "requested" {
		t.Errorf("status = %q, want \"requested\"", jobs[0].Status)
	}

	// The DCC transfer lands: the completion closes the row.
	s.completeUIDownload("Riddles of the Hobbit - Adam Roberts.epub")

	jobs = jobsFromHandler(t, s)
	if jobs[0].Status != "completed" {
		t.Errorf("status after completion = %q, want \"completed\"", jobs[0].Status)
	}
	if jobs[0].CompletedAt == "" || jobs[0].FileName == "" {
		t.Errorf("completed row is missing completedAt/fileName: %+v", jobs[0])
	}
}

// TestUICompletionLeavesAPIJobsAlone pins the deliberate split: the UI
// completion must not drain the api session's completion-callback FIFO, so it
// only ever touches its own source=ui rows.
func TestUICompletionLeavesAPIJobsAlone(t *testing.T) {
	s := newTestServer(t, true)

	state := s.api
	state.mu.Lock()
	apiJob := state.addJobLocked("!Bsk Api Book.epub", "api", false)
	state.mu.Unlock()

	s.completeUIDownload("Api Book.epub")

	state.mu.Lock()
	status := apiJob.Status
	state.mu.Unlock()
	if status != "requested" {
		t.Errorf("api job status = %q, want it untouched (\"requested\")", status)
	}
	if n := len(state.downloadCallbacks); n != 0 {
		t.Errorf("callback FIFO length = %d, want 0 (a UI completion must not drain it)", n)
	}
}
