package server

// PotatoStack v5 integration: the outbound peer overview + connectivity
// probes.
//
// GET <basepath>api/v1/integrations reports which peer integrations are
// configured and (with ?probe=1) whether they answer. It is the operator's
// window into "is this instance wired to Prowlarr/ABS/CWA/Readarr?" and it
// turns the bridge-IP fragility (docs/openbooks/stack-state-2026-09-01.md
// GAPS #2) into something observable: after a peer container recreate that
// moves a bridge IP, the probe for that peer reports unreachable until the
// OPENBOOKS_*_URL env value is updated.

import (
	"encoding/json"
	"net/http"

	"github.com/evan-buss/openbooks/server/integrations"
)

// integrationsOverviewHandler is GET /api/v1/integrations.
func (server *server) integrationsOverviewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := server.integrations.Config
		peer := func(name, baseURL string, enabled bool) map[string]interface{} {
			return map[string]interface{}{
				"name":    name,
				"enabled": enabled,
				"baseURL": baseURL,
			}
		}

		ov := map[string]interface{}{
			"prowlarr":       peer("prowlarr", cfg.ProwlarrBaseURL, server.integrations.Prowlarr.Enabled()),
			"audiobookshelf": peer("audiobookshelf", cfg.AudiobookshelfBaseURL, server.integrations.Audiobookshelf.Enabled()),
			"calibreweb":     peer("calibreweb", cfg.CalibreWebBaseURL, server.integrations.CalibreWeb.Enabled()),
			"readarr":        peer("readarr", cfg.ReadarrBaseURL, server.integrations.Readarr.Enabled()),
			"downloadCallback": map[string]interface{}{
				"configured": cfg.DownloadCallback != "",
			},
		}

		// Live probes only on opt-in: probing every peer on every overview
		// call would hammer the services. Two signals per peer: `reachable`
		// is a transport-level Ping (ANY HTTP status counts - Calibre-Web
		// /opds/ answers 401 without a session and would otherwise read as
		// down; a transport error is the real "unreachable", e.g. a moved
		// bridge IP) and `apiProbe` is a functional call against the peer's
		// real API (ok / HTTP status), which says whether the configured
		// key/path still works. The probes are cheap GETs.
		if r.URL.Query().Get("probe") == "1" {
			ov["probes"] = map[string]interface{}{
				"prowlarr": peerProbe(func() (int, error) {
					return server.integrations.Prowlarr.Ping(r.Context())
				}, func() (bool, string) {
					_, err := server.integrations.Prowlarr.ListIndexers(r.Context())
					return err == nil, errToMsg(err)
				}),
				"audiobookshelf": peerProbe(func() (int, error) {
					return server.integrations.Audiobookshelf.Ping(r.Context())
				}, func() (bool, string) {
					_, err := server.integrations.Audiobookshelf.ListLibraries(r.Context())
					return err == nil, errToMsg(err)
				}),
				"calibreweb": peerProbe(func() (int, error) {
					return server.integrations.CalibreWeb.Ping(r.Context())
				}, func() (bool, string) {
					_, err := server.integrations.CalibreWeb.RootFeed(r.Context())
					return err == nil, errToMsg(err)
				}),
				"readarr": peerProbe(func() (int, error) {
					return server.integrations.Readarr.Ping(r.Context())
				}, func() (bool, string) {
					_, _, err := server.integrations.Readarr.HasBook(r.Context(), "")
					return err == nil, errToMsg(err)
				}),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ov)
	}
}

// peerProbe runs a transport Ping (reachable) and a functional API call
// (apiProbe) against one peer and reports both. A disabled peer reports
// {reachable:false, reason:"not configured"} without issuing a request.
func peerProbe(ping func() (int, error), api func() (bool, string)) map[string]interface{} {
	status, err := ping()
	if err != nil && err == integrations.ErrDisabled {
		return map[string]interface{}{
			"reachable": false,
			"reason":    "not configured",
		}
	}
	out := map[string]interface{}{
		"reachable": err == nil,
	}
	if err != nil {
		out["reason"] = err.Error()
	} else {
		out["status"] = status
	}
	ok, reason := api()
	if ok {
		out["apiProbe"] = "ok"
	} else {
		out["apiProbe"] = reason
	}
	return out
}

// errToMsg turns a client error into a short operator-facing string.
func errToMsg(err error) string {
	if err == nil {
		return "ok"
	}
	if err == integrations.ErrDisabled {
		return "not configured"
	}
	return err.Error()
}
