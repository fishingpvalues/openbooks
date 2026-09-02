// Package integrations is the PotatoStack v5 outbound API surface: typed
// clients for the peer services openbooks talks to (Prowlarr, Audiobookshelf,
// Calibre-Web, Readarr). Upstream openbooks has no such surface - it is an
// IRC/DCC ebook downloader whose only downstream is a shared staging dir.
//
// Every client is OPTIONAL. A client whose base URL is empty is disabled and
// its methods return ErrDisabled; this keeps a standalone `openbooks server`
// (single-user desktop mode) working exactly like upstream with no env set,
// while `openbooks:local` on potatostack configures the peers it can reach.
//
// Addressing (measured 2026-09-01, see docs/openbooks/stack-state-2026-09-01.md
// section 6): openbooks runs in the gluetun network namespace
// (network_mode: service:gluetun), so it gets gluetun's /etc/resolv.conf
// (9.9.9.9), NOT docker's embedded DNS. Docker service names (prowlarr,
// audiobookshelf, calibre-web) do not resolve there, host.docker.internal is
// resolvable but unroutable, and the LAN-IP path to CWA fails. The only
// working durable addresses are the peers' BRIDGE IPs on 172.22.0.0/16:
//
//	prowlarr         172.22.0.7:9696
//	audiobookshelf   172.22.0.71:80
//	calibre-web      172.22.0.35:8083
//
// Those bridge IPs are DYNAMIC (reallocation on container recreate - the
// exact reason the prowlarr compose block refuses a pinned IP). They are
// configurable here precisely so a recreation that moves an IP is a one-line
// env change, not a code change. Set the OPENBOOKS_* base URLs accordingly.
package integrations

import "errors"

// ErrDisabled is returned by a client method when the client's base URL is
// empty (the peer is not configured / not reachable from this instance).
// Callers treat it as "this integration is off", not as a failure to alert on.
var ErrDisabled = errors.New("integrations: client not configured (empty base URL)")

// Default timeout for a single outbound call. A Prowlarr book search that
// fans out to many indexers through the gluetun VPN can take tens of
// seconds; the IRC search path is rate-limited separately, so this is the
// budget for the peer round-trip only.
const defaultTimeout = 30000 // ms, mirrored as a time.Duration in httputil.go
