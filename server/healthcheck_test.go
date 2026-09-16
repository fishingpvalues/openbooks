package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeHealth covers the container healthcheck contract: exit 0 exactly
// when the endpoint answers 200 with the configured token.
func TestProbeHealth(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		requireToken bool
		token        string
		wantErr      bool
	}{
		{name: "200 without token", status: http.StatusOK},
		{name: "200 with token", status: http.StatusOK, requireToken: true, token: "testtok"},
		{name: "401 without token", status: http.StatusUnauthorized, requireToken: true, wantErr: true},
		{name: "401 with wrong token", status: http.StatusUnauthorized, requireToken: true, token: "nope", wantErr: true},
		{name: "500", status: http.StatusInternalServerError, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.requireToken && r.Header.Get("Authorization") != "Bearer "+c.token {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(c.status)
			}))
			defer srv.Close()

			err := ProbeHealth(srv.URL, c.token, 2*time.Second)
			if c.wantErr && err == nil {
				t.Fatalf("ProbeHealth(%d) = nil, want an error", c.status)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("ProbeHealth(%d) = %v, want nil", c.status, err)
			}
		})
	}
}

func TestProbeHealthTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	if err := ProbeHealth(srv.URL, "", 50*time.Millisecond); err == nil {
		t.Fatal("ProbeHealth against a slow endpoint = nil, want a timeout error")
	}
}

func TestProbeHealthEmptyURL(t *testing.T) {
	if err := ProbeHealth("", "", time.Second); err == nil {
		t.Fatal("ProbeHealth(\"\") = nil, want an error")
	}
}

// TestSearchConnectFailureCarriesReason pins the v5.4.2 fix: when the api IRC
// session cannot be established, the reason must travel back in the response.
// Before it, performSearch returned an empty response, so the caller wrote
// 502 {"error":""} and the real cause (a 433, a dead tunnel, a timeout) existed
// only in the log. The same Note feeds the plain search handler, the unified
// IRC leg and the Newznab/torznab response.
func TestSearchConnectFailureCarriesReason(t *testing.T) {
	s := newTokenServer(t) // config.Server = 127.0.0.1:1, so the connect fails

	resp, status, _ := s.performSearch("gatsby")
	if status != http.StatusBadGateway {
		t.Fatalf("performSearch status = %d, want %d", status, http.StatusBadGateway)
	}
	if resp.Note == "" {
		t.Fatal("performSearch returned an empty note; the 502 body would be empty again")
	}
}

// TestSendSearchNowConnectFailureCarriesReason does the same for the
// fire-and-forget path (wait=false), whose caller writes the note too.
func TestSendSearchNowConnectFailureCarriesReason(t *testing.T) {
	s := newTokenServer(t)

	status, _, note := s.sendSearchNow("gatsby")
	if status != http.StatusBadGateway {
		t.Fatalf("sendSearchNow status = %d, want %d", status, http.StatusBadGateway)
	}
	if note == "" {
		t.Fatal("sendSearchNow returned an empty note; the 502 body would be empty again")
	}
}
