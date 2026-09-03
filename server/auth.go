package server

// PotatoStack v5: bearer-token authentication for the whole HTTP surface.
//
// Upstream openbooks has no authentication at all: the "OpenBooks" cookie is
// a client-generated uuid that doubles as an identity, /stats and /servers
// are open to anyone, and the library endpoints only needed the cookie to be
// a parseable UUID. Behind tailscale serve that meant every tailnet peer
// could list and delete the book library and read connected-client IPs.
//
// This file adds a static token. When OPENBOOKS_TOKEN (or --token) is
// set, EVERY route except the static SPA assets and GET /openapi.json
// requires a matching token, checked against:
//
//	Authorization: Bearer ***
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
//
// PotatoStack v5.3.0: scoped tokens (see scopes.go). The primary token
// keeps its full privilege; additional "token:scope,scope" entries in
// OPENBOOKS_SCOPED_TOKENS authenticate the same four credential forms but
// only for the scopes they carry. A matched token's scope set is stashed in
// the request context; the per-route requireScope wrappers (registered in
// registerRoutes) turn a scope miss into a 403 (right identity, wrong
// privilege) as opposed to the 401 of a missing or invalid token.

import (
	"context"
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

		set, ok := server.matchToken(r)
		if !ok {
			server.log.Printf("Rejected request to %s from %s: no valid token\n",
				r.URL.Path, r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","message":"missing or invalid token"}`))
			return
		}

		// Stash the authenticated token's scope set for the requireScope
		// wrappers. The primary token (and unscoped mode) carry "all".
		ctx := context.WithValue(r.Context(), scopeContextKey{}, set)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// matchToken reports whether the request carries a configured token in one
// of the accepted forms, and returns the matched token's scope set. The
// primary token is tried first and matches everything; the scoped tokens
// are tried second. A candidate that matches neither is rejected.
func (server *server) matchToken(r *http.Request) (scopeSet, bool) {
	candidates := tokenCandidates(r)

	// Primary token: full privilege.
	if server.config.Token != "" {
		for _, c := range candidates {
			if subtle.ConstantTimeCompare([]byte(c), []byte(server.config.Token)) == 1 {
				return scopeSet{all: true}, true
			}
		}
	}

	// Scoped tokens: the credential matched, the privilege depends on the
	// entry. (A candidate equal to the primary token is already handled
	// above; the same string configured twice is rejected at parse time.)
	for _, c := range candidates {
		if set, ok := server.scopedSetFor(c); ok {
			return set, true
		}
	}

	return scopeSet{}, false
}

// scopedSetFor looks up a candidate in the scoped-token table. It returns
// ok=false when the table is empty (the unscoped single-token mode, where
// only the primary token exists) or when no entry matches.
func (server *server) scopedSetFor(candidate string) (scopeSet, bool) {
	if len(server.config.ScopedTokens) == 0 {
		return scopeSet{}, false
	}
	set, ok := server.config.ScopedTokens[candidate]
	return set, ok
}

// tokenCandidates collects the raw credential forms off a request, in
// precedence order: Authorization header, X-OpenBooks-Token header,
// ?token=, then ?apikey= (the Newznab form - indexer tools like Prowlarr
// and Readarr append their API key to the indexer URL).
func tokenCandidates(r *http.Request) []string {
	var out []string

	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			out = append(out, strings.TrimSpace(auth[len(prefix):]))
		}
	}

	if header := r.Header.Get("X-OpenBooks-Token"); header != "" {
		out = append(out, header)
	}

	if query := r.URL.Query().Get("token"); query != "" {
		out = append(out, query)
	}

	// Newznab form: indexer tools (Prowlarr, Readarr) append their API key
	// as ?apikey=*** on the indexer URL. Same protocol shape.
	if query := r.URL.Query().Get("apikey"); query != "" {
		out = append(out, query)
	}

	return out
}

// wsTokenMatches is the WebSocket variant: gorilla's CheckOrigin is asked
// per request and the same token forms are accepted. Browsers cannot set
// headers on a raw WebSocket, so header-less clients (REST callers) always
// use the query param, and the browser UI sends token=<t> in the URL.
func (server *server) wsTokenMatches(r *http.Request) bool {
	_, ok := server.matchToken(r)
	return ok
}

// tokenMatches is the bool convenience the torznab tests probe with
// (header-free, the way indexer tools call): any configured token in any
// accepted form. The scope set is dropped here by design - this is the
// "is this credential known" question, not the "may it do this" one.
func (server *server) tokenMatches(r *http.Request) bool {
	_, ok := server.matchToken(r)
	return ok
}
