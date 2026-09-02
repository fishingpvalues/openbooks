package server

// PotatoStack v5.2.0: the Atom feed of library activity.
//
// GET /api/v1/feeds/atom (token-gated like every /api/v1 route) is an
// Atom 0.3 feed with one entry per book the api session has seen:
//
//   - the current library contents (persist mode), newest first; and
//   - the recent completions from GET /api/v1/downloads (always, because
//     the completion log is the "what landed just now" signal even when
//     the library itself is stable).
//
// Research note (docs/openbooks/feature-matrix.md, 2026-09-02): NO
// downloader-shaped project (bookdl, Shelfmark, ReadMeABook, Mylar3)
// ships an RSS/Atom feed of results or releases - the gap is universal
// among the downloaders and filled only by the catalogue-shaped servers
// (and even they mostly serve OPDS, not RSS). An Atom feed is the
// zero-polling integration point for the rest of the stack (a DAG, a
// Telegram bot, Readarr if it ever appears): subscribe once, get woken
// up when a book lands. The feed is deliberately small and flat - entry
// id is the book name, updated is the completion or file mtime - so a
// feed reader needs no state to tell new from old.
//
// Content types: the library files are whatever the download bot sent
// (epub, pdf, zip of search results, ...); the Atom media type is
// application/octet-stream for file entries and text/plain for the feed
// title, which is the honest choice - Atom does not require a per-file
// MIME table, and guessing "application/epub+zip" for a file that turns
// out to be a PDF would be a lie.

import (
	"encoding/xml"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// atomFeed is the Atom 0.3 document shape for the endpoints in this file.
// Namespace: the default Atom namespace (the xml package emits the
// <feed> root in it when the type's field order is right; links/ids use
// absolute URLs so a feed reader can follow them).
type atomFeed struct {
	XMLName  xml.Name     `xml:"feed"`
	Title    string       `xml:"title"`
	Updated  string       `xml:"updated"`
	Link     []atomLink   `xml:"link"`
	ID       string       `xml:"id"`
	Subtitle string       `xml:"subtitle,omitempty"`
	Entries  []atomEntry  `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	Title   string     `xml:"title"`
	ID      string     `xml:"id"`
	Updated string     `xml:"updated"`
	Published string   `xml:"published,omitempty"`
	Link    []atomLink `xml:"link"`
	Content string     `xml:"content"`
	// ContentType for <content type="xhtml"> is kept simple: text/plain
	// bodies (no XHTML wrapper) - Atom allows type="text" only via xhtml,
	// so the pragmatic choice used by minimal feeders is a type-less
	// <content> whose body is escaped text (rendered as a code block by
	// every major reader).
	ContentType string `xml:"type,attr,omitempty"`
}

// feedEntryURL is the absolute URL a feed reader uses to fetch one book.
// It is absolute (host from the request) because feed readers resolve
// relative links against the FEED's URL, and this endpoint sits under
// the basepath - a relative /api/v1/library/... would resolve wrong.
func feedEntryURL(r *http.Request, server *server, name string) string {
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

// atomFeedHandler is GET /api/v1/feeds/atom.
func (server *server) atomFeedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		feed := atomFeed{
			Title:   "openbooks library - " + server.config.Version,
			Updated: now.Format(time.RFC3339),
			ID:      "urn:openbooks:" + server.config.Version + ":library",
			Link: []atomLink{
				{Rel: "self", Href: feedSelfURL(r, server)},
			},
			Subtitle: "Books in the openbooks library (persist mode) plus recent completions",
			Entries:  []atomEntry{},
		}

		seen := map[string]bool{}

		// 1. Recent completions (always available - the completion log
		// does not require persist mode to be meaningful, though the
		// library section below does).
		state := server.api
		state.mu.Lock()
		type completion struct {
			name string
			at   time.Time
		}
		var completions []completion
		for name, at := range state.downloads {
			completions = append(completions, completion{name, at})
		}
		state.mu.Unlock()
		sort.Slice(completions, func(i, j int) bool {
			return completions[i].at.After(completions[j].at)
		})
		for _, c := range completions {
			if seen[c.name] {
				continue
			}
			seen[c.name] = true
			// Cap the completions section: the downloads map is bounded
			// by the library, but a feed with 20k entries is not a feed.
			if len(feed.Entries) >= 100 {
				break
			}
			feed.Entries = append(feed.Entries, atomEntry{
				Title:     c.name,
				ID:        "urn:openbooks:completion:" + c.name,
				Updated:   c.at.UTC().Format(time.RFC3339),
				Published: c.at.UTC().Format(time.RFC3339),
				Link:      []atomLink{{Rel: "alternate", Href: feedEntryURL(r, server, c.name)}},
				Content:   "completed by the openbooks api session",
			})
		}

		// 2. The library itself (persist mode only), newest first.
		if server.settings.GetPersist() {
			base := server.libraryBase()
			entries, err := readDirNonRecursive(base)
			if err == nil {
				sort.Slice(entries, func(i, j int) bool {
					return entries[i].mod.After(entries[j].mod)
				})
				for _, e := range entries {
					if seen[e.name] {
						continue
					}
					seen[e.name] = true
					if len(feed.Entries) >= 100 {
						break
					}
					updated := e.mod
					if updated.IsZero() {
						updated = now
					}
					feed.Entries = append(feed.Entries, atomEntry{
						Title:     e.name,
						ID:        "urn:openbooks:library:" + e.name,
						Updated:   updated.UTC().Format(time.RFC3339),
						Link:      []atomLink{{Rel: "alternate", Href: feedEntryURL(r, server, e.name)}},
						Content:   "in library since " + updated.UTC().Format(time.RFC3339),
					})
				}
			}
		}

		w.Header().Set("Content-Type", "application/atom+xml")
		w.WriteHeader(http.StatusOK)
		xml.NewEncoder(w).Encode(feed)
	}
}

// feedSelfURL is the absolute URL of the feed itself (the rel=self link
// a feed reader uses for cache validation).
func feedSelfURL(r *http.Request, server *server) string {
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host + server.config.Basepath + "api/v1/feeds/atom"
}

// feedEntry helper (kept next to the type for the OPDS file below): the
// extension-to-MIME table the OPDS media links use. Deliberately small:
// the library is whatever the download bot sends, and the honest answer
// for an unknown extension is application/octet-stream.
var fileMimeByExt = map[string]string{
	".epub": "application/epub+zip",
	".pdf":  "application/pdf",
	".mobi": "application/x-mobipocket-ebook",
	".azw3": "application/vnd.amazon.ebook",
	".cbr":  "application/vnd.comicbook+rar",
	".cbz":  "application/vnd.comicbook+zip",
	".zip":  "application/zip",
	".txt":  "text/plain",
	".html": "text/html",
	".fb2":  "application/x-fictionbook+xml",
	".m4b":  "audio/mp4",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".ogg":  "audio/ogg",
}

func mimeForName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if m, ok := fileMimeByExt[ext]; ok {
		return m
	}
	return "application/octet-stream"
}
