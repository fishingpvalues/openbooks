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
	"context"
	_ "embed" // directive-only use: //go:embed openapi.json, no embed.X referenced
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

	lastSearch time.Time

	// At most one search may be in flight; nil when none is.
	pendingSearch chan searchOutcome

	// Recent book downloads: file name -> completion time. Lets a caller
	// see whether a requested book has landed without diffing the library.
	downloads map[string]time.Time
}

func newAPIState() *apiState {
	return &apiState{downloads: make(map[string]time.Time)}
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

	client := &Client{
		uuid: apiClientID,
		send: make(chan interface{}, 128),
		irc:  irc.New(server.config.UserName, server.config.UserAgent),
		log:  server.log,
		ctx:  context.Background(),
	}

	if err := core.Join(client.irc, server.config.Server, server.config.EnableTLS); err != nil {
		client.irc = nil
		return fmt.Errorf("api IRC connect: %w", err)
	}

	handler := server.NewIrcEventHandler(client)
	go core.StartReader(context.Background(), client.irc, handler)
	go server.apiResultPump(client)

	state.client = client
	state.connected = true
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
	state.mu.Unlock()
	server.log.Printf("api client: book download completed: %s\n", name)
}

// healthHandler is GET /api/v1/health - the token probe the UI uses.
func (server *server) healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{
			Name:    "openbooks",
			Version: server.config.Version,
			Persist: server.settings.GetPersist(),
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

		if fileName == "" || fileName == "." || fileName == ".." ||
			strings.ContainsAny(fileName, `/\`) || strings.ContainsRune(fileName, 0) {
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

// searchHandler is POST /api/v1/search - send a query to the IRC search bot
// and (by default) wait for the parsed results.
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
			writeJSONError(w, http.StatusBadRequest, "query is required")
			return
		}

		wait := true
		if req.Wait != nil {
			wait = *req.Wait
		}

		state := server.api
		if err := server.startAPIClient(); err != nil {
			server.log.Printf("%s\n", err)
			writeJSONError(w, http.StatusBadGateway, "unable to connect to IRC server")
			return
		}

		state.mu.Lock()
		nextAvailable := state.lastSearch.Add(server.config.SearchTimeout)
		if time.Now().Before(nextAvailable) {
			remaining := int(time.Until(nextAvailable).Seconds() + 0.5)
			state.mu.Unlock()
			writeJSONError(w, http.StatusTooManyRequests,
				fmt.Sprintf("rate limited, retry after %ds", remaining))
			return
		}
		state.lastSearch = time.Now()

		var outcomeCh chan searchOutcome
		if wait {
			if state.pendingSearch != nil {
				state.mu.Unlock()
				writeJSONError(w, http.StatusConflict, "search already in flight, retry later")
				return
			}
			outcomeCh = make(chan searchOutcome, 1)
			state.pendingSearch = outcomeCh
		}
		state.mu.Unlock()

		// Arm the waiter BEFORE sending, so a fast result cannot be missed.
		core.SearchBook(state.client.irc, server.config.SearchBot, query)
		server.log.Printf("api search sent: %q\n", query)

		if !wait {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(APISearchResponse{Waited: false, Note: "search sent"})
			return
		}

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
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if !strings.HasPrefix(strings.TrimSpace(req.Book), "!") {
			writeJSONError(w, http.StatusBadRequest,
				"book must be the !-prefixed identifier from search results")
			return
		}

		if err := server.startAPIClient(); err != nil {
			server.log.Printf("%s\n", err)
			writeJSONError(w, http.StatusBadGateway, "unable to connect to IRC server")
			return
		}

		state := server.api
		core.DownloadBook(state.client.irc, req.Book)
		server.log.Printf("api download requested: %s\n", req.Book)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "requested",
			"detail": "book requested over DCC; poll GET /api/v1/library until the file appears",
		})
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

	// REST API for the rest of the stack.
	router.With(server.requireToken).Route("/api/v1", func(r chi.Router) {
		r.Get("/health", server.healthHandler())
		r.Get("/openapi.json", server.openapiHandler())
		r.Get("/library", server.libraryListHandler())
		r.Get("/library/*", server.libraryFileHandler())
		r.Delete("/library/{name}", server.libraryDeleteHandler())
		r.Post("/search", server.searchHandler())
		r.Post("/download", server.downloadHandler())
		// Runtime-mutable settings (port of the fork's a65ef3d settings
		// work). Changing the download dir at runtime rewrites where
		// downloads land without a restart.
		r.Get("/settings", server.settingsHandler())
		r.Put("/settings", server.settingsHandler())
	})

	return router
}
