# [v5.1.2] - 2026-09-02

## Added
- `GET /api/v1/downloads` - book completions the api session has recorded (name + completedAt, oldest last). The polling path for `POST /api/v1/download` callers without a `callbackUrl`: before this, learning that a file landed required diffing the library listing. 404 when persist mode is off.
- `GET /api/v1/metrics` - Prometheus text format: `openbooks_up`, `openbooks_version`, `openbooks_irc_connected`, `openbooks_irc_sessions_total`, `openbooks_searches_total`, `openbooks_downloads_total`, `openbooks_download_errors_total`, `openbooks_downloads_completed`, `openbooks_callback_queue_size`, `openbooks_http_requests_total{status}`. Token-gated like every `/api/v1` route.
- `ircConnected` on `GET /api/v1/health` - the HTTP process being alive is not the same as the IRC session being up; the field says which of the two a probe actually saw.
- `.github/workflows/ci.yml` - `go vet` + `go test` on push/PR to `integrated`/`master`, plus a docker image build on push (the tag-based release workflows did not gate the line between tags).

## Fixed
- The api IRC session no longer dies permanently. `startAPIClient` wired the reader's death into a `reapSession` hook (a new `onDeath` parameter on `core.StartReader`): when the connection drops (gluetun netns flap, irchighway server drop) the session is marked down and the next search re-establishes it. Before this, the `connected` flag stayed `true` over the dead conn, the first search after a drop silently burned the 120s wait, and every later search hit the dead conn while every HTTP healthcheck still passed - the same failure shape as the v4.5.0 join race, in steady state.

# [v5.1.1] - 2026-09-02

## Added
- `Retry-After` header on the 429 rate limit (`POST /api/v1/search` and `GET /torznab`) - indexer clients (Prowlarr/Readarr) back off on the header instead of parsing the message body.
- Full `/api/v1` handler test coverage (`server/api_test.go`).

## Changed
- `POST /api/v1/download` validates the request (book identifier, `callbackUrl`) before opening the IRC session, so a malformed request is a 400 rather than a failed 502.
- The v5 delete name policy is aligned with the legacy handler (dotfiles and backslashes rejected via `validBookName`).

# [v5.1.0] - 2026-09-01

## Added
- Newznab /torznab book-indexer endpoint (inbound; Prowlarr/Readarr can add openbooks as a book indexer the standard way).
- Outbound peer clients: Prowlarr, Audiobookshelf, Calibre-Web, Readarr (`server/integrations/`).
- Download-completion webhook (`callbackUrl` on `POST /api/v1/download`, plus the static `OPENBOOKS_DOWNLOAD_CALLBACK`).
- `GET /api/v1/integrations` overview (+ `?probe=1` live reachability).
- `GET/PUT /api/v1/settings` (runtime-mutable download dir + persist).
- `GET /api/v1/health`, `/api/v1/library`, `/api/v1/search`, `/api/v1/download` (the REST API on the server-owned IRC session).
- Bearer-token authentication on every route except the SPA and `/openapi.json` (`OPENBOOKS_TOKEN`).
- Hardened archive extraction (path-traversal guard + 5GiB entry cap), IRC TLS certificate pinning, IRC join race fix, `--bind` defaults to 127.0.0.1.
- Embedded OpenAPI document at `GET /openapi.json`.
- Go test suites in `server/`, `core/`, `dcc/`, `irc/`, `util/`.

# [v4.5.0] - 2023-01-08
