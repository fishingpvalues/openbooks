// Package integrations is the PotatoStack v5 outbound API surface: typed
// clients for the peer services openbooks talks to (Prowlarr, Audiobookshelf,
// Komga, Readarr). Upstream openbooks has no such surface - it is an
// IRC/DCC ebook downloader whose only downstream is a shared staging dir.
//
// Every client is OPTIONAL. A client whose base URL is empty is disabled and
// its methods return ErrDisabled; this keeps a standalone `openbooks server`
// (single-user desktop mode) working exactly like upstream with no env set,
// while `openbooks:local` on potatostack configures the peers it can reach.
//
// Addressing: openbooks runs in the gluetun network namespace (network_mode:
// service:gluetun). The 2026-09-01 note that docker service names do not
// resolve there was wrong when measured on 2026-09-16 (and again 2026-10-04
// for komga): gluetun's DNS answers container names, and bridge IPs are
// dynamic. Address peers by service name, e.g.
//
//	prowlarr         http://prowlarr:9696
//	audiobookshelf   http://audiobookshelf:80
//	komga            http://komga:25600
//
// Set the OPENBOOKS_* base URLs accordingly.
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
const defaultTimeout = 60000 // ms, mirrored as a time.Duration in http.go
