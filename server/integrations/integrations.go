package integrations

import (
	"os"
	"strings"
)

// Config is the set of peer-service endpoints openbooks can call. Every
// field is optional: an empty base URL means that integration is disabled
// and the client returns ErrDisabled. This is what keeps a standalone
// `openbooks server` (single-user desktop mode) working with no env set.
//
// On potatostack the OPENBOOKS_* values are the durable BRIDGE-IP addresses
// measured from the gluetun netns (see the package doc and
// docs/openbooks/stack-state-2026-09-01.md section 6). They are dynamic and
// may change on peer container recreate - a move is a one-line env change.
type Config struct {
	// Prowlarr base URL + X-Api-Key (GET /api/v1/search, /api/v1/indexer).
	ProwlarrBaseURL string `json:"prowlarrBaseURL"`
	ProwlarrAPIKey  string `json:"-"` // never serialized; it is a credential

	// Audiobookshelf base URL + JWT API key (Authorization: Bearer <key>).
	AudiobookshelfBaseURL string `json:"audiobookshelfBaseURL"`
	AudiobookshelfAPIKey  string `json:"-"`

	// Calibre-Web (CWA) base URL (OPDS root; no HTTP auth).
	CalibreWebBaseURL string `json:"calibreWebBaseURL"`

	// Readarr base URL + X-Api-Key. Empty on potatostack (Readarr is not
	// deployed); set when Readarr lands.
	ReadarrBaseURL string `json:"readarrBaseURL"`
	ReadarrAPIKey  string `json:"-"`

	// DownloadCallback is an optional outbound webhook: when a DCC download
	// completes, openbooks POSTs a small JSON body to this URL. This is the
	// arr-style completion hook (Readarr/Prowlarr can point it at a handler).
	// Empty = no callback. It is independent of the four peer clients above
	// (it is an address openbooks CALLS, not a service it queries).
	DownloadCallback string `json:"downloadCallback,omitempty"`

	// DownloadCallbackToken, when set, is sent as `Authorization: Bearer <t>`
	// on the callback POST so the receiving endpoint can authenticate it.
	DownloadCallbackToken string `json:"-"`
}

// FromEnv builds a Config from the environment. The OPENBOOKS_* names follow
// the potatostack compose convention (every service's vars are OPENBOOKS_-
// prefixed); the short names are accepted too for standalone use.
//
//	OPENBOOKS_PROWLARR_URL / PROWLARR_URL              -> ProwlarrBaseURL
//	OPENBOOKS_PROWLARR_API_KEY / PROWLARR_API_KEY      -> ProwlarrAPIKey
//	OPENBOOKS_AUDIOBOOKSHELF_URL / AUDIOBOOKSHELF_URL  -> AudiobookshelfBaseURL
//	OPENBOOKS_AUDIOBOOKSHELF_API_KEY / AUDIOBOOKSHELF_API_KEY -> AudiobookshelfAPIKey
//	OPENBOOKS_CALIBREWEB_URL / CALIBREWEB_URL          -> CalibreWebBaseURL
//	OPENBOOKS_READARR_URL / READARR_URL                -> ReadarrBaseURL
//	OPENBOOKS_READARR_API_KEY / READARR_API_KEY        -> ReadarrAPIKey
//	OPENBOOKS_DOWNLOAD_CALLBACK / DOWNLOAD_CALLBACK    -> DownloadCallback
//	OPENBOOKS_DOWNLOAD_CALLBACK_TOKEN / DOWNLOAD_CALLBACK_TOKEN -> DownloadCallbackToken
func FromEnv() Config {
	// first returns the first non-empty of the given names.
	first := func(names ...string) string {
		for _, n := range names {
			if v := os.Getenv(n); v != "" {
				return v
			}
		}
		return ""
	}

	return Config{
		ProwlarrBaseURL:       normalize(first("OPENBOOKS_PROWLARR_URL", "PROWLARR_URL")),
		ProwlarrAPIKey:        first("OPENBOOKS_PROWLARR_API_KEY", "PROWLARR_API_KEY"),
		AudiobookshelfBaseURL: normalize(first("OPENBOOKS_AUDIOBOOKSHELF_URL", "AUDIOBOOKSHELF_URL")),
		AudiobookshelfAPIKey:  first("OPENBOOKS_AUDIOBOOKSHELF_API_KEY", "AUDIOBOOKSHELF_API_KEY"),
		CalibreWebBaseURL:     normalize(first("OPENBOOKS_CALIBREWEB_URL", "CALIBREWEB_URL")),
		ReadarrBaseURL:        normalize(first("OPENBOOKS_READARR_URL", "READARR_URL")),
		ReadarrAPIKey:         first("OPENBOOKS_READARR_API_KEY", "READARR_API_KEY"),
		DownloadCallback:      normalize(first("OPENBOOKS_DOWNLOAD_CALLBACK", "DOWNLOAD_CALLBACK")),
		DownloadCallbackToken: first("OPENBOOKS_DOWNLOAD_CALLBACK_TOKEN", "DOWNLOAD_CALLBACK_TOKEN"),
	}
}

// normalize trims whitespace and a trailing slash from a base URL so the
// join in http.go produces a clean result.
func normalize(s string) string {
	return strings.Trim(strings.TrimSpace(s), "/")
}

// Bundle holds the constructed clients. Built once at server start from a
// Config (FromEnv or explicit) and shared read-only by the HTTP handlers.
type Bundle struct {
	Prowlarr       *ProwlarrClient
	Audiobookshelf *AudiobookshelfClient
	CalibreWeb     *CalibreWebClient
	Readarr        *ReadarrClient
	Config         Config
}

// New builds a Bundle from a Config. Disabled clients are still present (and
// report ErrDisabled) so handlers can branch on Enabled() without a nil
// check.
func New(cfg Config) *Bundle {
	return &Bundle{
		Prowlarr:       NewProwlarr(cfg.ProwlarrBaseURL, cfg.ProwlarrAPIKey),
		Audiobookshelf: NewAudiobookshelf(cfg.AudiobookshelfBaseURL, cfg.AudiobookshelfAPIKey),
		CalibreWeb:     NewCalibreWeb(cfg.CalibreWebBaseURL),
		Readarr:        NewReadarr(cfg.ReadarrBaseURL, cfg.ReadarrAPIKey),
		Config:         cfg,
	}
}

// FromEnv builds a Bundle from the environment.
func FromEnvBundle() *Bundle {
	return New(FromEnv())
}
