# [v5.2.0] - 2026-09-02

## Added
- The persistent **Wanted watchlist** - `POST /api/v1/wanted` `{query, author?, autoFetch?}` adds a book to re-search; `GET /api/v1/wanted` lists entries (oldest first); `DELETE /api/v1/wanted/{query}` removes one (URL-escaped path segment). Duplicate queries (case-insensitive) are a 409. A background poller (interval `OPENBOOKS_WANTED_POLL_INTERVAL`, Go duration, default 5m; 0 disables the poller while the REST endpoints keep working) sends ONE IRC search per tick through the shared `performSearch` path - same single-flight rule, same 10s rate limit, never bypassed - until the entry matches. `autoFetch: true` entries fetch the first match through the same `core.DownloadBook` path as a manual download, so completions land in `GET /api/v1/downloads` and fire the callback webhooks in order. The watchlist persists across restarts as `<downloadDir>/wanted.json` (persist mode only; follows the runtime download dir like the library does). The biggest gap vs the comparable downloader projects (Mylar3 watchlist, Shelfmark/ReadMeABook request queue) - `docs/openbooks/feature-matrix.md`.
- `GET /api/v1/feeds/atom` - an Atom 0.3 feed of library activity: the current library contents (persist mode, newest first) plus the recent completions, 100-entry cap, entry links absolute and token-carrying. No downloader-shaped comparable project ships an RSS/Atom feed; it is the zero-polling integration point for DAGs, bots and (a future) Readarr.
- `GET /opds` - the OPDS 1.0 catalog of the local library tree (newest first, 500-entry cap, hidden files and `.temp` excluded, per-extension MIME on the acquisition links). `?search=term` is the OPDS search contract (case-insensitive title filter). Token-gated; entry links are absolute with `?token=*** OPDS clients cannot set Authorization headers on content fetches (same exposure model as the Newznab `<api>` element).
- `POST /api/v1/search/unified` `{query, sources?}` - searches the IRC session and Prowlarr in one request and returns a normalized result list with a per-source status (`ok` / `not-configured` / `rate-limited` / `busy` / `bad-gateway` / `error`). One dead source never hides the others' results; the IRC leg's 429/409 is reported in its status, not as a failed request.
- Metrics: `openbooks_wanted_entries`, `openbooks_wanted_unmatched` (gauges), `openbooks_unified_searches_total`, `openbooks_wanted_matches_total`, `openbooks_wanted_autofetches_total` (counters).

## Fixed
- The Prowlarr search leg's wire shape, caught by the first live run of the unified search: `age` is an INT (whole days, not a string) and `indexerFlags` is a STRING array (`["freeleech"]`, not an object); the endpoint is Prowlarr's own `/api/v1/search` spelling (`magnetUrl`, single `protocol` string - not the Radarr `magnetUri`/`downloadProtocols` names). The struct is now the measured wire shape, pinned by a golden-fixture decode test (`server/integrations/prowlarr_test.go`).

## Changed
- The OpenAPI document now carries the v5.2.0 paths and schemas (`WantedItem`, `UnifiedResult`, `UnifiedSourceStatus`); `/api/v1/health` reports `5.2.0`.

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
