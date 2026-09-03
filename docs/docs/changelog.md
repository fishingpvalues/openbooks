# [v5.3.0] - 2026-09-03

## Added
- **Scoped bearer tokens.** `OPENBOOKS_SCOPED_TOKENS` (semicolon-separated `token:scope,scope` entries) adds secondary tokens that authenticate the same four credential forms (Bearer, `X-OpenBooks-Token`, `?token=`, `?apikey=*** but only for the scopes they carry: `ui` (library reads + feeds), `search` (search, the wanted watchlist, completion polling), `newznab` (the `/torznab` indexer surface), `admin` (downloads, verify, jobs, cache, settings, integrations, metrics, library deletes); `all` is the catch-all. The primary `OPENBOOKS_TOKEN` keeps full privilege, so existing deploys are unchanged; a scope miss is a **403** (right identity, wrong privilege) as opposed to the 401 of a missing/invalid token. Misconfiguration (unknown scope, duplicate token) fails at startup. Config parse: `server/scopes.go`; auth: `server/auth.go`; routes: scope groups in `server/api.go`.
- **Search-result cache.** `POST /api/v1/search` now sits behind an in-memory TTL cache: an exact hit (same query, same quality filters - the filters are part of the cache key) inside the TTL is served with zero IRC traffic and carries `note: "cached"`. `OPENBOOKS_SEARCH_CACHE_TTL` (Go duration; default 24h; 0 disables) and a 4096-entry bound (oldest evicted). `GET /api/v1/search-cache` reports `entries/hits/misses/ttl` + a bounded key sample; `POST /api/v1/search-cache/clean` empties it (counters preserved). A failed search is never stored, so a dead round never poisons the entry for the TTL.
- **Quality-control filters.** `POST /api/v1/search` and `POST /api/v1/search/unified` accept `{filters: {formats?, language?, maxSizeBytes?, prefer?}}` (the `QualityFilters` struct, persisted on wanted entries). An explicit `formats` list is a demand (unknown/absent format fails it), `language` is a substring on the lowercased title, `maxSizeBytes` drops larger releases (size-unknown passes), and `prefer: "ebook"|"audiobook"` drops the other class (unclassified passes). Empty filters = the v5.2 behavior. The filters apply to the normalized result set for every source - the IRC bot and the Prowlarr search API cannot both take them server-side, so the contract is identical across legs.
- **Cross-source result dedupe.** `POST /api/v1/search/unified` results now carry `dedupGroup`: the number of results sharing the same dedupe key (ISBN when the source exposes one, else lowercased `title|author`), so a book that hits IRC and Prowlarr shows as one group of 2 instead of two unrelated hits. The wanted poller additionally records every release it has matched (`seenReleases`) and never re-offers one.
- **Per-job download tracking + retry.** Every `POST /api/v1/download` is now tracked as a job: `{id, book, status, fileName, sha256, completedAt}`, in-memory, bounded to the 256 most recent. `GET /api/v1/jobs` lists them; `POST /api/v1/jobs/{id}/retry` re-sends a failed job's command to the shared IRC session (409 on a non-failed job - re-fetching a completed file is the verify path, not a retry).
- **sha256 verification.** `POST /api/v1/verify` `{file, jobId?, recompute?}` compares a landed file's sha256 to the hash recorded at completion: `status: ok|mismatch|missing`; without a `jobId` the computed hash is recorded as a baseline job for a later verify. `core.ParseSizeToBytes` also gained whitespace tolerance ("900 MB" parses, not just the IRC leg's spaceless "900MB") so the size cap applies to non-IRC sources too.
- **Wanted-entry lifecycle (failed-release handling).** Wanted entries gained `filters` (the poller re-searches the same shape), `withSidecar` (adoption item 10: once the audiobook match is handled, the ebook class of the same round's results is fetched), `attempts` / `failedRounds` with a per-entry backoff (5m base, doubling, 24h cap - a no-match title is re-checked daily, not abandoned), `lastAttemptAt` / `nextAttemptAt`, `seenReleases` (a release that already matched is never re-matched), and `staleSince` (set once an entry goes 7 days without a match; still polled - staleness is an operator signal, not a lifecycle state). Poll rounds are cache-first: an exact hit inside the TTL costs zero IRC traffic.
- Metrics: `openbooks_search_cache_hits_total`, `openbooks_search_cache_misses_total`, `openbooks_wanted_failed_rounds_total`.
- v5.3.0 test coverage: the 403/401 scope contract, the cache (TTL/key/clean/disabled), the filters, the dedupe annotation, the cache-first wanted poll round, the wanted backoff, and the scoped-token parser (`server/v53_test.go`).

## Changed
- The OpenAPI document now carries the v5.3.0 paths (`/api/v1/jobs`, `/api/v1/jobs/{id}/retry`, `/api/v1/verify`, `/api/v1/search-cache`, `/api/v1/search-cache/clean`); `/api/v1/health` reports `5.3.0`.
- `server/auth.go` is split: `matchToken` returns the matched token's scope set (constant-time compare of the primary token first, then the scoped table); `tokenMatches` is the credential-known bool the torznab tests probe with.

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
