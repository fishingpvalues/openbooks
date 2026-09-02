package server

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/cors"

	"github.com/evan-buss/openbooks/server/integrations"
)

type server struct {
	// Shared app configuration
	config *Config

	// Shared data
	repository *Repository

	// Registered clients.
	clients map[uuid.UUID]*Client

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	log *log.Logger

	// Mutex to guard the lastSearch timestamp
	lastSearchMutex sync.Mutex

	// The time the last search was performed. Used to rate limit searches.
	lastSearch time.Time

	// PotatoStack v5: the shared server-owned IRC session backing the
	// /api/v1 REST endpoints (see api.go).
	api *apiState

	// PotatoStack v5: runtime-mutable settings (GET/PUT /settings),
	// seeded from the static config and overridable via the API. See
	// server/settings.go.
	settings *Settings

	// PotatoStack v5 integrations: the outbound peer clients (Prowlarr,
	// Audiobookshelf, Calibre-Web, Readarr) plus the download-completion
	// webhook config. Built from the environment at start; every client is
	// optional (empty base URL = disabled). See server/integrations/.
	integrations *integrations.Bundle
}

// Config contains settings for server
type Config struct {
	Log                     bool
	Port                    string
	UserName                string
	Persist                 bool
	DownloadDir             string
	Basepath                string
	Server                  string
	EnableTLS               bool
	SearchTimeout           time.Duration
	SearchBot               string
	DisableBrowserDownloads bool
	UserAgent               string

	// PotatoStack v5 additions:
	// Token, when set (OPENBOOKS_TOKEN or --token), guards every API route
	// and the websocket. See server/auth.go.
	Token string

	// BindIP is the listen address. Defaults to loopback: upstream binds
	// :port which on a LAN-reachable machine publishes the whole UI (and,
	// before v5, the library) to everyone. The docker image passes 0.0.0.0
	// explicitly because a container must listen on all its interfaces.
	BindIP string

	// Version is reported by /api/v1/health.
	Version string
}

func New(config Config) *server {
	s := &server{
		repository:   NewRepository(),
		config:       &config,
		register:     make(chan *Client),
		unregister:   make(chan *Client),
		clients:      make(map[uuid.UUID]*Client),
		log:          log.New(os.Stdout, "SERVER: ", log.LstdFlags|log.Lmsgprefix),
		api:          newAPIState(),
		settings:     &Settings{},
		integrations: integrations.FromEnvBundle(),
	}
	// Seed the runtime settings from the startup config; the CLI
	// ensures the dir exists and is writable before Start is called.
	if err := s.settings.SetDownloadDir(config.DownloadDir); err != nil {
		s.log.Fatalf("invalid download dir %s: %s", config.DownloadDir, err)
	}
	s.settings.SetPersist(config.Persist)
	return s
}

// Start instantiates the web server and opens the browser
func Start(config Config) {
	createBooksDirectory(config)

	// PotatoStack v5: the token can come from the flag (--token, wired in
	// cmd/) or from the environment so container deploys do not need to put
	// secrets on a command line.
	if config.Token == "" {
		config.Token = os.Getenv("OPENBOOKS_TOKEN")
	}

	// PotatoStack v5: loopback by default. The docker image passes BindIP
	// 0.0.0.0 explicitly; a bare `openbooks server` must not publish the UI
	// to the LAN.
	bindIP := config.BindIP
	if bindIP == "" {
		bindIP = "127.0.0.1"
	}

	if config.Token != "" {
		log.Println("API token enabled: every route except the static SPA and /openapi.json requires it (Authorization: Bearer, X-OpenBooks-Token header, or ?token=)")
	} else {
		log.Println("WARNING: no API token set (OPENBOOKS_TOKEN). All API routes are open - single-user mode only.")
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)

	// The SPA is same-origin in production and the 5173 vite dev server in
	// development; the dev origin is the only cross-origin caller.
	// (The AllowCredentials + wildcard combination upstream used is
	// rejected by the CORS spec and is meaningless here anyway.)
	corsConfig := cors.Options{
		AllowCredentials: true,
		AllowedOrigins:   []string{"http://127.0.0.1:5173", "http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "DELETE", "OPTIONS"},
	}
	router.Use(cors.New(corsConfig).Handler)

	server := New(config)
	routes := server.registerRoutes()

	ctx, cancel := context.WithCancel(context.Background())
	go server.startClientHub(ctx)
	server.registerGracefulShutdown(cancel)
	router.Mount(config.Basepath, routes)

	server.log.Printf("Base Path: %s\n", config.Basepath)
	server.log.Printf("OpenBooks is listening on %s:%v\n", bindIP, config.Port)
	server.log.Printf("Download Directory: %s\n", config.DownloadDir)
	server.log.Printf("Open http://localhost:%v%s in your browser.", config.Port, config.Basepath)
	server.log.Fatal(http.ListenAndServe(net.JoinHostPort(bindIP, config.Port), router))
}

// The client hub is to be run in a goroutine and handles management of
// websocket client registrations.
func (server *server) startClientHub(ctx context.Context) {
	for {
		select {
		case client := <-server.register:
			server.clients[client.uuid] = client
		case client := <-server.unregister:
			if _, ok := server.clients[client.uuid]; ok {
				_, cancel := context.WithCancel(client.ctx)
				close(client.send)
				cancel()
				delete(server.clients, client.uuid)
			}
		case <-ctx.Done():
			for _, client := range server.clients {
				_, cancel := context.WithCancel(client.ctx)
				close(client.send)
				cancel()
				delete(server.clients, client.uuid)
			}
			return
		}
	}
}

func (server *server) registerGracefulShutdown(cancel context.CancelFunc) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		server.log.Println("Graceful shutdown.")
		// Close the shutdown channel. Triggering all reader/writer WS handlers to close.
		cancel()
		time.Sleep(time.Second)
		os.Exit(0)
	}()
}

func createBooksDirectory(config Config) {
	err := os.MkdirAll(filepath.Join(config.DownloadDir, "books"), os.FileMode(0755))
	if err != nil {
		panic(err)
	}
}
