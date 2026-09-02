package server

// PotatoStack v5: bearer-token authentication for the whole HTTP surface.
//
// Upstream openbooks has no authentication at all: the "OpenBooks" cookie is
// a client-generated uuid that doubles as an identity, /stats and /servers
// are open to anyone, and the library endpoints only needed the cookie to be
// a parseable UUID. Behind tailscale serve that meant every tailnet peer
// could list and delete the book library and read connected-client IPs.
//
// This file adds a single static token. When OPENBOOKS_TOKEN (or --token) is
// set, EVERY route except the static SPA assets and GET /openapi.json
// requires a matching token, checked against:
//
//	Authorization: Bearer <token>
//	X-OpenBooks-Token: <token>
//	?token=<token>   (query param - the only way a <a download> link or a
//	                 WebSocket can carry credentials in the browser)
//
// The comparison is a constant-time compare so the check does not leak the
// token length through timing. The static SPA stays open by design: it ships
// no data, only the UI shell, and the UI itself probes /api/v1/health and
// asks for a token before showing anything.
//
// When no token is configured (single-user desktop mode, local server) the
// server runs exactly like upstream: the middleware passes everything
// through. That keeps `openbooks server` out-of-the-box usable on a laptop
// while `openbooks:local` behind tailscale serves a token by default.

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireToken is the API guard. It wraps every non-static route.
func (server *server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if server.config.Token == "" {
			next.ServeHTTP(w, r)
			return
		}

		if !server.tokenMatches(r) {
			server.log.Printf("Rejected request to %s from %s: no valid token\n",
				r.URL.Path, r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","message":"missing or invalid token"}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// tokenMatches reports whether the request carries the configured token in
// one of the accepted forms.
func (server *server) tokenMatches(r *http.Request) bool {
	want := server.config.Token

	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(auth[len(prefix):])), []byte(want)) == 1 {
				return true
			}
		}
	}

	if header := r.Header.Get("X-OpenBooks-Token"); header != "" {
		if subtle.ConstantTimeCompare([]byte(header), []byte(want)) == 1 {
			return true
		}
	}

	if query := r.URL.Query().Get("token"); query != "" {
		if subtle.ConstantTimeCompare([]byte(query), []byte(want)) == 1 {
			return true
		}
	}

	// Newznab form: indexer tools (Prowlarr, Readarr) append their API key
	// as ?apikey=*** on the indexer URL. Same token, standard protocol.
	if query := r.URL.Query().Get("apikey"); query != "" {
		if subtle.ConstantTimeCompare([]byte(query), []byte(want)) == 1 {
			return true
		}
	}

	return false
}

// wsTokenMatches is the WebSocket variant: gorilla's CheckOrigin is asked
// per request and the same token forms are accepted. Browsers cannot set
// headers on a raw WebSocket, so header-less clients (REST callers) always
// use the query param, and the browser UI sends token=<t> in the URL.
func (server *server) wsTokenMatches(r *http.Request) bool {
	return server.tokenMatches(r)
}
