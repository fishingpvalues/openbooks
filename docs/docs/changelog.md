# [v5.4.4] - 2026-09-16

## Fixed
- **The REST session survives the web UI holding the configured nickname.** v5.4.1 assumed a refused nickname could be answered with an in-place `NICK` on the same socket. Measured against the live server on 2026-09-16: when the colliding session has the same user@host - this stack's own UI websocket session against its REST session, the everyday case - irchighway sends `433 * <nick>` and closes the socket in ~0.1s. The rename went nowhere, the next read returned EOF, and every REST search answered `502 {"error":"api IRC connect: EOF"}` while the UI was fine. `Join` now dials again under the next candidate when the socket dies mid-registration.
- The candidate nicknames after the first are random-suffixed (`potatobooks_7fq2`) instead of the deterministic `_`/`__`/`___` ladder, which our own sessions selected just as predictably as the base nick.

## Changed
- `GET /api/v1/health` reports `5.4.4`; `server/openapi.json` info.version is `5.4.4`.

# [v5.4.3] - 2026-09-16

## Fixed
- **A download requested from the web UI is now tracked.** The Download button in a result row runs over the websocket session, so it never reached the v5.3.0 job log: `GET /api/v1/jobs` returned `[]` right after a successful UI download and the Jobs view said "No download jobs yet." while the file landed in the library. `DownloadJob` gained `source` ("api" | "ui"), `apiState.addJobLocked` is now the single constructor for a job row (shared by both request paths), `recordUIDownload` opens the UI row and `completeUIDownload` closes it from the websocket completion hook. The completion is deliberately separate from `recordAPIDownload`, which drains the api session's completion-callback FIFO in request order - a UI completion must not consume an API caller's callback.

## Added
- **A visible "stop watching" control on each watchlist card.** It was only in the card's context menu, which opens on a real pointer sequence (unreachable for a scripted or headless click) and hid the one destructive action two clicks deep. The card now has a trash `ActionIcon` calling the same mutation; the menu item remains.

## Changed
- `GET /api/v1/health` reports `5.4.3`; `server/openapi.json` info.version is `5.4.3` (the drift test pins the version string; the path set is unchanged).

# [v5.4.2] - 2026-09-16

## Fixed
- **A failing search says why.** `performSearch` returned a zero response when the api IRC session could not be established, so the handler wrote the empty note into the body: every connect failure was `502 {"error":""}` while the reason sat in the container log. It now returns the error as the note, and `sendSearchNow` (fire-and-forget path) gained a third return value so it reports "rate limited, retry after Ns" / "search already in flight, retry later" / the connect error instead of a blanket "search not sent". One fix covers `/api/v1/search`, `/api/v1/search/unified` (the IRC leg's `note`) and `/torznab`, which all render that note.

## Added
- **`openbooks healthcheck`** - the probe the container's compose healthcheck runs. The runtime image is distroless (no shell, no curl, no wget), so the binary is the only thing available inside the container: it GETs the local `/api/v1/health` with the configured token and exits 0 only on HTTP 200 (`--url`, `--timeout`; token from `--token`/`OPENBOOKS_TOKEN`). It checks HTTP liveness only - **never `ircConnected`**, which is legitimately false right after a restart and briefly false after a VPN bounce, so failing on it would make autoheal restart a healthy container. Session health is alerted on instead (`openbooks_irc_connected`, exported by `scripts/openbooks/openbooks-metrics.sh`).
- `server/healthcheck.go` (+ `server/healthcheck_test.go`) and `cmd/openbooks/healthcheck.go`.

## Changed
- `GET /api/v1/health` reports `5.4.2`; `server/openapi.json` info.version is `5.4.2` (the drift test pins the version string; the path set is unchanged - no new REST paths).

# [v5.4.1] - 2026-09-16

## Fixed
- **IRC registration survives a nickname that is already taken.** upstream (and every v5.x line so far) sends one `NICK` and then waits for the `001` welcome; when the server answers `433 * <nick> :Nickname is already in use` instead, nothing retries. irchighway then closes the socket at its registration timeout, so the caller saw a bare `EOF`: `SERVER: api IRC connect: EOF` and every REST search answered **502 `unable to connect to IRC server`** while the web UI still reported "Welcome, connection established". This stack collides with itself systematically - the web UI opens a websocket IRC session on every page load and the shared `/api/v1` session (which the UI's own searches, the DAGs and the wanted poller use) is built from the SAME configured nick - so the failure was the default state whenever the UI was open.
  - `core/irchighway.go`: the registration loop now handles `432`/`433`/`436` by retrying with the next nickname from `nickCandidates` (the configured nick, then underscore-suffixed variants, bounded by `maxNickAttempts`) and keeps waiting for `001`; the fallback deadline is unchanged.
  - `irc/irc.go`: new `Conn.ChangeNick` sends `NICK` and records the name on the connection, so the UI detail, the IRC log file name and `GET /stats` show the nickname the server actually assigned.
  - Tests: `core/irchighway_test.go` covers the candidate list, the numeric classification, and drives the real `Join` against a loopback stub that answers `433` first and `001` second.
- **The unified search's Prowlarr leg stopped racing its own 30s budget.** Prowlarr's `/api/v1/search?type=book` round measured 29.3s, and the same 30s appeared in `server/integrations/config.go` (`defaultTimeout`), `server/integrations/http.go` (`httpTimeout`) and the per-leg context in `server/unified.go`, so about every other identical query ended in `context deadline exceeded` instead of results. All three are 60s now (IRC 259 + Prowlarr 1198 hits for "the hobbit"). The peer base URLs moved from stale bridge IPs to Docker service names in the deploy config (they resolve from the gluetun netns).

## Changed
- `GET /api/v1/health` reports `5.4.1`; `server/openapi.json` info.version is `5.4.1` (the drift test pins the version string; the path set is unchanged - no new REST paths).

# [v5.4.0] - 2026-09-03

## Added
- **The web UI line.** The web app now drives the v5.2/v5.3 acquisition API instead of only the v4.5 surface (one-shot IRC search + the v1 library). The sidebar is four views: History (one-shot search results + the search-cache strip), **Wanted** (the watchlist), **Jobs** (the download log), Library (previous downloads).
  - **Search page: the unified multi-source search** (v5.2.0 API). A "+ Prowlarr" toggle: off = the existing one-shot IRC websocket search (untouched); on = `POST /api/v1/search/unified` (IRC + Prowlarr in one REST call) with per-source status chips (ok / not-configured / rate-limited / busy / error, hit counts) and a `UnifiedTable` of the normalized results: the IRC leg's rows carry a Download button (the `!`-prefixed BookID through the shared session), the Prowlarr leg's rows a Magnet button (the operator's torrent client), and `dedupGroup > 1` marks the same book surfaced by several sources.
  - **Wanted view** (`server/app/src/components/sidebar/Wanted.tsx`): a form to add a watchlist entry (`POST /api/v1/wanted`) with `autoFetch` and `withSidecar` switches plus the v5.3.0 **quality filters** (a "Filters" expander: formats comma-list, language, max size MB, prefer ebook/audiobook - sent as the `QualityFilters` body and persisted on the entry so poll rounds re-search the same shape), plus a one-shot Search button (the existing IRC search - kept distinct from "watch" because the two paths have very different lifetimes). Each entry is a card with a status badge (`matched` green / `stale` yellow / `watching` brand) and a details menu: added/matched dates, next poll time (backoff), attempts, consecutive no-match rounds, releases seen, auto-fetch/sidecar flags, and the persisted quality filters (human-readable). Delete = `DELETE /api/v1/wanted/{query}`.
  - **Jobs view** (`server/app/src/components/sidebar/Jobs.tsx`): the download job log (`GET /api/v1/jobs`, newest first, 30 shown, 15 s poll). Each job is a card with a status badge (requested/downloading/completed/failed) and a menu: timeline, retries, sha256 with copy-to-clipboard, a **Re-request** button for failed/completed jobs (`POST /api/v1/jobs/{id}/retry`), and a **Verify sha256** button for completed jobs with a file name (`POST /api/v1/verify` with `recompute=true`).
  - **History view: the search-cache strip** (v5.3.0 API) - `entries / hits / misses / ttl` (30 s poll) with a clean button (`POST /api/v1/search-cache/clean`); the cache is off by default and the strip reports `ttl 0s` plainly.
  - `state/api.ts` gained the v5.x RTK Query endpoints + types (`WantedItem`, `DownloadJob`, `QualityFilters`, `SearchCacheStats`, `UnifiedResult`, `UnifiedSourceStatus`, `VerifyResponse`): `getWanted`/`addWanted`/`deleteWanted`, `getJobs`/`retryJob`/`verifyBook`, `getSearchCache`/`cleanSearchCache`, `unifiedSearch`. No server change - the web app is a consumer of the existing `openapi.json` contract; the token gate is unchanged.

## Changed
- `GET /api/v1/health` reports `5.4.0`; `server/openapi.json` info.version is `5.4.0` (the drift test pins the new version string; the path set is unchanged - no new REST paths).

# [v5.3.0] - 2026-09-03

## Added
- **Scoped bearer tokens.** `OPENBOOKS_SCOPED_TOKENS` (semicolon-separated `token:scope,scope` entries) adds secondary tokens that authenticate the same four credential forms (Bearer, `X-OpenBooks-Token`, `?token=`, `?apikey=*** but only for the scopes they carry: `ui` (library reads + feeds), `search` (search, the wanted watchlist, completion polling), `newznab` (the `/torznab` indexer surface), `admin` (downloads, verify, jobs, cache, settings, integrations, metrics, library deletes); `all` is the catch-all. The primary `OPENBOOKS_TOKEN` keeps full privilege, so existing deploys are unchanged; a scope miss is a **403** (right identity, wrong privilege) as opposed to the 401 of a missing/invalid token. Misconfiguration (unknown scope, duplicate token) fails at startup. Config parse: `server/scopes.go`; auth: `server/auth.go`; routes: scope groups in `server/api.go`.
- **Search-result cache.** `POST /api/v1/search` now sits behind an in-memory TTL cache: an exact hit (same query, same quality filters - the filters are part of the cache key) inside the TTL is served with zero IRC traffic and carries `note: "cached"`. `OPENBOOKS_SEARCH_CACHE_TTL` (Go duration; **off by default** - unset/0 keeps the pre-v5.3 always-live behavior; set e.g. `24h` to enable, sub-1m values clamp to 1m) and a 4096-entry bound (oldest evicted). `GET /api/v1/search-cache` reports `entries/hits/misses/ttl` + a bounded key sample; `POST /api/v1/search-cache/clean` empties it (counters preserved). A failed search is never stored, so a dead round never poisons the entry for the TTL.
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
