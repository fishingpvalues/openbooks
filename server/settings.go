package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Settings are the runtime-configurable parts of the server config. The
// download directory and persistence flag were previously fixed at startup;
// the settings endpoints let a running instance change the save location
// without a restart. (Ported from the fork's a65ef3d settings work; the
// endpoints are behind the v5 token middleware like every data route.)
type Settings struct {
	mu          sync.RWMutex
	DownloadDir string `json:"downloadDir"`
	Persist     bool   `json:"persist"`
}

// SetDownloadDir validates and applies a new download directory.
func (s *Settings) SetDownloadDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("download dir must be an absolute path, got %q", dir)
	}
	dir = filepath.Clean(dir)
	if dir == "/" {
		return fmt.Errorf("refusing to use / as download dir")
	}
	// Create the dir plus the books/ child the library handlers expect.
	if err := os.MkdirAll(filepath.Join(dir, "books"), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	// Prove writability before switching the active dir, so a failed
	// download never ends up in a read-only location.
	probe := filepath.Join(dir, ".openbooks-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	_ = os.Remove(probe)

	s.mu.Lock()
	s.DownloadDir = dir
	s.mu.Unlock()
	return nil
}

// GetDownloadDir returns the active download directory.
func (s *Settings) GetDownloadDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.DownloadDir
}

// SetPersist toggles whether downloads are kept after being sent to the
// browser.
func (s *Settings) SetPersist(persist bool) {
	s.mu.Lock()
	s.Persist = persist
	s.mu.Unlock()
}

// GetPersist reports the current persistence flag.
func (s *Settings) GetPersist() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Persist
}

func (server *server) settingsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Add("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"downloadDir": server.settings.GetDownloadDir(),
				"persist":     server.settings.GetPersist(),
			})
		case http.MethodPut:
			var req struct {
				DownloadDir *string `json:"downloadDir"`
				Persist     *bool   `json:"persist"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			if req.DownloadDir != nil {
				if err := server.settings.SetDownloadDir(*req.DownloadDir); err != nil {
					server.log.Printf("Rejected download dir change: %s\n", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			if req.Persist != nil {
				server.settings.SetPersist(*req.Persist)
			}

			w.Header().Add("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"downloadDir": server.settings.GetDownloadDir(),
				"persist":     server.settings.GetPersist(),
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// libraryPath is a convenience wrapper over path.Join for download links so
// the basepath prefix is applied in exactly one place.
func libraryPath(name string) string {
	return path.Join("library", name)
}

// rejectInvalidDir reports whether dir fails the minimal checks applied
// before SetDownloadDir does the full validation.
func rejectInvalidDir(dir string) bool {
	return !filepath.IsAbs(dir) || dir == "/" || strings.Contains(dir, "\x00")
}
