package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/evan-buss/openbooks/irc"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

//go:embed app/dist
var reactClient embed.FS

// serveWs handles websocket requests from the peer.
//
// PotatoStack v5: auth comes from the requireToken middleware (the route is
// wrapped in registerRoutes), so a token-less upgrade is already a 401
// before this handler runs. The CheckOrigin pin below keeps the websocket
// same-origin-only; upstream set it to "always allow", which let any site
// open the connection and drive IRC searches for the connected identity.
func (server *server) serveWs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("OpenBooks")
		if errors.Is(err, http.ErrNoCookie) {
			cookie = &http.Cookie{
				Name:     "OpenBooks",
				Value:    uuid.New().String(),
				Secure:   false,
				HttpOnly: true,
				Expires:  time.Now().Add(time.Hour * 24 * 7),
				SameSite: http.SameSiteStrictMode,
			}
			w.Header().Add("Set-Cookie", cookie.String())
		}

		userId, err := uuid.Parse(cookie.Value)
		_, alreadyConnected := server.clients[userId]

		// The single-IRC-connection rule applies to BROWSER clients only:
		// the api client (apiClientID) holds the server-owned session for
		// the /api/v1 endpoints and must never block the UI (and vice
		// versa).
		wsClients := 0
		for id := range server.clients {
			if id != apiClientID {
				wsClients++
			}
		}

		// If invalid UUID or the same browser tries to connect again or multiple browser connections
		// Don't connect to IRC or create new client
		if err != nil || alreadyConnected || wsClients > 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Same-origin only: browsers send the Origin header, and for a
		// websocket to the same site Origin == scheme://host must match
		// Host. Missing Origin (non-browser clients) is allowed - those go
		// through the token check above like everything else.
		upgrader.CheckOrigin = func(req *http.Request) bool {
			if req.Header.Get("Origin") == "" {
				return true
			}
			origin, err := url.Parse(req.Header.Get("Origin"))
			if err != nil {
				return false
			}
			return origin.Host == req.Host
		}

		conn, err := upgrader.Upgrade(w, r, w.Header())
		if err != nil {
			server.log.Println(err)
			return
		}

		client := &Client{
			conn: conn,
			send: make(chan interface{}, 128),
			uuid: userId,
			irc:  irc.New(server.config.UserName, server.config.UserAgent),
			log:  log.New(os.Stdout, fmt.Sprintf("CLIENT (%s): ", server.config.UserName), log.LstdFlags|log.Lmsgprefix),
			ctx:  context.Background(),
		}

		server.log.Printf("Client connected from %s\n", conn.RemoteAddr().String())
		client.log.Println("New client created.")

		server.register <- client

		go server.writePump(client)
		go server.readPump(client)
	}
}

func (server *server) staticFilesHandler(assetPath string) http.Handler {
	// update the embedded file system's tree so that index.html is at the root
	app, err := fs.Sub(reactClient, assetPath)
	if err != nil {
		server.log.Println(err)
	}

	// strip the predefined base path and serve the static file
	return http.StripPrefix(server.config.Basepath, http.FileServer(http.FS(app)))
}

func (server *server) statsHandler() http.HandlerFunc {
	type statsReponse struct {
		UUID string `json:"uuid"`
		IP   string `json:"ip"`
		Name string `json:"name"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		result := make([]statsReponse, 0, len(server.clients))

		for _, client := range server.clients {
			// The api client (server-owned IRC session, see api.go) has no
			// websocket connection - a nil conn is the normal case for it.
			ip := ""
			if client.conn != nil {
				ip = client.conn.RemoteAddr().String()
			}

			details := statsReponse{
				UUID: client.uuid.String(),
				Name: client.irc.Username,
				IP:   ip,
			}

			result = append(result, details)
		}

		json.NewEncoder(w).Encode(result)
	}
}

func (server *server) serverListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(server.repository.servers)
	}
}

func (server *server) getAllBooksHandler() http.HandlerFunc {
	type download struct {
		Name         string    `json:"name"`
		DownloadLink string    `json:"downloadLink"`
		Time         time.Time `json:"time"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if !server.settings.GetPersist() {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		libraryDir := filepath.Join(server.settings.GetDownloadDir(), "books")
		books, err := os.ReadDir(libraryDir)
		if err != nil {
			server.log.Printf("Unable to list books. %s\n", err)
		}

		output := make([]download, 0)
		for _, book := range books {
			if book.IsDir() || strings.HasPrefix(book.Name(), ".") || filepath.Ext(book.Name()) == ".temp" {
				continue
			}

			info, err := book.Info()
			if err != nil {
				server.log.Println(err)
			}

			dl := download{
				Name:         book.Name(),
				DownloadLink: path.Join("library", book.Name()),
				Time:         info.ModTime(),
			}

			output = append(output, dl)
		}

		w.Header().Add("Content-Type", "application/json")
		json.NewEncoder(w).Encode(output)
	}
}

// getBookHandler serves GET /library/{fileName...}.
//
// PotatoStack v5: the wildcard may contain subfolders (the bookdl pipeline
// organizes into subdirectories), so the whole capture is validated with
// safeJoin instead of taking the last path segment. Without this, nested
// books were simply un-downloadable, and a "..%2F" sequence was a traversal
// (see deleteBooksHandler's comment for the upstream story).
func (server *server) getBookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := server.libraryBase()
		// chi wildcard: <basepath>library/<name>
		raw := strings.TrimPrefix(r.URL.Path, server.config.Basepath+"library/")
		name, err := urlUnescapePath(raw)
		if err != nil || name == "" {
			http.Error(w, "invalid file name", http.StatusBadRequest)
			return
		}

		target, ok := safeJoin(base, name)
		if !ok {
			server.log.Printf("Rejected book path outside library: %q\n", name)
			http.Error(w, "invalid file name", http.StatusBadRequest)
			return
		}

		if _, err := os.Stat(target); err != nil {
			http.Error(w, "book not found", http.StatusNotFound)
			return
		}

		http.ServeFile(w, r, target)

		// Non-persist mode serves the file, then removes it - upstream
		// behavior, kept verbatim: the download link must still work
		// before the book disappears.
		if !server.settings.GetPersist() {
			if err := os.Remove(target); err != nil {
				server.log.Printf("Error when deleting book file. %s", err)
			}
		}
	}
}

// validBookName reports whether fileName is a plausible book file name:
// one path segment, no traversal or NUL, not a dotfile. Shared by the
// delete handler and the test suite.
func validBookName(fileName string) bool {
	if fileName == "" || fileName == "." || fileName == ".." {
		return false
	}
	if strings.ContainsAny(fileName, "/\\") || strings.ContainsRune(fileName, 0) {
		return false
	}
	return !strings.HasPrefix(fileName, ".")
}

// POTATOSTACK PATCH: arbitrary file deletion via the {fileName} parameter.
//
// chi routes on URL.RawPath when it is set, so the parameter still holds its
// percent-encoding, and the explicit url.PathUnescape below then turns
// "..%2F..%2Fx" into "../../x" AFTER routing has already accepted it as one
// path segment. filepath.Join collapses that and os.Remove deletes outside the
// books directory. Reachable unauthenticated: gluetun publishes this UI on
// 127.0.0.1:8083 and 8083 is in TAILSCALE_SERVE_PORTS, so every tailnet peer
// can call it. openbooks has no auth of its own.
//
// Neither error branch returned either, so an unescape failure fell through
// and called os.Remove with an empty name, and both branches wrote a second
// header after the first.
func (server *server) deleteBooksHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fileName, err := url.PathUnescape(chi.URLParam(r, "fileName"))
		if err != nil {
			server.log.Printf("Error unescaping path: %s\n", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// A book file name is one path segment. Anything carrying a separator,
		// a traversal component or a NUL is a request for a file this endpoint
		// does not own.
		if !validBookName(fileName) {
			server.log.Printf("Rejected book file name: %q\n", fileName)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		base := filepath.Join(server.settings.GetDownloadDir(), "books")
		target := filepath.Join(base, fileName)

		// Defence in depth: the checks above already exclude separators, so this
		// only catches a base that is itself odd, but it costs nothing and keeps
		// the guarantee local to the call that acts on it.
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
	}
}
