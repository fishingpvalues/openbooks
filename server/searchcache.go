package server

// PotatoStack v5.3.0: the search-result cache.
//
// Research note (docs/openbooks/feature-matrix.md, 2026-09-02, adoption
// item 5): bookdl runs the same problem - its IRC/indexer sources are
// polite-behavior-limited remotely even though they are cheap locally, and
// it keeps a TTL search cache (24h default) with stats and clean commands.
// The IRC channel's 10s rate budget is the same constraint: a DAG and the
// poller re-asking the same query in quick succession burn channel
// goodwill for results that have not moved.
//
// The cache sits in front of performSearch: an EXACT query (same filters)
// inside the TTL is served from cache with a "cached" note and ZERO IRC
// traffic; everything else falls through to the live path unchanged. The
// live path writes the cache on success (a rate-limited or failed search
// must not poison the cache with a note). Cache state is process-local
// (in-memory only, deliberately - the wanted watchlist is the persistent
// state, and search results are cheap to re-earn).
//
// TTL: OPENBOOKS_SEARCH_CACHE_TTL (Go duration; default 24h; 0 disables
// the cache entirely - the pre-v5.3 behavior).
//
// Endpoints (admin scope, token-gated under /api/v1):
//
//	GET  /search-cache          -> {entries, hits, misses, ttl}
//	POST /search-cache/clean    -> 200 {removed}

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/evan-buss/openbooks/core"
)

// searchCacheDefaultTTL is the default entry lifetime. It mirrors bookdl's
// 24h default: long enough that a wanted poller (one search per minute at
// the floor) never re-hits the IRC channel for the same title, short enough
// that "no results" does not read as permanent.
const searchCacheDefaultTTL = 24 * time.Hour

// searchCacheMaxEntries bounds the map (an operator who queries 10k
// distinct titles must not grow the process; the oldest entries drop).
const searchCacheMaxEntries = 4096

type searchCacheEntry struct {
	at       time.Time
	response APISearchResponse
}

type searchCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]searchCacheEntry
	byAge   []string // insertion order (oldest first) for the bound check
	hits    uint64
	misses  uint64
}

func newSearchCache(ttl time.Duration) *searchCache {
	return &searchCache{
		ttl:     ttl,
		entries: make(map[string]searchCacheEntry),
	}
}

// cacheKey is the exact request identity: the trimmed query plus the
// quality filters (an epub search and a pdf search for the same title are
// different requests and must not share an entry).
func cacheKey(query string, f QualityFilters) string {
	return strings.ToLower(strings.TrimSpace(query)) + "|" +
		normalizeFormatList(f.Formats) + "|" +
		strings.ToLower(strings.TrimSpace(f.Language)) + "|" +
		itoa64(f.MaxSizeBytes)
}

// lookup returns the cached response for an unexpired key. A disabled
// cache (zero TTL) always misses: the pre-v5.3 behavior.
func (c *searchCache) lookup(key string) (APISearchResponse, bool) {
	if c.ttl <= 0 {
		recordCacheMiss()
		return APISearchResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		c.misses++
		recordCacheMiss()
		return APISearchResponse{}, false
	}
	if time.Since(e.at) > c.ttl {
		delete(c.entries, key)
		c.removeAge(key)
		c.misses++
		recordCacheMiss()
		return APISearchResponse{}, false
	}
	c.hits++
	recordCacheHit()
	out := e.response
	if out.Note == "" {
		out.Note = "cached"
	} else {
		out.Note = out.Note + " (cached)"
	}
	return out, true
}

// store records a successful live search. The bound check evicts the
// oldest entry first (insertion order, not TTL order - simpler and the
// entries are all TTL-bounded anyway). A disabled cache stores nothing.
func (c *searchCache) store(key string, response APISearchResponse) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.byAge = append(c.byAge, key)
	}
	c.entries[key] = searchCacheEntry{at: time.Now(), response: response}
	for len(c.byAge) > searchCacheMaxEntries {
		oldest := c.byAge[0]
		c.byAge = c.byAge[1:]
		if _, ok := c.entries[oldest]; ok {
			delete(c.entries, oldest)
		}
	}
}

// removeAge drops key from the age order. Call with c.mu held.
func (c *searchCache) removeAge(key string) {
	for i, k := range c.byAge {
		if k == key {
			c.byAge = append(c.byAge[:i], c.byAge[i+1:]...)
			return
		}
	}
}

// stats is the GET /search-cache body.
func (c *searchCache) stats() (int, uint64, uint64, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.hits, c.misses, c.ttl
}

// clean drops every entry and returns how many were dropped.
func (c *searchCache) clean() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.entries)
	c.entries = make(map[string]searchCacheEntry)
	c.byAge = nil
	return n
}

// searchCacheHandler is GET /api/v1/search-cache.
func (server *server) searchCacheHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, hits, misses, ttl := server.searchCache.stats()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"entries": entries,
			"hits":    hits,
			"misses":  misses,
			"ttl":     ttl.String(),
		})
	}
}

// searchCacheCleanHandler is POST /api/v1/search-cache/clean.
func (server *server) searchCacheCleanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		removed := server.searchCache.clean()
		server.log.Printf("search cache: cleaned %d entries\n", removed)
		writeJSON(w, http.StatusOK, map[string]int{"removed": removed})
	}
}

// ── quality filters (adoption item 4) ────────────────────────────────────
//
// bookdl exposes format/language/year/size filters on every search; Readarr
// exposes per-request quality profiles. The IRC bot cannot be told to
// filter server-side (the query goes to a channel), and the Prowlarr
// search API takes no size/format params either, so the filtering is
// applied to the parsed result set - honest about the mechanism, and it
// works identically for every source.

// QualityFilters are the request-level quality controls for a search.
// Every field is optional; an empty struct is the v5.2 behavior (no
// filtering). The fields are persisted on wanted entries, so a poller
// round re-searches WITH the filters (the wanted contract: the operator
// asked for a specific shape, and a re-match that drifts shape is a
// different book).
type QualityFilters struct {
	// Formats, when non-empty, restricts results to these extensions
	// (lowercase, no dot: ["epub","pdf"]). A result whose format is
	// unknown (the bot reports none) is DROPPED when Formats is set -
	// an explicit format list is a shape demand.
	Formats []string `json:"formats,omitempty"`

	// Language restricts to results whose title contains the language
	// tag (the IRC bot has no language metadata; the title is the only
	// signal - "german", "deutsch", "english", ...). Case-insensitive
	// substring on the lowercased title.
	Language string `json:"language,omitempty"`

	// MaxSizeBytes, when > 0, drops results larger than the cap.
	// Results with an unparseable size are kept (size-unknown is not
	// size-large).
	MaxSizeBytes int64 `json:"maxSizeBytes,omitempty"`

	// Prefer selects the media class the operator wants:
	// "ebook", "audiobook", or "" (either). An empty class match is
	// kept (a result the parser could not classify); a result of the
	// other class is dropped.
	Prefer string `json:"prefer,omitempty"`
}

// formatOf returns the extension class of a unified result: the explicit
// Format when set, else the file extension of the title.
func formatOf(r unifiedResult) string {
	if r.Format != "" {
		return strings.ToLower(r.Format)
	}
	return extOf(r.Title)
}

// applyQualityFilters filters results in place (returns the kept
// subset). It never returns nil for a non-empty input (an empty slice is
// the zero-result contract callers expect).
func applyQualityFilters(results []unifiedResult, f QualityFilters) []unifiedResult {
	if len(f.Formats) == 0 && f.Language == "" && f.MaxSizeBytes == 0 && f.Prefer == "" {
		return results
	}
	allowed := make(map[string]bool, len(f.Formats))
	for _, x := range f.Formats {
		allowed[strings.ToLower(strings.TrimSpace(x))] = true
	}
	lang := strings.ToLower(strings.TrimSpace(f.Language))
	out := make([]unifiedResult, 0, len(results))
	for _, r := range results {
		// Format list: an explicit list is a demand - unknown format
		// (empty) fails it.
		if len(allowed) > 0 {
			fm := formatOf(r)
			if fm == "" || !allowed[fm] {
				continue
			}
		}
		// Language: substring on the lowercased title (no metadata
		// exists to do better).
		if lang != "" && !strings.Contains(strings.ToLower(r.Title), lang) {
			continue
		}
		// Size cap: size-unknown passes (it is not large).
		if f.MaxSizeBytes > 0 {
			if sz := core.ParseSizeToBytes(r.Size); sz > f.MaxSizeBytes {
				continue
			}
		}
		// Prefer: a result of the other class is dropped; an
		// unclassified result passes (it may still be the wanted book).
		if f.Prefer == "ebook" {
			fm := formatOf(r)
			if fm != "" && !core.IsEbookFormat(fm) {
				continue
			}
		} else if f.Prefer == "audiobook" {
			fm := formatOf(r)
			if fm != "" && !core.IsAudioFormat(fm) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// ── cross-source dedupe (adoption item 8) ────────────────────────────────
//
// The same book hits IRC, a Newznab indexer and Prowlarr; without grouping
// the operator sees N hits for one book, and the wanted poller would
// re-download it N times. Komga's dedupe discipline (duplicate-file and
// duplicate-page detection) applied EARLIER - at the result level instead
// of the library level - is the model.
//
// Grouping is by dedupKey: the ISBN when the source exposes one, else
// lowercased title + author (both trimmed). The IRC leg has no ISBN
// (title+author), so a Prowlarr hit and the IRC hit for the same title
// group together on the title+author key - which is exactly the
// same-book identity the operator reasons in.

// dedupKeyOf returns the grouping key for one unified result.
func dedupKeyOf(r unifiedResult) string {
	title := strings.ToLower(strings.TrimSpace(r.Title))
	author := strings.ToLower(strings.TrimSpace(r.Author))
	if title == "" && author == "" {
		// No grouping signal at all: fall back to the fetch identifier
		// (a degenerate group of one; a result with no title/author has
		// no dedup partner).
		return r.BookID
	}
	return title + "|" + author
}

// annotateDedupe walks the result list and sets each result's DedupGroup
// to the number of results that share its key (1 = unique). The list is
// not reordered.
func annotateDedupe(results []unifiedResult) {
	if len(results) == 0 {
		return
	}
	counts := make(map[string]int, len(results))
	for _, r := range results {
		counts[dedupKeyOf(r)]++
	}
	for i := range results {
		results[i].DedupGroup = counts[dedupKeyOf(results[i])]
	}
}

// ── small helpers ────────────────────────────────────────────────────────

// extOf returns the lowercased file extension (no dot) of a name.
func extOf(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		switch name[i] {
		case '.':
			return strings.ToLower(name[i+1:])
		case ' ', '/':
			return ""
		}
	}
	return ""
}

// normalizeFormatList renders a format list canonically (sorted,
// lowercase, dot-stripped) so the cache key is stable across
// spellings of the same request.
func normalizeFormatList(formats []string) string {
	out := make([]string, 0, len(formats))
	for _, f := range formats {
		f = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(f, ".")))
		if f != "" {
			out = append(out, f)
		}
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return strings.Join(out, ",")
}

// itoa64 is the cache-key rendering of a byte count.
func itoa64(n int64) string {
	if n <= 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
