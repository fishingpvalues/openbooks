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
//
// PotatoStack v5.3.0: the search-result cache (in front of performSearch),
// the quality filters (on /search and /search/unified and wanted entries),
// per-job download tracking (GET /api/v1/jobs, POST .../jobs/{id}/retry,
// POST /api/v1/verify) and the download sidecar flag (echoed; the actual
// sidecar fetch is wired to the wanted lifecycle - see wanted.go).

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed" // directive-only use: //go:embed openapi.json, no embed.X referenced
	"encoding/hex"
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

	// Filters are the v5.3.0 quality controls (formats, language,
	// maxSizeBytes, prefer). Optional: empty = v5.2 behavior.
	Filters QualityFilters `json:"filters,omitempty"`
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

	// WithSidecar asks for an ebook sidecar alongside an audiobook (the
	// ReadMeABook pattern, adoption item 10). The flag is accepted and
	// echoed in the response; the fetch of the matching ebook is wired to
	// the wanted lifecycle (a wanted entry with withSidecar re-searches
	// for the ebook class once the audiobook lands). A plain download is
	// single-shot - there is no request state to hang a second fetch on.
	WithSidecar bool `json:"withSidecar,omitempty"`
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

	// v5.3.0: per-job download tracking (adoption item 3). The v5.1.2
	// completion log (name -> time) is kept for the legacy
	// GET /api/v1/downloads contract and the Atom feed; the jobs map is
	// the richer view: one entry per POST /api/v1/download request, with
	// the requested book, its state, and the landed file's sha256
	// (adoption item 7 - the verification baseline).
	downloads map[string]time.Time
	jobs      map[string]*DownloadJob

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

// ── v5.3.0: per-job download tracking (adoption item 3) ──────────────────
//
// bookdl-web exposes exactly this shape (per-job progress, pause/resume,
// retry-failed, verify --fix); ReadMeABook tracks request state the same
// way. The api IRC session is single-flight, so there is at most one
// in-flight DCC transfer - the job state is the durable view over it:
// requested -> completed (with the landed file name and its sha256) or
// failed (the IRC side answered an error for the download). A completion
// that matches no pending job (the operator deleted the job, or the
// session was reaped between request and landing) still records the
// completion log entry and the file's hash.

// DownloadJob is one POST /api/v1/download request over its lifetime.
type DownloadJob struct {
	ID     string `json:"id"`
	Book   string `json:"book"` // the "!"-identifier that was requested
	Status string `json:"status"`

	// RequestedAt / CompletedAt in RFC3339 (CompletedAt empty while
	// pending).
	RequestedAt  string `json:"requestedAt"`
	CompletedAt  string `json:"completedAt,omitempty"`
	Retries      int    `json:"retries"`
	FileName     string `json:"fileName,omitempty"`
	LibraryPath  string `json:"libraryPath,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	WithSidecar  bool   `json:"withSidecar,omitempty"`
	FailedDetail string `json:"failedDetail,omitempty"`
}

// DownloadedBook is the v5.1.2 completion-log entry (GET /api/v1/downloads).
type DownloadedBook struct {
	Name        string    `json:"name"`
	CompletedAt time.Time `json:"completedAt"`
	SHA256      string    `json:"sha256,omitempty"`
}

func newAPIState() *apiState {
	return &apiState{
		downloads: make(map[string]time.Time),
		jobs:      make(map[string]*DownloadJob),
	}
}

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

// sha256OfFile hashes a file in chunks (the library holds multi-MB books;
// the whole file must not sit in memory).
func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// normalizeBookFile is the file name a "!"-identifier or a DCC completion
// arrives under: the leading "!" stripped and a trailing .temp removed (the
// bot names in-flight transfers <name>.temp).
func normalizeBookFile(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "!")
	s = strings.TrimSuffix(s, ".temp")
	return s
}

// recordAPIDownload is the completion hook (one landed book). It records
// the completion log (legacy contract), matches the completion to the
// pending job (request order - the single api session processes DCC
// transfers in request order), computes the landed file's sha256 (the
// verification baseline), and drains the callback FIFO in order.
func (server *server) recordAPIDownload(name string) {
	now := time.Now()
	plain := normalizeBookFile(name)

	state := server.api
	state.mu.Lock()
	state.downloads[name] = now
	if j := state.matchPendingJob(plain); j != nil {
		j.Status = "completed"
		j.CompletedAt = now.UTC().Format(time.RFC3339)
		j.FileName = plain
		j.LibraryPath = "/api/v1/library/" + strings.ReplaceAll(plain, " ", "%20")
	}
	// Drain the FIFO: this completion belongs to the oldest queued
	// callback (book DCC transfers are handled in request order on the
	// single api session).
	cb := downloadCallback{}
	if len(state.downloadCallbacks) > 0 {
		cb = state.downloadCallbacks[0]
		state.downloadCallbacks = state.downloadCallbacks[1:]
	}
	state.mu.Unlock()

	// The file's hash (persist mode only - non-persist files are deleted
	// after serving and there is nothing to verify). Best effort: a
	// vanishing file degrades the job to "no hash", never a lost
	// completion.
	if server.settings.GetPersist() {
		p := filepath.Join(server.libraryBase(), plain)
		if h, err := sha256OfFile(p); err == nil {
			state.mu.Lock()
			for _, j := range state.jobs {
				if j.FileName == plain && j.Status == "completed" {
					j.SHA256 = h
					break
				}
			}
			state.mu.Unlock()
		}
	}

	server.log.Printf("api client: book download completed: %s\n", name)

	if cb.URL == "" {
		return
	}
	go server.fireDownloadCallback(cb, name)
}

// matchPendingJob pairs a completion to the job it belongs to. Call with
// state.mu held. Exact normalized-name match first; then the oldest pending
// job (request-order pairing - the DCC transfer order the single api
// session guarantees). A completion that matches neither is an orphan: it
// still lands in the completion log (the caller already wrote it).
func (state *apiState) matchPendingJob(plain string) *DownloadJob {
	var fallback *DownloadJob
	for _, j := range state.jobs {
		if j.Status != "requested" {
			continue
		}
		if normalizeBookFile(j.Book) == plain {
			return j
		}
		if fallback == nil || j.RequestedAt < fallback.RequestedAt {
			fallback = j
		}
	}
	return fallback
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
		writeJSON(w, http.StatusOK, HealthResponse{
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

		writeJSON(w, http.StatusOK, output)
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

// downloadsHandler is GET /api/v1/downloads - the book completions the api
// session has recorded, oldest last (the completion map is keyed by file
// name, so there is no request ordering; the timestamps give the caller
// the order). It exists because POST /api/v1/download returns the moment
// the DCC request is *sent* and the file then takes minutes to arrive:
// callers without a callbackUrl had to diff the library listing to learn
// the file landed. 404 with a JSON error when persist mode is off, same
// contract as GET /api/v1/library.
//
// v5.3.0: each entry carries the landed file's sha256 when it was computed
// (the verification baseline, adoption item 7).
func (server *server) downloadsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !server.settings.GetPersist() {
			writeJSONError(w, http.StatusNotFound, "downloads are only available with --persist")
			return
		}

		state := server.api
		state.mu.Lock()
		out := make([]DownloadedBook, 0, len(state.downloads))
		hashes := map[string]string{}
		for _, j := range state.jobs {
			if j.SHA256 != "" && j.Status == "completed" {
				hashes[j.FileName] = j.SHA256
			}
		}
		for name, ts := range state.downloads {
			out = append(out, DownloadedBook{
				Name:        name,
				CompletedAt: ts,
				SHA256:      hashes[normalizeBookFile(name)],
			})
		}
		state.mu.Unlock()

		sort.Slice(out, func(i, j int) bool { return out[i].CompletedAt.Before(out[j].CompletedAt) })
		writeJSON(w, http.StatusOK, out)
	}
}

// ── v5.3.0: job list / retry / verify endpoints ──────────────────────────

// jobsHandler is GET /api/v1/jobs - the download request log, newest first.
func (server *server) jobsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := server.api
		state.mu.Lock()
		out := make([]*DownloadJob, 0, len(state.jobs))
		for _, j := range state.jobs {
			out = append(out, j)
		}
		state.mu.Unlock()

		sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt > out[j].RequestedAt })
		writeJSON(w, http.StatusOK, out)
	}
}

// jobRetryHandler is POST /api/v1/jobs/{id}/retry - resend the DCC request
// for a terminal job (Readarr's "automatic failed download handling tries
// another release", adoption item 2, at the manual level: the poller-side
// release rotation lives in wanted.go). The same single-flight and
// validation rules apply as to POST /api/v1/download.
func (server *server) jobRetryHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		state := server.api
		state.mu.Lock()
		j, ok := state.jobs[id]
		if ok && j.Status == "requested" {
			state.mu.Unlock()
			writeJSONError(w, http.StatusConflict, "job already in flight")
			return
		}
		if !ok {
			state.mu.Unlock()
			writeJSONError(w, http.StatusNotFound, "no such job")
			return
		}
		book := j.Book
		state.mu.Unlock()

		if !strings.HasPrefix(strings.TrimSpace(book), "!") {
			recordAPIDownloadError()
			writeJSONError(w, http.StatusBadRequest, "book must be the !-prefixed identifier from search results")
			return
		}

		recordAPIDownload()
		if err := server.startAPIClient(); err != nil {
			server.log.Printf("%s\n", err)
			recordAPIStatus(http.StatusBadGateway)
			writeJSONError(w, http.StatusBadGateway, "unable to connect to IRC server")
			return
		}

		state.mu.Lock()
		j.Status = "requested"
		j.Retries++
		j.FailedDetail = ""
		j.CompletedAt = ""
		state.mu.Unlock()
		recordAPIIRCSession()
		core.DownloadBook(state.client.irc, book)
		server.log.Printf("api download retried: %s (job %s, retry %d)\n", book, id, j.Retries)
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "requested",
			"detail": "book re-requested over DCC; poll GET /api/v1/jobs until the file appears",
		})
	}
}

// verifyHandler is POST /api/v1/verify - check a landed book against its
// recorded sha256 (bookdl's verify --fix, adoption item 7, at the check
// level: a re-download is POST /api/v1/jobs/{id}/retry, not a silent
// re-fetch - the operator must see which book failed). Body:
// {"fileName": "...", "recompute": true?}. Without recompute the check is
// recorded-hash only (no I/O); with it the file is hashed again and the
// result reported. A file with no recorded hash (computed before v5.3.0)
// is "missing" with recompute=true the way to establish the baseline.
func (server *server) verifyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !server.settings.GetPersist() {
			writeJSONError(w, http.StatusNotFound, "verify is only available with --persist")
			return
		}
		var req struct {
			FileName  string `json:"fileName"`
			Recompute bool   `json:"recompute"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		fileName := strings.TrimSpace(req.FileName)
		if !validBookName(fileName) {
			writeJSONError(w, http.StatusBadRequest, "invalid file name")
			return
		}

		state := server.api
		state.mu.Lock()
		var recorded string
		jobID := ""
		for _, j := range state.jobs {
			if j.FileName == fileName && j.SHA256 != "" {
				recorded = j.SHA256
				jobID = j.ID
			}
		}
		state.mu.Unlock()

		target, ok := safeJoin(server.libraryBase(), fileName)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid file name")
			return
		}
		if _, err := os.Stat(target); err != nil {
			writeJSONError(w, http.StatusNotFound, "file not in the library")
			return
		}

		if recorded == "" && !req.Recompute {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"fileName": fileName,
				"status":   "missing",
				"detail":   "no recorded sha256; pass recompute=true to establish the baseline",
			})
			return
		}

		actual, err := sha256OfFile(target)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "unable to hash file: "+err.Error())
			return
		}

		status := "ok"
		if recorded != "" && actual != recorded {
			status = "mismatch"
		}
		// Record a fresh baseline. When the file is tied to a job the
		// hash lives on that job; otherwise a synthetic baseline job is
		// kept (id "baseline:<file>") so the next verify without
		// recompute compares against THIS run's hash instead of
		// reporting "missing" forever.
		state.mu.Lock()
		if jobID != "" {
			if j, ok := state.jobs[jobID]; ok {
				j.SHA256 = actual
			}
		} else {
			id := "baseline:" + fileName
			if j, ok := state.jobs[id]; !ok {
				state.jobs[id] = &DownloadJob{
					ID:       id,
					Book:     "",
					Status:   "completed",
					FileName: fileName,
					SHA256:   actual,
				}
			} else {
				j.SHA256 = actual
			}
		}
		state.mu.Unlock()

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"fileName": fileName,
			"status":   status,
			"sha256":   actual,
			"recorded": recorded,
		})
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
		// v5.4.2: carry the failure into the response the caller writes.
		// Returning a zero response here made every connect failure a
		// 502 with the body {"error":""} - the actual reason (a 433, a dead
		// tunnel, a DNS failure) existed only in the log. The Note feeds the
		// /api/v1/search handler, the unified IRC leg and /torznab alike.
		return APISearchResponse{Note: err.Error()}, http.StatusBadGateway, 0
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

// searchCacheCheckAndStore wraps performSearch with the v5.3.0 cache: an
// exact-query hit inside the TTL is served with ZERO IRC traffic; a miss
// falls through to the live path and stores the result on success. The
// fire-and-forget path (wait=false) does NOT use the cache (it sends a
// search and returns immediately - there is nothing to serve back) but the
// eventual outcome is not cached either (deliverAPISearch drops it).
func (server *server) searchCacheCheckAndStore(query string, f QualityFilters) (APISearchResponse, int, int) {
	key := cacheKey(query, f)
	if resp, ok := server.searchCache.lookup(key); ok {
		server.log.Printf("api search cache hit: %q\n", query)
		return resp, http.StatusOK, 0
	}

	resp, status, retryAfter := server.performSearch(query)
	if status == http.StatusOK && resp.Note != "" && !strings.Contains(resp.Note, "rate limited") &&
		resp.Note != "timed out waiting for search results" {
		// Only a real answer is cacheable: a failure note would poison
		// the entry for the whole TTL. "no results" is a real answer
		// (the negative is as valuable as the positive - a wanted poller
		// must not re-ask the channel for a title with no releases).
		server.searchCache.store(key, resp)
	}
	return resp, status, retryAfter
}

// sendSearchNow fires a search at the IRC bot and returns immediately
// (fire-and-forget). It enforces the same rate limit as performSearch but
// does NOT arm a result waiter, so it never blocks on the result. This is
// the wait=false path of POST /api/v1/search. It returns the HTTP status,
// the seconds to wait (Retry-After) and, since v5.4.2, the reason the caller
// should put in the error body - an empty note means the search was sent.
func (server *server) sendSearchNow(query string) (int, int, string) {
	state := server.api
	if err := server.startAPIClient(); err != nil {
		server.log.Printf("%s\n", err)
		return http.StatusBadGateway, 0, err.Error()
	}

	state.mu.Lock()
	nextAvailable := state.lastSearch.Add(server.config.SearchTimeout)
	if time.Now().Before(nextAvailable) {
		remaining := int(time.Until(nextAvailable).Seconds() + 0.5)
		state.mu.Unlock()
		return http.StatusTooManyRequests, remaining, fmt.Sprintf("rate limited, retry after %ds", remaining)
	}
	state.lastSearch = time.Now()
	if state.pendingSearch != nil {
		state.mu.Unlock()
		return http.StatusConflict, 0, "search already in flight, retry later"
	}
	state.mu.Unlock()

	// No waiter is armed (pendingSearch left nil), so the outcome is
	// dropped if nobody is waiting - that is the fire-and-forget contract.
	core.SearchBook(state.client.irc, server.config.SearchBot, query)
	server.log.Printf("api search sent (async): %s\n", query)
	return http.StatusOK, 0, ""
}

// normalizeQualityFilters trims, lowercases and validates a filter set so
// the cache key and the filter application agree on the request identity.
// Invalid prefer values are dropped (400 is the caller's job at the
// handler level; a silently-normalized value here keeps the shared path
// honest).
func normalizeQualityFilters(f QualityFilters) QualityFilters {
	out := QualityFilters{Language: strings.ToLower(strings.TrimSpace(f.Language))}
	for _, x := range f.Formats {
		x = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(x, ".")))
		if x != "" {
			out.Formats = append(out.Formats, x)
		}
	}
	out.MaxSizeBytes = f.MaxSizeBytes
	switch strings.ToLower(strings.TrimSpace(f.Prefer)) {
	case "ebook", "audiobook", "":
		out.Prefer = strings.ToLower(strings.TrimSpace(f.Prefer))
	default:
		out.Prefer = ""
	}
	return out
}

// searchHandler is POST /api/v1/search - send a query to the IRC search bot
// and (by default) wait for the parsed results. wait=false is
// fire-and-forget. v5.3.0: the result cache sits in front of the live
// search (exact query + filters, TTL), and the quality filters narrow the
// result set.
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
		f := normalizeQualityFilters(req.Filters)

		// One accepted search attempt (both wait paths); the 429/409/502
		// below is also counted by recordAPIStatus in each branch.
		recordAPISearch()

		wait := true
		if req.Wait != nil {
			wait = *req.Wait
		}

		if !wait {
			status, retryAfter, note := server.sendSearchNow(query)
			if status != http.StatusOK {
				if retryAfter > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				}
				recordAPIStatus(status)
				// v5.4.2: the reason, not a generic sentence.
				if note == "" {
					note = "search not sent"
				}
				writeJSONError(w, status, note)
				return
			}
			recordAPIStatus(http.StatusOK)
			writeJSON(w, http.StatusOK, APISearchResponse{Waited: false, Note: "search sent"})
			return
		}

		resp, status, retryAfter := server.searchCacheCheckAndStore(query, f)
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

		// The quality filters apply to the parsed result set (the IRC bot
		// cannot filter server-side; the channel query is untouched).
		unified := make([]unifiedResult, 0, len(resp.Books))
		for _, b := range resp.Books {
			unified = append(unified, unifiedResult{
				Source: "irc",
				Title:  b.Title,
				Author: b.Author,
				Format: b.Format,
				Size:   b.Size,
				BookID: b.Full,
			})
		}
		kept := applyQualityFilters(unified, f)
		filteredBooks := make([]core.BookDetail, 0, len(kept))
		for _, u := range kept {
			for _, b := range resp.Books {
				if b.Full == u.BookID {
					filteredBooks = append(filteredBooks, b)
					break
				}
			}
		}
		resp.Books = filteredBooks
		if len(filteredBooks) == 0 && len(unified) > 0 {
			resp.Note = "all results filtered out by quality filters"
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// downloadHandler is POST /api/v1/download - ask the IRC book server for a
// book. The identifier is the "!"-prefixed line shown in search results.
// The file arrives over DCC into the library dir; poll GET /api/v1/jobs
// (v5.3.0) or GET /api/v1/library until it appears (a DCC transfer can
// take a while).
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

		// v5.3.0: the job record (one per request). The id is the
		// request's identity for GET /api/v1/jobs and the retry endpoint.
		jobID := uuid.New().String()
		now := time.Now().UTC().Format(time.RFC3339)
		var client *Client
		state.mu.Lock()
		client = state.client
		state.jobs[jobID] = &DownloadJob{
			ID:          jobID,
			Book:        req.Book,
			Status:      "requested",
			RequestedAt: now,
			WithSidecar: req.WithSidecar,
		}
		// Bound the job log (the completion log is bounded by the library;
		// the job log is not - a caller can request 10k books). Keep the
		// 256 newest.
		if len(state.jobs) > 256 {
			ids := make([]string, 0, len(state.jobs))
			dates := map[string]string{}
			for id, j := range state.jobs {
				ids = append(ids, id)
				dates[id] = j.RequestedAt
			}
			sort.Slice(ids, func(i, j int) bool { return dates[ids[i]] < dates[ids[j]] })
			drop := len(state.jobs) - 256
			for i := 0; i < drop; i++ {
				delete(state.jobs, ids[i])
			}
		}
		// Queue the completion webhook (FIFO): the api IRC session handles
		// book DCC transfers in request order, so recordAPIDownload
		// delivers this to the oldest queued callback when the book lands.
		// callbackURL is already validated above (pre-IRC).
		if callbackURL != "" {
			state.downloadCallbacks = append(state.downloadCallbacks,
				downloadCallback{Book: req.Book, URL: callbackURL})
		}
		state.mu.Unlock()

		// The session is live (startAPIClient would have failed otherwise);
		// the client is read under the lock above so this read does not
		// race a concurrent (re)start.
		core.DownloadBook(client.irc, req.Book)
		server.log.Printf("api download requested: %s (job %s)\n", req.Book, jobID)

		resp := map[string]string{
			"status": "requested",
			"jobId":  jobID,
			"detail": "book requested over DCC; poll GET /api/v1/jobs until the file appears",
		}
		if req.CallbackURL != "" {
			resp["callback"] = "a completion POST will be sent to callbackUrl when the book lands"
		}
		if req.WithSidecar {
			resp["sidecar"] = "ebook sidecar requested; fetched by the wanted lifecycle once the audiobook lands"
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// writeJSON is the small JSON-200 helper the v5.3.0 handlers share.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// registerRoutes builds the chi router. Every data route sits behind
// requireToken (and, for the v5.3.0 scopes, a per-route requireScope);
// the static SPA and the two openapi.json copies stay open (they ship no
// data).
func (server *server) registerRoutes() *chi.Mux {
	router := chi.NewRouter()

	// Open: API documentation + the static SPA (chi's catch-all route is
	// only tried after the specific routes above it have not matched).
	router.Get("/openapi.json", server.openapiHandler())
	router.Handle("/*", server.staticFilesHandler("app/dist"))

	// Legacy browser endpoints (the React app still uses these).
	// v5.3.0: the ui scope - the legacy surface IS the UI surface, so a
	// scoped token used with the browser carries exactly this scope
	// (and nothing else can read the library through it).
	router.With(server.requireToken, server.requireScopeMW("ui")).Get("/ws", server.serveWs())
	router.With(server.requireToken, server.requireScopeMW("ui")).Get("/stats", server.statsHandler())
	router.With(server.requireToken, server.requireScopeMW("ui")).Get("/servers", server.serverListHandler())
	router.Group(func(r chi.Router) {
		r.Use(server.requireToken, server.requireScopeMW("ui"))
		r.Get("/library", server.getAllBooksHandler())
		r.Get("/library/*", server.getBookHandler())
	})
	// The legacy library delete is admin-only (it spends a write against
	// the shared download dir), so it sits OUTSIDE the ui group with its
	// own scope gate - a ui token must not be able to delete library
	// files through the browser surface.
	router.With(server.requireToken, server.requireScopeMW("admin")).Delete("/library/{fileName}", server.deleteBooksHandler())

	// Inbound Newznab: openbooks as a book indexer for the *arr stack.
	// Prowlarr/Readarr point a book indexer here and search through the
	// standard Newznab protocol (auth via ?apikey=*** like every other
	// route). It is token-gated and sits next to the SPA, not under
	// /api/v1, because indexer tools poll it exactly like they poll a
	// tracker's Newznab URL.
	// v5.3.0: the newznab scope - the indexer apikey travels in *arr
	// configs, so a scoped token for it must be the narrowest.
	router.With(server.requireToken, server.requireScopeMW("newznab")).Get("/torznab", server.torznabHandler())

	// PotatoStack v5.2: the OPDS 1.0 catalog of the local library
	// (ereader apps consume the downloaded tree directly). Token-gated
	// like /torznab; ?search=term is the OPDS search contract.
	router.With(server.requireToken, server.requireScopeMW("ui")).Get("/opds", server.opdsFeedHandler())

	// REST API for the rest of the stack. v5.3.0: the routes sit in
	// three scope groups (ui / search / admin) under the shared token
	// gate - a scoped token sees only its group, and the primary token
	// (all scopes) sees everything, exactly like the pre-v5.3 behavior.
	router.With(server.requireToken).Route("/api/v1", func(r chi.Router) {
		r.Get("/health", server.healthHandler())
		r.Get("/openapi.json", server.openapiHandler())

		// ui scope: library reads + the feeds the UI generates links
		// for (Atom/OPDS). A ui token cannot search or download.
		r.Group(func(r chi.Router) {
			r.Use(server.requireScopeMW("ui"))
			r.Get("/library", server.libraryListHandler())
			r.Get("/library/*", server.libraryFileHandler())
			r.Get("/feeds/atom", server.atomFeedHandler())
		})

		// search scope: the DAG + poller + indexer-facing surface
		// (search, the wanted watchlist, completion polling). A ui token
		// must not be able to burn the IRC budget.
		r.Group(func(r chi.Router) {
			r.Use(server.requireScopeMW("search"))
			r.Post("/search", server.searchHandler())
			r.Post("/search/unified", server.unifiedSearchHandler())
			r.Get("/downloads", server.downloadsHandler())
			// PotatoStack v5.2: the persistent Wanted watchlist. A
			// watchlist is a standing search, and the poller's
			// auto-fetch goes through the same single api session the
			// search scope already implies.
			r.Post("/wanted", server.wantedAddHandler())
			r.Get("/wanted", server.wantedListHandler())
			r.Delete("/wanted/{query}", server.wantedDeleteHandler())
		})

		// admin scope: writes + spend-the-session actions (download,
		// verify, job retry, cache clean, settings, integrations probe,
		// metrics scrape) and library deletes.
		r.Group(func(r chi.Router) {
			r.Use(server.requireScopeMW("admin"))
			r.Delete("/library/{name}", server.libraryDeleteHandler())
			r.Post("/download", server.downloadHandler())
			// PotatoStack v5.3: the per-job download view and its actions.
			r.Get("/jobs", server.jobsHandler())
			r.Post("/jobs/{id}/retry", server.jobRetryHandler())
			// PotatoStack v5.3: the sha256 verification of a landed book.
			r.Post("/verify", server.verifyHandler())
			// PotatoStack v5.3: the search-result cache stats + clean.
			r.Get("/search-cache", server.searchCacheHandler())
			r.Post("/search-cache/clean", server.searchCacheCleanHandler())
			// Prometheus-format metrics for the stack's monitoring.
			r.Get("/metrics", server.metricsHandler())
			// Runtime-mutable settings (port of the fork's a65ef3d
			// settings work). Changing the download dir at runtime
			// rewrites where downloads land without a restart.
			r.Get("/settings", server.settingsHandler())
			r.Put("/settings", server.settingsHandler())
			// Outbound peer integrations: which services are configured
			// and (with ?probe=1) reachable.
			r.Get("/integrations", server.integrationsOverviewHandler())
		})
	})

	return router
}
