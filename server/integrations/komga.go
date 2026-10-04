package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// KomgaClient talks to Komga's REST API (gotson/komga). Live-verified
// 2026-10-04 on 1.28.0, which replaced Calibre-Web-Automated on potatostack:
// CWA rewrote every book it imported, Komga only reads the seeding payloads.
//
// Auth is an API key in the `X-API-Key` header (Komga 1.11+, created under
// Account > API Keys). Search is GET /api/v1/books?search=<term>, a Spring
// page ({content, totalElements, ...}); each book carries its library,
// series, file url, size, media type and the metadata title/authors Komga
// read from the file.
type KomgaClient struct {
	*base
	// publicURL is the browser-facing Komga address (e.g. the tailnet URL).
	// Optional: when set, each result carries a webUrl to the book's page.
	publicURL string
}

// NewKomga builds a client. baseURL empty => disabled. publicURL is optional.
func NewKomga(baseURL, apiKey, publicURL string) *KomgaClient {
	return &KomgaClient{
		base:      newBase("komga", baseURL, apiKey, "X-API-Key"),
		publicURL: strings.TrimRight(publicURL, "/"),
	}
}

func (c *KomgaClient) Enabled() bool { return c.baseURL != "" }

// KomgaLibrary is the slice of a GET /api/v1/libraries entry we keep.
type KomgaLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}

// ListLibraries returns the configured libraries. It is the functional probe:
// it needs a valid API key, unlike Ping.
func (c *KomgaClient) ListLibraries(ctx context.Context) ([]KomgaLibrary, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	raw, status, err := c.get(ctx, "/api/v1/libraries", nil)
	if err != nil {
		return nil, err
	}
	if !statusOK(status) {
		return nil, fmt.Errorf("komga libraries: HTTP %d", status)
	}
	var out []KomgaLibrary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("komga libraries: decode: %w", err)
	}
	return out, nil
}

// KomgaBook is one search hit, flattened for the unified result shape.
type KomgaBook struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors,omitempty"`
	Series      string   `json:"series,omitempty"`
	LibraryID   string   `json:"libraryId,omitempty"`
	MediaType   string   `json:"mediaType,omitempty"`
	Format      string   `json:"format,omitempty"`
	SizeBytes   int64    `json:"sizeBytes"`
	FileName    string   `json:"fileName,omitempty"`
	WebURL      string   `json:"webUrl,omitempty"`
	DownloadURL string   `json:"downloadUrl"`
}

// rawKomgaPage is the minimal Spring page of BookDto we decode.
type rawKomgaPage struct {
	Content []struct {
		ID          string `json:"id"`
		SeriesTitle string `json:"seriesTitle"`
		LibraryID   string `json:"libraryId"`
		Name        string `json:"name"`
		URL         string `json:"url"`
		SizeBytes   int64  `json:"sizeBytes"`
		Media       struct {
			MediaType    string `json:"mediaType"`
			MediaProfile string `json:"mediaProfile"`
		} `json:"media"`
		Metadata struct {
			Title   string `json:"title"`
			Authors []struct {
				Name string `json:"name"`
				Role string `json:"role"`
			} `json:"authors"`
		} `json:"metadata"`
	} `json:"content"`
	TotalElements int `json:"totalElements"`
}

// SearchBooks runs a full-text search over every library. limit <= 0 uses 20.
func (c *KomgaClient) SearchBooks(ctx context.Context, term string, limit int) ([]KomgaBook, int, error) {
	if !c.Enabled() {
		return nil, 0, ErrDisabled
	}
	if limit <= 0 {
		limit = 20
	}
	q := url.Values{}
	q.Set("search", term)
	q.Set("size", fmt.Sprintf("%d", limit))
	raw, status, err := c.get(ctx, "/api/v1/books", q)
	if err != nil {
		return nil, 0, err
	}
	if !statusOK(status) {
		return nil, 0, fmt.Errorf("komga books: HTTP %d", status)
	}
	var page rawKomgaPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, 0, fmt.Errorf("komga books: decode: %w", err)
	}
	out := make([]KomgaBook, 0, len(page.Content))
	for _, b := range page.Content {
		title := b.Metadata.Title
		if title == "" {
			title = b.Name
		}
		kb := KomgaBook{
			ID:          b.ID,
			Title:       title,
			Series:      b.SeriesTitle,
			LibraryID:   b.LibraryID,
			MediaType:   b.Media.MediaType,
			Format:      strings.ToLower(b.Media.MediaProfile),
			SizeBytes:   b.SizeBytes,
			FileName:    b.URL[strings.LastIndex(b.URL, "/")+1:],
			DownloadURL: "/api/v1/books/" + url.PathEscape(b.ID) + "/file",
		}
		for _, a := range b.Metadata.Authors {
			if a.Role == "" || a.Role == "writer" || a.Role == "author" {
				kb.Authors = append(kb.Authors, a.Name)
			}
		}
		if c.publicURL != "" {
			kb.WebURL = c.publicURL + "/book/" + url.PathEscape(b.ID)
		}
		out = append(out, kb)
	}
	return out, page.TotalElements, nil
}
