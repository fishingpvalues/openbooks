package server

// PotatoStack v5: a plain REST API on top of the websocket app.
//
// The upstream UI is websocket-only for searching and downloading: a peer
// must hold the single IRC connection to trigger anything. That made
// openbooks useless to the rest of the stack (DAGs, other services, curl).
// These endpoints expose the same operations as REST. Every endpoint sits
// behind requireToken (see auth.go).
//
// Search and download run in a server-owned IRC session ("api client") that
// lives in the same clients map as websocket clients, under a reserved
// uuid. serveWs's single-client rule therefore counts only websocket
// clients, so the UI and the REST API can share the server. Concurrent REST
// calls share one api session: a second search while the first is in flight
// gets a 429 from the rate limit, same as the UI.

import (
	"bytes"
	"context"
	_ "embed" // directive-only use: //go:embed openapi.json, no embed.X referenced
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evan-buss/openbooks/core"
	"github.com/evan-buss/openbooks/irc"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Reserved uuid the server-owned IRC session uses in the clients map.
var apiClientID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// openapiSpec is served at GET <basepath>openapi.json so any HTTP client can
// discover the API without external docs.
//
//go:embed openapi.json
var openapiSpec []byte

// BookFile is one entry of GET /api/v1/library.
type BookFile struct {
	Name         string    `json:"name"`
	DownloadLink string    `json:"downloadLink"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modifiedAt"`
}

// HealthResponse is the response of GET /api/v1/health.
type HealthResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Persist bool   `json:"persist"`

	// IRCConnected reports whether the shared api IRC session is up. It
	// is the signal that distinguishes "process alive" (the HTTP health
	// itself) from "can actually search" - the process comes up before
	// any IRC session exists, so a probe that only checked the HTTP
	// status would call the server healthy while every search fails.
	// The session re-establishes itself lazily on the next search
	// (reapSession), so a false here is self-healing by design.
	IRCConnected bool `json:"ircConnected"`
}

// APISearchRequest is the body of POST /api/v1/search.
type APISearchRequest struct {
	// Query is the search term sent to the IRC search bot.
	Query string `json:"query"`

	// Wait blocks until results arrive (or the 120s timeout). Default true -
	// callers like DAGs want results, not a "sent" ack. false is fire and
	// forget.
	Wait *bool `json:"wait,omitempty"`
}

// APISearchResponse is the response of POST /api/v1/search (wait=true).
type APISearchResponse struct {
	Books  []core.BookDetail `json:"books"`
	Errors []core.ParseError `json:"errors,omitempty"`
	Waited bool              `json:"waited"`
	Note   string            `json:"note,omitempty"`
}

// APIDownloadRequest is the body of POST /api/v1/download.
type APIDownloadRequest struct {
	// Book is the "!"-prefixed identifier from the search results.
	Book string `json:"book"`

	// CallbackURL, when set, is called (POST, JSON body) once the book has
	// landed in the library. This is the *arr-style completion hook:
	// Readarr/Prowlarr register a callback and learn of completion without
	// polling. It is independent of the static OPENBOOKS_DOWNLOAD_CALLBACK
	// webhook (that one fires for every download; this one is per-request
	// and only when the caller passes it).
	CallbackURL string `json:"callbackUrl,omitempty"`
}

// searchOutcome is what the api event handlers deliver for one search.
type searchOutcome struct {
	Books  []core.BookDetail
	Errs   []core.ParseError
	Failed string // non-empty when the IRC side answered an error
}

// apiState is the shared server-owned IRC session plus the result plumbing
// for waiting search callers.
type apiState struct {
	mu sync.Mutex

	client    *Client
	connected bool

	// cancel tears the api session's context down. reapSession calls it so
	// the old session's apiResultPump exits (its ctx is not the process
	// ctx - a reaped session must not pin a goroutine). nil when no
	// session is up.
	cancel func()

	lastSearch time.Time

	// At most one search may be in flight; nil when none is.
	pendingSearch chan searchOutcome

	// Recent book downloads: file name -> completion time. Lets a caller
	// see whether a requested book has landed without diffing the library.
	downloads map[string]time.Time

	// downloadCallbacks is the FIFO of completion webhooks queued by
	// POST /api/v1/download. The api IRC session is single-flight and
	// processes book DCC transfers in request order, so completions
	// (recordAPIDownload) drain the queue in order. At most one pending
	// download per caller by design of the single api client.
	downloadCallbacks []downloadCallback
}

// downloadCallback is one queued download-completion webhook.
type downloadCallback struct {
	// Book is the "!"-prefixed identifier the caller requested.
	Book string
	// URL is where the completion POST goes.
	URL string
}

func newAPIState() *apiState {
	return &apiState{downloads: make(map[string]time.Time)}
}

// reapSession marks the api IRC session dead (the deferred unregister sends
// block until process exit - the documented ws-path behavior) and lets the
// next performSearch / startAPIClient bring up a fresh connection. This is
// what keeps the REST API alive across a gluetun netns flap or an
// irchighway server drop: without it the first search after the drop
// silently times out (120s) and every later search hits the dead conn, so
// openbooks reads as "up" to every healthcheck while every real search
// fails - the exact failure shape of the v4.5.0 join race, now in steady
// state.
func (server *server) reapSession() {
	state := server.api
	state.mu.Lock()
	state.connected = false
	cancel := state.cancel
	state.cancel = nil
	state.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	server.log.Printf("api client: IRC session lost; will reconnect on the next search\n")
}

// libraryBase is the books dir every library endpoint acts on.
func (server *server) libraryBase() string {
	return filepath.Join(server.settings.GetDownloadDir(), "books")
}

// fileMeta returns size and modification time of a file, zero values if it
// vanished between listing and stat.
func fileMeta(path string) (int64, time.Time) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}
	}
	return info.Size(), info.ModTime()
}

// urlUnescapePath percent-decodes a path fragment from a request.
func urlUnescapePath(raw string) (string, error) {
	return url.PathUnescape(raw)
}

// safeJoin joins base and name and guarantees the result stays inside base.
func safeJoin(base, name string) (string, bool) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, 0) {
		return "", false
	}
	target := filepath.Join(base, name)
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	// Every v5 error response passes through here, which is where the
	// metrics status counter belongs (one call site, every path). The
	// handlers that set a status WITHOUT this helper (the 400 book-name
	// checks, the 500 delete) count themselves.
	recordAPIStatus(status)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]string{"error": message})
	_, _ = w.Write(b)
}

// startAPIClient brings up the shared IRC session if it is not up yet.
// Registration goes through the hub's register channel, so the clients map
// is only ever written by the hub goroutine.
func (server *server) startAPIClient() error {
	state := server.api
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.connected {
		return nil
	}

	// A cancellable session ctx: reapSession cancels it when the reader
	// reports a dead connection, so apiResultPump (which blocks on the
	// send channel) exits instead of leaking.
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{
		uuid: apiClientID,
		send: make(chan interface{}, 128),
		irc:  irc.New(server.config.UserName, server.config.UserAgent),
		log:  server.log,
		ctx:  ctx,
	}

	if err := core.Join(client.irc, server.config.Server, server.config.EnableTLS); err != nil {
		client.irc = nil
		cancel()
		return fmt.Errorf("api IRC connect: %w", err)
	}

	// The reader's death hook (reapSession) flips state.connected, so the
	// next startAPIClient re-establishes the session; see reapSession.
	handler := server.NewIrcEventHandler(client)
	go core.StartReader(context.Background(), client.irc, handler, server.reapSession)
	go server.apiResultPump(client)

	state.client = client
	state.connected = true
	state.cancel = cancel
	recordAPIIRCSession()
	server.register <- client
	return nil
}

// apiResultPump drains the api client's send channel (the ws writePump's job
// is done by this instead - there is no websocket to write to) and routes
// messages into apiState.
func (server *server) apiResultPump(client *Client) {
	defer func() {
		client.irc.Disconnect()
		// Best effort: on server shutdown the hub is gone and this send
		// blocks until process exit (graceful shutdown ends with os.Exit).
		server.unregister <- client
	}()
	for {
		select {
		case <-client.ctx.Done():
			return
		case msg, ok := <-client.send:
			if !ok {
				return
			}
			server.routeAPIMessage(msg)
		}
	}
}

// routeAPIMessage folds the ws-shaped messages the IRC event handlers
// produce into REST state.
func (server *server) routeAPIMessage(msg interface{}) {
	switch m := msg.(type) {
	case SearchResponse:
		server.deliverAPISearch(searchOutcome{Books: m.Books, Errs: m.Errors})
	case DownloadResponse:
		server.recordAPIDownload(m.Name)
	case ConnectionResponse:
		server.log.Printf("api client: %s\n", m.Detail)
	case StatusResponse:
		// DANGER statuses are the error paths of the search handlers
		// (no results, bad server, download/parse failures). A pending
		// search gets them; download errors are logged.
		if m.NotificationType == DANGER {
			server.log.Printf("api client error: %s\n", m.Title)
			server.deliverAPISearch(searchOutcome{Failed: m.Title})
		} else {
			server.log.Printf("api client: %s\n", m.Title)
		}
	default:
		server.log.Printf("api client: unhandled message type %T\n", msg)
	}
}

// deliverAPISearch hands a finished search to the single waiting caller.
// If nobody is waiting (timeout already returned, or a stray DANGER after a
// failed search) it is dropped.
func (server *server) deliverAPISearch(outcome searchOutcome) {
	state := server.api
	state.mu.Lock()
	ch := state.pendingSearch
	state.pendingSearch = nil
	state.mu.Unlock()
	if ch != nil {
		ch <- outcome
	}
}

func (server *server) recordAPIDownload(name string) {
	state := server.api
	state.mu.Lock()
	state.downloads[name] = time.Now()
	// Drain the FIFO: this completion belongs to the oldest queued
	// callback (book DCC transfers are handled in request order on the
	// single api session).
	cb := downloadCallback{}
	if len(state.downloadCallbacks) > 0 {
		cb = state.downloadCallbacks[0]
		state.downloadCallbacks = state.downloadCallbacks[1:]
	}
	state.mu.Unlock()

	server.log.Printf("api client: book download completed: %s\n", name)

	if cb.URL == "" {
		return
	}
	go server.fireDownloadCallback(cb, name)
}

// fireDownloadCallback POSTs the download-completion webhook. Best effort:
// failures are logged (the caller can still poll GET /api/v1/library), with
// one retry. Not retried forever - a dead webhook must not pin the queue.
func (server *server) fireDownloadCallback(cb downloadCallback, fileName string) {
	body, _ := json.Marshal(map[string]string{
		"status": "completed",
		"book":   cb.Book,
		"file":   fileName,
	})
	// Re-validate rather than trusting the queued value: the guard is cheap and
	// the entry has been sitting in a FIFO since the request.
	target, err := validateCallbackURL(cb.URL)
	if err != nil {
		server.log.Printf("download callback: refusing %s: %v\n",
			redactCallbackURL(cb.URL), err)
		return
	}
	client := newCallbackClient(target)

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cb.URL, bytes.NewReader(body))
		if err != nil {
			cancel()
			server.log.Printf("download callback: invalid URL %s: %v\n", redactCallbackURL(cb.URL), err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(httpReq)
		if err != nil {
			cancel()
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		resp.Body.Close()
		cancel()
		if resp.StatusCode < 300 {
			server.log.Printf("download callback: delivered %s -> %s\n", fileName, redactCallbackURL(cb.URL))
			return
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
	}
	server.log.Printf("download callback: giving up on %s for %s: %v\n", fileName, redactCallbackURL(cb.URL), lastErr)
}

// healthHandler is GET /api/v1/health - the token probe the UI uses.
func (server *server) healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := server.api
		state.mu.Lock()
		ircConnected := state.connected
		state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{
			Name:         "openbooks",
			Version:      server.config.Version,
			Persist:      server.settings.GetPersist(),
			IRCConnected: ircConnected,
		})
	}
}

// openapiHandler serves the OpenAPI document for this server.
func (server *server) openapiHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openapiSpec)
	}
}

// libraryListHandler is GET /api/v1/library - the same data as the legacy
// /library endpoint, plus size and mtime, and a real error code when
// persist mode is off.
func (server *server) libraryListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !server.settings.GetPersist() {
			writeJSONError(w, http.StatusNotFound, "library is only available with --persist")
			return
		}

		base := server.libraryBase()
		books, err := os.ReadDir(base)
		if err != nil {
			server.log.Printf("Unable to list books. %s\n", err)
			writeJSONError(w, http.StatusNotFound, "library unavailable")
			return
		}

		output := make([]BookFile, 0)
		for _, book := range books {
			if book.IsDir() || strings.HasPrefix(book.Name(), ".") || filepath.Ext(book.Name()) == ".temp" {
				continue
			}

			size, mod := fileMeta(filepath.Join(base, book.Name()))
			output = append(output, BookFile{
				Name:         book.Name(),
				DownloadLink: "/api/v1/library/" + book.Name(),
				Size:         size,
				ModifiedAt:   mod,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(output)
	}
}

// libraryFileHandler is GET /api/v1/library/{name...} - download a file.
// The name may contain subfolders (the bookdl + filebot pipeline uses them),
// so the route is a wildcard and every component is validated instead of a
// single chi param.
func (server *server) libraryFileHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !server.settings.GetPersist() {
			writeJSONError(w, http.StatusNotFound, "library is only available with --persist")
			return
		}

		raw := strings.TrimPrefix(r.URL.Path, server.config.Basepath+"api/v1/library/")
		name, err := urlUnescapePath(raw)
		if err != nil || name == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid file name")
			return
		}

		base := server.libraryBase()
		target, ok := safeJoin(base, name)
		if !ok {
			server.log.Printf("Rejected library path outside library: %q\n", name)
			writeJSONError(w, http.StatusBadRequest, "invalid file name")
			return
		}

		http.ServeFile(w, r, target)
	}
}

// libraryDeleteHandler is DELETE /api/v1/library/{name} - one top-level
// entry, same hardening as the legacy handler (see routes.go).
func (server *server) libraryDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fileName, err := urlUnescapePath(chi.URLParam(r, "name"))
		if err != nil {
			server.log.Printf("Error unescaping path: %s\n", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Same name policy as the legacy delete handler (validBookName):
		// one segment, no separators, no traversal, no dotfiles - a dotfile
		// is not a book the listing ever exposes.
		if !validBookName(fileName) {
			server.log.Printf("Rejected book file name: %q\n", fileName)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		base := server.libraryBase()
		target := filepath.Join(base, fileName)
		rel, err := filepath.Rel(base, target)
		if err != nil || rel != fileName {
			server.log.Printf("Rejected book path outside library: %q\n", fileName)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if err := os.Remove(target); err != nil {
			server.log.Printf("Error deleting book file: %s\n", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// DownloadedBook is one entry of GET /api/v1/downloads.
type DownloadedBook struct {
	Name       string    `json:"name"`
	CompletedAt time.Time `json:"completedAt"`
}

// downloadsHandler is GET /api/v1/downloads - the book completions the api
// session has recorded, oldest last (the completion map is keyed by file
// name, so there is no request ordering; the timestamps give the caller
// the order). It exists because POST /api/v1/download returns the moment
// the DCC request is *sent* and the file then takes minutes to arrive:
// callers without a callbackUrl had to diff the library listing to learn
// the file landed. 404 with a JSON error when persist mode is off, same
// contract as GET /api/v1/library.
func (server *server) downloadsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !server.settings.GetPersist() {
			writeJSONError(w, http.StatusNotFound, "downloads are only available with --persist")
			return
		}

		state := server.api
		state.mu.Lock()
		out := make([]DownloadedBook, 0, len(state.downloads))
		for name, ts := range state.downloads {
			out = append(out, DownloadedBook{Name: name, CompletedAt: ts})
		}
		state.mu.Unlock()

		sort.Slice(out, func(i, j int) bool { return out[i].CompletedAt.Before(out[j].CompletedAt) })
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// performSearch runs one IRC book search through the shared api session and
// blocks (up to 120s) for the parsed result. It is the shared core of
// POST /api/v1/search (searchHandler) and the inbound Newznab search
// endpoint (torznab.go) so both enforce the same rate limit and single
// in-flight-search rule. It does NOT write the HTTP response; callers own
// that. On rate-limit it returns StatusTooManyRequests (with the seconds
// remaining, for the Retry-After header); on a concurrent search,
// StatusConflict; on IRC connect failure, StatusBadGateway.
func (server *server) performSearch(query string) (APISearchResponse, int, int) {
	state := server.api
	if err := server.startAPIClient(); err != nil {
		server.log.Printf("%s\n", err)
		return APISearchResponse{}, http.StatusBadGateway, 0
	}

	state.mu.Lock()
	nextAvailable := state.lastSearch.Add(server.config.SearchTimeout)
	if time.Now().Before(nextAvailable) {
		remaining := int(time.Until(nextAvailable).Seconds() + 0.5)
		state.mu.Unlock()
		return APISearchResponse{Note: fmt.Sprintf("rate limited, retry after %ds", remaining)}, http.StatusTooManyRequests, remaining
	}
	state.lastSearch = time.Now()

	if state.pendingSearch != nil {
		state.mu.Unlock()
		return APISearchResponse{Note: "search already in flight, retry later"}, http.StatusConflict, 0
	}
	outcomeCh := make(chan searchOutcome, 1)
	state.pendingSearch = outcomeCh
	state.mu.Unlock()

	// Arm the waiter BEFORE sending, so a fast result cannot be missed.
	core.SearchBook(state.client.irc, server.config.SearchBot, query)
	server.log.Printf("api search sent: %q\n", query)

	var outcome searchOutcome
	timedOut := false
	select {
	case outcome = <-outcomeCh:
	case <-time.After(120 * time.Second):
		timedOut = true
		state.mu.Lock()
		if state.pendingSearch == outcomeCh {
			state.pendingSearch = nil
		}
		state.mu.Unlock()
	}

	resp := APISearchResponse{Waited: true}
	if timedOut {
		resp.Note = "timed out waiting for search results"
	} else if outcome.Failed != "" {
		resp.Note = outcome.Failed
	} else {
		resp.Books = outcome.Books
		resp.Errors = outcome.Errs
		if len(outcome.Books) == 0 {
			resp.Note = "no results"
		}
	}
	return resp, http.StatusOK, 0
}

// sendSearchNow fires a search at the IRC bot and returns immediately
// (fire-and-forget). It enforces the same rate limit as performSearch but
// does NOT arm a result waiter, so it never blocks on the result. This is
// the wait=false path of POST /api/v1/search. It returns the HTTP status
// and, for a 429, the seconds to wait (Retry-After).
func (server *server) sendSearchNow(query string) (int, int) {
	state := server.api
	if err := server.startAPIClient(); err != nil {
		server.log.Printf("%s\n", err)
		return http.StatusBadGateway, 0
	}

	state.mu.Lock()
	nextAvailable := state.lastSearch.Add(server.config.SearchTimeout)
	if time.Now().Before(nextAvailable) {
		remaining := int(time.Until(nextAvailable).Seconds() + 0.5)
		state.mu.Unlock()
		return http.StatusTooManyRequests, remaining
	}
	state.lastSearch = time.Now()
	if state.pendingSearch != nil {
		state.mu.Unlock()
		return http.StatusConflict, 0
	}
	state.mu.Unlock()

	// No waiter is armed (pendingSearch left nil), so the outcome is
	// dropped if nobody is waiting - that is the fire-and-forget contract.
	core.SearchBook(state.client.irc, server.config.SearchBot, query)
	server.log.Printf("api search sent (async): %s\n", query)
	return http.StatusOK, 0
}

// searchHandler is POST /api/v1/search - send a query to the IRC search bot
// and (by default) wait for the parsed results. wait=false is fire-and-forget.
func (server *server) searchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req APISearchRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			recordAPIStatus(http.StatusBadRequest)
			writeJSONError(w, http.StatusBadRequest, "query is required")
			return
		}

		// One accepted search attempt (both wait paths); the 429/409/502
		// below is also counted by recordAPIStatus in each branch.
		recordAPISearch()

		wait := true
		if req.Wait != nil {
			wait = *req.Wait
		}

		if !wait {
			status, retryAfter := server.sendSearchNow(query)
			if status != http.StatusOK {
				if retryAfter > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				}
				recordAPIStatus(status)
				writeJSONError(w, status, "search not sent")
				return
			}
			recordAPIStatus(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(APISearchResponse{Waited: false, Note: "search sent"})
			return
		}

		resp, status, retryAfter := server.performSearch(query)
		if status == http.StatusConflict || status == http.StatusTooManyRequests ||
			status == http.StatusBadGateway {
			if retryAfter > 0 {
				// HTTP-standard backoff hint; Prowlarr/Readarr honor it when
				// polling the Newznab endpoint.
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			}
			recordAPIStatus(status)
			writeJSONError(w, status, resp.Note)
			return
		}
		recordAPIStatus(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// downloadHandler is POST /api/v1/download - ask the IRC book server for a
// book. The identifier is the "!"-prefixed line shown in search results.
// The file arrives over DCC into the library dir; poll GET /api/v1/library
// until it appears (a DCC transfer can take a while).
func (server *server) downloadHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req APIDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			recordAPIStatus(http.StatusBadRequest)
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if !strings.HasPrefix(strings.TrimSpace(req.Book), "!") {
			recordAPIDownloadError()
			recordAPIStatus(http.StatusBadRequest)
			writeJSONError(w, http.StatusBadRequest,
				"book must be the !-prefixed identifier from search results")
			return
		}

		// Validate the completion webhook before opening the IRC session:
		// a bad URL must be a 400 even when the IRC server is unreachable,
		// and there is no reason to establish a connection for a request
		// that is about to be rejected. validateCallbackURL (callback_guard.go)
		// is the full SSRF policy; the dial-time check in newCallbackClient
		// is the one that actually holds, because a hostname's answer can
		// change between here and the POST.
		var callbackURL string
		if cb := strings.TrimSpace(req.CallbackURL); cb != "" {
			u, err := validateCallbackURL(cb)
			if err != nil {
				recordAPIDownloadError()
				recordAPIStatus(http.StatusBadRequest)
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			callbackURL = u.String()
		}

		recordAPIDownload()
		if err := server.startAPIClient(); err != nil {
			server.log.Printf("%s\n", err)
			recordAPIStatus(http.StatusBadGateway)
			writeJSONError(w, http.StatusBadGateway, "unable to connect to IRC server")
			return
		}

		state := server.api
		recordAPIIRCSession()
		core.DownloadBook(state.client.irc, req.Book)
		server.log.Printf("api download requested: %s\n", req.Book)

		// Queue the completion webhook (FIFO): the api IRC session handles
		// book DCC transfers in request order, so recordAPIDownload
		// delivers this to the oldest queued callback when the book lands.
		// callbackURL is already validated above (pre-IRC).
		if callbackURL != "" {
			state.mu.Lock()
			state.downloadCallbacks = append(state.downloadCallbacks,
				downloadCallback{Book: req.Book, URL: callbackURL})
			state.mu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json")
		resp := map[string]string{
			"status": "requested",
			"detail": "book requested over DCC; poll GET /api/v1/library until the file appears",
		}
		if req.CallbackURL != "" {
			resp["callback"] = "a completion POST will be sent to callbackUrl when the book lands"
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// registerRoutes wires the whole HTTP tree.
//
// The static SPA and the OpenAPI document are open: the SPA ships no data,
// only the UI shell, and openapi.json is documentation. Everything else -
// the legacy browser endpoints AND the /api/v1 REST API - is behind
// requireToken.
func (server *server) registerRoutes() *chi.Mux {
	router := chi.NewRouter()

	// Open: API documentation + the static SPA (chi's catch-all route is
	// only tried after the specific routes above it have not matched).
	router.Get("/openapi.json", server.openapiHandler())
	router.Handle("/*", server.staticFilesHandler("app/dist"))

	// Legacy browser endpoints (the React app still uses these).
	router.With(server.requireToken).Get("/ws", server.serveWs())
	router.With(server.requireToken).Get("/stats", server.statsHandler())
	router.With(server.requireToken).Get("/servers", server.serverListHandler())
	router.Group(func(r chi.Router) {
		r.Use(server.requireToken)
		r.Get("/library", server.getAllBooksHandler())
		r.Delete("/library/{fileName}", server.deleteBooksHandler())
		r.Get("/library/*", server.getBookHandler())
	})

	// Inbound Newznab: openbooks as a book indexer for the *arr stack.
	// Prowlarr/Readarr point a book indexer here and search through the
	// standard Newznab protocol (auth via ?apikey=*** like every other
	// route). It is token-gated and sits next to the SPA, not under
	// /api/v1, because indexer tools poll it exactly like they poll a
	// tracker's Newznab URL.
	router.With(server.requireToken).Get("/torznab", server.torznabHandler())

	// REST API for the rest of the stack.
	router.With(server.requireToken).Route("/api/v1", func(r chi.Router) {
		r.Get("/health", server.healthHandler())
		r.Get("/openapi.json", server.openapiHandler())
		r.Get("/library", server.libraryListHandler())
		r.Get("/library/*", server.libraryFileHandler())
		r.Delete("/library/{name}", server.libraryDeleteHandler())
		r.Post("/search", server.searchHandler())
		r.Post("/download", server.downloadHandler())
		// Book completions recorded by the api session (the polling path
		// for callers without a callbackUrl).
		r.Get("/downloads", server.downloadsHandler())
		// Prometheus-format metrics for the stack's monitoring.
		r.Get("/metrics", server.metricsHandler())
		// Runtime-mutable settings (port of the fork's a65ef3d settings
		// work). Changing the download dir at runtime rewrites where
		// downloads land without a restart.
		r.Get("/settings", server.settingsHandler())
		r.Put("/settings", server.settingsHandler())
		// Outbound peer integrations: which services are configured and
		// (with ?probe=1) reachable.
		r.Get("/integrations", server.integrationsOverviewHandler())
	})

	return router
}
