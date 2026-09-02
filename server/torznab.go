package server

// PotatoStack v5 integration: the inbound Newznab endpoint.
//
// This is what makes openbooks usable BY the *arr stack. Prowlarr and Readarr
// can point a book indexer at openbooks and query it through the Newznab
// search protocol (the same protocol every tracker indexer uses):
//
//	GET <basepath>torznab/?t=caps            -> capability document
//	GET <basepath>torznab/?t=search&q=<term> -> book search, Newznab XML
//
// Auth is the same token as every other API route, carried the Newznab way:
// the ?apikey=*** query parameter (Prowlarr/Readarr indexer configs accept an
// API key field and append it to the URL). The Authorization header and
// X-OpenBooks-Token forms are accepted too, since requireToken already
// normalizes all three.
//
// The search reuses performSearch (the shared core of POST /api/v1/search),
// so the Newznab endpoint and the REST search share the IRC session, the
// rate limit, and the single in-flight-search rule.

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
)

// newznabCaps is the capability document a Newznab indexer advertises. It is
// minimal on purpose: openbooks is a book-search indexer, not a full torrent
// indexer, so only t=search / t=book are meaningful. The <api> element (the
// standard way an indexer tells a tool which key to use) is appended
// dynamically in torznabHandler, because it carries the deployment's token.
const newznabCaps = `<caps version="2">
<search>
<searchfield name="q" label="book title or author">text</searchfield>
<groupname name="type" label="indexer type">
<groupvalue name="search"/>
<groupvalue name="book"/>
<groupvalue name="bookish"/>
</groupname>
</search>
<limits maxage="1460" minage="1"/>
</caps>
`

// newznabCapsFor returns the caps document with the <api> element injected
// just before </caps> when a token is configured (indexer tools read the key
// from caps; that is the documented Newznab behavior). Tokenless mode
// advertises no api element.
func newznabCapsFor(token string) string {
	if token == "" {
		return newznabCaps
	}
	const close = "</caps>"
	i := strings.Index(newznabCaps, close)
	if i < 0 {
		return newznabCaps
	}
	// The token is already validated by the caller; xml-escape so a token
	// with special characters cannot break the document.
	escaped := xmlEscapeAttr(token)
	return newznabCaps[:i] + `<api key="` + escaped + `" root=""/>` + "\n" + newznabCaps[i:]
}

// xmlEscapeAttr escapes the five XML entities in an attribute value.
func xmlEscapeAttr(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// newznabResponse is the Newznab <search> document returned for t=search.
type newznabResponse struct {
	XMLName xml.Name      `xml:"search"`
	Items   []newznabItem `xml:"item"`
}

// newznabItem is one <item>. The fields are the Newznab core set; the
// download URL points at the openbooks library endpoint for the book the
// search refers to.
type newznabItem struct {
	Title       string `xml:"title"`
	Index       string `xml:"index"`
	Size        string `xml:"size"`
	PubDate     string `xml:"pubDate"`
	Comments    string `xml:"comments"`
	DownloadURL string `xml:"url"`
}

// torznabHandler is GET <basepath>torznab - the inbound Newznab endpoint.
func (server *server) torznabHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// Newznab carries the key as ?apikey=*** requireToken already checked
		// the token (via apikey, Authorization, or X-OpenBooks-Token), so a
		// wrong apikey=*** here is already a 401; nothing to re-check.

		w.Header().Set("Content-Type", "text/xml; charset=utf-8")

		switch q.Get("t") {
		case "caps":
			_, _ = w.Write([]byte(newznabCapsFor(server.config.Token)))
			return
		case "search", "book", "bookish", "":
			term := strings.TrimSpace(q.Get("q"))
			if term == "" {
				http.Error(w, "missing q parameter", http.StatusBadRequest)
				return
			}

			resp, status, retryAfter := server.performSearch(term)
			if status == http.StatusConflict || status == http.StatusTooManyRequests ||
				status == http.StatusBadGateway {
				if retryAfter > 0 {
					// The Newznab way an indexer client learns to back off:
					// Retry-After on the 429.
					w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				}
				http.Error(w, resp.Note, status)
				return
			}

			doc := newznabResponse{Items: []newznabItem{}}
			for _, b := range resp.Books {
				doc.Items = append(doc.Items, newznabItem{
					Title:       b.Title,
					Index:       b.Author,
					Size:        b.Size,
					PubDate:     "2026-01-01T00:00:00Z",
					Comments:    b.Format,
					DownloadURL: server.libraryDownloadURL(b.Title, b.Full),
				})
			}
			if err := xml.NewEncoder(w).Encode(doc); err != nil {
				server.log.Printf("torznab: encode: %s\n", err)
			}
		default:
			http.Error(w, "unsupported t="+q.Get("t"), http.StatusBadRequest)
		}
	}
}

// libraryDownloadURL builds the token-protected download URL for a book a
// search result refers to. The book file lands in the library under its DCC
// file name (the "full" field of the BookDetail - the !-prefixed line from
// the search results), so the URL is /api/v1/library/<name>. Callers add
// ?token=*** (the same token as the search) to download.
func (server *server) libraryDownloadURL(title, full string) string {
	name := full
	if name == "" {
		name = title
	}
	// Strip the leading "!" and any trailing .temp so the library path is the
	// clean file name the DCC download produces.
	name = strings.TrimPrefix(name, "!")
	name = strings.TrimSuffix(name, ".temp")
	return "/api/v1/library/" + strings.ReplaceAll(name, " ", "%20")
}
