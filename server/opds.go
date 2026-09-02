package server

// PotatoStack v5.2.0: the OPDS catalog of the local library.
//
// GET <basepath>opds (token-gated, token accepted in the standard
// query/header forms like every other route) serves the library
// directory tree as an OPDS 1.0 acquisition feed:
//
//	GET /opds               -> the whole library (newest first, 500 cap)
//	GET /opds?search=<term> -> case-insensitive substring match on the
//	                           file name (the OPDS search contract every
//	                           comparable server uses: calibre-web
//	                           /opds/search?search=, COPS feed.php?search=,
//	                           Kavita /api/Opds/{apiKey}?search=)
//
// Research note (docs/openbooks/feature-matrix.md, 2026-09-02): every
// catalogue-shaped project (calibre-web, Komga v1+v2, Kavita, COPS,
// Grimmory) exposes OPDS; no downloader-shaped one does. Serving the
// downloaded tree as OPDS makes the library directly consumable by
// ereader apps (KyBook, KOReader, Apple Books' OPDS support, Feedly's
// OPDS reader) without a second catalog service standing in front of
// the same directory. The tree is FLAT (one file per book - the
// download bot's output), so no series/author grouping is invented:
// the file name IS the metadata, and guessing a richer schema from a
// file name would be fabrication.
//
// Entry links are absolute with ?token=*** because OPDS clients
// (ereader firmware, phone apps) cannot set Authorization headers on
// content fetches; the token is already accepted in query form by
// requireToken. This is the same exposure model as the Newznab <api>
// element in /torznab caps (documented there), and the tailnet-only
// bind (HOST_BIND=127.0.0.1 + tailscale serve) is what keeps the
// absolute links private.

import (
	"encoding/xml"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// opdsFeed is the OPDS 1.0 (Atom acquisition feed) document shape.
type opdsFeed struct {
	XMLName xml.Name    `xml:"feed"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Entries []opdsEntry `xml:"entry"`
}

// opdsEntry is one book. rel=alternate + a book MIME type is the
// acquisition link OPDS clients render as "download / open".
type opdsEntry struct {
	ID      string     `xml:"id"`
	Title   string     `xml:"title"`
	Updated string     `xml:"updated"`
	Links   []atomLink `xml:"link"`
}

// dirEntry is one library file with its metadata.
type dirEntry struct {
	name string
	size int64
	mod  time.Time
}

// readDirNonRecursive lists the top-level (non-hidden, non-.temp,
// non-directory) files of dir. Shared by the Atom feed and the OPDS
// catalog so the "what counts as a book file" rule lives in one place.
func readDirNonRecursive(dir string) ([]dirEntry, error) {
	books, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(books))
	for _, b := range books {
		if b.IsDir() || strings.HasPrefix(b.Name(), ".") ||
			strings.HasSuffix(b.Name(), ".temp") {
			continue
		}
		var size int64
		var mod time.Time
		if info, err := b.Info(); err == nil {
			size = info.Size()
			mod = info.ModTime()
		}
		out = append(out, dirEntry{name: b.Name(), size: size, mod: mod})
	}
	return out, nil
}

// opdsMediaURL is the absolute acquisition URL for one book.
func opdsMediaURL(r *http.Request, server *server, name string) string {
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host + server.config.Basepath +
		"api/v1/library/" + strings.ReplaceAll(name, "%", "%25") +
		"?token=***"
}

// opdsFeedHandler is GET /opds[?search=term].
func (server *server) opdsFeedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !server.settings.GetPersist() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"library is only available with --persist"}`))
			return
		}

		now := time.Now().UTC()
		opdsSelf := opdsBaseURL(r, server)

		entries, err := readDirNonRecursive(server.libraryBase())
		if err != nil {
			server.log.Printf("opds: %s\n", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"library unavailable"}`))
			return
		}

		// OPDS search contract: ?search= is a case-insensitive filter on
		// the title (the file name). No term -> the full catalog.
		term := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
		sort.Slice(entries, func(i, j int) bool {
			if !entries[i].mod.Equal(entries[j].mod) {
				return entries[i].mod.After(entries[j].mod)
			}
			return entries[i].name < entries[j].name
		})

		var kept []dirEntry
		for _, e := range entries {
			if term == "" || strings.Contains(strings.ToLower(e.name), term) {
				kept = append(kept, e)
			}
		}
		if len(kept) > 500 {
			kept = kept[:500]
		}

		feed := opdsFeed{
			ID:      "urn:openbooks:opds:root",
			Title:   "openbooks library",
			Updated: now.Format(time.RFC3339),
			Links: []atomLink{
				{Rel: "self", Href: opdsSelf},
				{Rel: "search", Type: "application/atom+xml",
					Href: opdsSelf + "?search=%s"},
			},
			Entries: make([]opdsEntry, 0, len(kept)),
		}
		if term != "" {
			feed.Title = "openbooks search: " + r.URL.Query().Get("search")
		}

		for _, e := range kept {
			updated := e.mod
			if updated.IsZero() {
				updated = now
			}
			feed.Entries = append(feed.Entries, opdsEntry{
				ID:      "urn:openbooks:library:" + e.name,
				Title:   e.name,
				Updated: updated.UTC().Format(time.RFC3339),
				Links: []atomLink{
					{Rel: "alternate", Type: mimeForName(e.name),
						Href: opdsMediaURL(r, server, e.name)},
				},
			})
		}

		w.Header().Set("Content-Type", "application/atom+xml; profile=opds-catalog")
		w.WriteHeader(http.StatusOK)
		xml.NewEncoder(w).Encode(feed)
	}
}

// opdsBaseURL is the absolute URL of this OPDS endpoint.
func opdsBaseURL(r *http.Request, server *server) string {
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host + server.config.Basepath + "opds"
}
