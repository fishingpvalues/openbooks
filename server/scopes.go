package server

// PotatoStack v5.3.0: scoped tokens.
//
// v5.0.0 shipped ONE static token that opens every route. That is the right
// shape for a single operator behind tailscale; it is the wrong shape the
// moment a DAG, a Newznab poller and the UI need DIFFERENT privileges - a
// poller token that can also PUT /settings or DELETE /library is a standing
// escalation path (the Newznab apikey in particular is visible to every
// *arr app configured against /torznab).
//
// The model follows the catalogue servers (Komga per-user API keys, Kavita
// per-user auth keys): the primary token (OPENBOOKS_TOKEN / --token) is
// unchanged and still opens everything; ADDITIONAL scoped tokens are
// configured as "token:scope,scope;token2:scope" in
// OPENBOOKS_SCOPED_TOKENS. A request authenticated with a scoped token may
// only call the routes its scopes cover; the same token without the scope
// is a 403 (the identity was right, the privilege was not) - distinct from
// the 401 of a missing/invalid token.
//
// Scope vocabulary (keep it small; the table below is the whole policy):
//
//	ui         health, library list/file (the React app + the Atom/OPDS
//	                      feed links the UI generates)
//	search     search, unified search, wanted watchlist (read and write),
//	                      downloads list, atom feed
//	newznab    /torznab (the *arr indexer surface; the apikey travels in
//	                      indexer configs, so it must be the narrowest)
//	admin      download requests (incl. sidecar), retry/verify, cache
//	                      management, settings read/write, library delete,
//	                      integrations, metrics
//
// "all" covers every scope (a scoped token equivalent to the primary).
//
// Unscoped deployments are untouched: with OPENBOOKS_SCOPED_TOKENS unset
// there is exactly one token and the scope check always passes, so a
// standalone `openbooks server` and the current potatostack deploy behave
// exactly as before.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// The scope set, as a fixed list (documentation + validation order).
var scopeNames = []string{"ui", "search", "newznab", "admin"}

// scopeAll is the catch-all scope name in scoped-token config.
const scopeAll = "all"

type scopeSet struct {
	all bool
	by  map[string]bool
}

func (s scopeSet) has(scope string) bool {
	if s.all {
		return true
	}
	return s.by[scope]
}

// parseScopedTokens parses the OPENBOOKS_SCOPED_TOKENS value into
// token->scopes entries. Grammar: entries separated by ';', each entry
// "token:scope,scope". Tokens containing ':' or ':'-adjacent garbage,
// unknown scope names, and empty tokens are rejected with an error (a
// misconfigured deploy must fail loudly at startup, not silently run
// scope-less).
func parseScopedTokens(raw string) (map[string]scopeSet, error) {
	out := make(map[string]scopeSet)
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.LastIndex(entry, ":")
		if idx <= 0 {
			return nil, fmt.Errorf("scoped-token entry %q must be token:scope[,scope]", entry)
		}
		token := entry[:idx]
		set := scopeSet{by: make(map[string]bool)}
		for _, sc := range strings.Split(entry[idx+1:], ",") {
			sc = strings.TrimSpace(sc)
			if sc == "" {
				continue
			}
			if sc == scopeAll {
				set.all = true
				continue
			}
			found := false
			for _, name := range scopeNames {
				if sc == name {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("unknown scope %q in entry %q", sc, entry)
			}
			set.by[sc] = true
		}
		if !set.all && len(set.by) == 0 {
			return nil, fmt.Errorf("scoped-token entry %q has no scopes", entry)
		}
		if _, ok := out[token]; ok {
			return nil, fmt.Errorf("duplicate scoped token in %q", entry)
		}
		out[token] = set
	}
	return out, nil
}

// scopeContextKey carries the authenticated token's scopes down the
// handler chain. requireToken stashes it; requireScope reads it.
type scopeContextKey struct{}

// scopeOf extracts the scope set from the request context. An empty set
// (no key) happens for unauthenticated requests (already 401'd by
// requireToken) and for the unscoped single-token mode, where the primary
// token matches everything and the set is the full "all".
func scopeOf(ctx context.Context) scopeSet {
	v, _ := ctx.Value(scopeContextKey{}).(scopeSet)
	return v
}

// requireScope wraps a handler with a per-route scope check. It must run
// AFTER requireToken (the token has been matched and the scope set
// stashed); on a scoped token that lacks the scope it is a 403 with the
// missing scope named, so a mis-issued token is debuggable at the caller.
func (server *server) requireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !scopeOf(r.Context()).has(scope) {
			recordAPIStatus(http.StatusForbidden)
			server.log.Printf("Rejected %s %s: token lacks scope %q\n",
				r.Method, r.URL.Path, scope)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden","message":"token lacks the required scope"}`))
			return
		}
		next(w, r)
	}
}

// requireScopeMW is the middleware form of requireScope for chi's
// With/Use positions (which take func(http.Handler) http.Handler, not a
// handler wrapper). Same 403 contract; same context read.
func (server *server) requireScopeMW(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !scopeOf(r.Context()).has(scope) {
				recordAPIStatus(http.StatusForbidden)
				server.log.Printf("Rejected %s %s: token lacks scope %q\n",
					r.Method, r.URL.Path, scope)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"forbidden","message":"token lacks the required scope"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
