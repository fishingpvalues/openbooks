# openbooks:local - PotatoStack patch notes

This directory is the [evan-buss/openbooks](https://github.com/evan-buss/openbooks)
source at tag **v4.5.0** plus the **PotatoStack v5.3.0 patch line**, built as
`openbooks:local` (same pattern as `bookdl:local`). Full changelog and API
docs: `README.md`; machine-readable API spec: `server/openapi.json`, served
at `GET /openapi.json`.

## v5.3.0 (2026-09-03) - hardening + observability line (research-driven)

Follow-up to the v5.2.0 feature-matrix audit: the remaining adoptable
items that are downloader-shaped (not catalogue-shaped) - scoped
authentication, result caching, quality filters, cross-source dedupe,
per-job download tracking, verification, and the wanted entry's
failed-release lifecycle:

- **Scoped bearer tokens.** `OPENBOOKS_SCOPED_TOKENS` adds secondary
  tokens with a scope set (`ui`/`search`/`newznab`/`admin`, `;`-separated
  `token:scope,scope`). The primary `OPENBOOKS_TOKEN` keeps full
  privilege (existing deploys unchanged); a scope miss is a 403 (the
  token is known, the privilege is not) vs the 401 of a missing/invalid
  token. Scope groups in `registerRoutes`: `ui` = library reads +
  feeds/Atom/OPDS; `search` = search/unified/wanted/downloads; `admin`
  = download/verify/jobs/cache-clean/settings/integrations/metrics +
  legacy library delete (moved out of the ui group). The indexer
  surface (`/torznab`) is `newznab`-scoped: a scoped indexer apikey
  cannot search or download. Parse is fail-loud at startup
  (`server/scopes.go`), auth is constant-time (`server/auth.go`).
- **Search-result cache.** `POST /api/v1/search` is now cache-first:
  exact hit (query + filters, the filters are part of the key) inside
  the TTL is served with zero IRC traffic, `note: "cached"`.
  `OPENBOOKS_SEARCH_CACHE_TTL` (**off by default**, 0/unset = pre-v5.3; set e.g. 24h to enable, sub-1m clamps to 1m), 4096-entry bound.
  `GET/POST /api/v1/search-cache[/clean]` for stats + empty. The wanted
  poller is cache-first too: a re-search that hits the TTL spends no
  channel budget.
- **Quality filters.** `{formats?, language?, maxSizeBytes?, prefer?}`
  on `/search` and `/search/unified`; persisted on wanted entries so a
  poll round re-searches the SAME shape (a match that drifts format or
  language is a different book). Applied to the normalized result set
  for every source - the IRC bot cannot take filters server-side and the
  Prowlarr search API has no size/format params, so the contract is
  identical across legs. `prefer: "ebook"|"audiobook"` is the
  sidecar's gate.
- **Cross-source dedupe.** `/search/unified` results carry `dedupGroup`
  (same ISBN, else `title|author`, across sources): one book hitting IRC
  and Prowlarr is a group of 2. `ParseSizeToBytes` strips whitespace
  before the suffix match - "900 MB" (Prowlarr spelling) parses now,
  where only the IRC leg's spaceless "900MB" did before.
- **Per-job download tracking + retry.** `POST /download` jobs are
  tracked in-memory (256 cap): `GET /api/v1/jobs`, `POST
  /api/v1/jobs/{id}/retry` (409 on non-failed). sha256 at completion;
  `POST /api/v1/verify {file, jobId?}` is `ok|mismatch|missing` (baseline
  job when no `jobId`).
- **Wanted lifecycle.** Entries carry `attempts`/`failedRounds` with
  per-entry backoff (5m base, doubling, 24h cap), `staleSince` at 7
  days no-match (still polled), `seenReleases` (a release that already
  matched is never re-matched - Readarr-style failed-release handling at
  the entry level), and `withSidecar` (ReadMeABook, adoption item 10:
  once the audiobook match is handled, the ebook class of the same
  round's results is fetched).
- Metrics: `openbooks_search_cache_{hits,misses}_total`,
  `openbooks_wanted_failed_rounds_total`.
- Version: `5.3.0` (`cmd/openbooks/main.go`, `server/openapi.json` - now
  21 paths; the drift test pins the five new ones).

## v5.2.0 (2026-09-02) - acquisition layer (research-driven)

A feature-matrix audit of the comparable self-hosted book projects
(`docs/openbooks/feature-matrix.md` in the potatostack repo, 2026-09-02:
bookdl, calibre-web, Komga, Kavita, Booksonic, Shelfmark, ReadMeABook,
Mylar3, COPS, CWA, Grimmory) ranked the adoptable gaps for a
downloader-shaped service. v5.2.0 implements the downloader-relevant
ones (it deliberately does NOT adopt the catalogue-shaped ones: no
metadata DB, no web reader, no Subsonic - the matrix says those belong
to the server, not the fetcher):

- **Persistent Wanted watchlist.** `POST /api/v1/wanted`
  `{query, author?, autoFetch?}` (201; duplicate 409, case-insensitive),
  `GET /api/v1/wanted`, `DELETE /api/v1/wanted/{query}` (URL-escaped
  segment; 204/404). A background poller re-searches ONE entry per tick
  through the shared `performSearch` path (single-flight rule and the
  10s rate limit apply; the poller never bypasses them) until the entry
  matches; `autoFetch: true` entries fetch the first match through the
  same `core.DownloadBook` path as a manual download, so completions
  land in `/api/v1/downloads` and fire the callback webhooks in order.
  State: in-memory + `<downloadDir>/wanted.json` snapshot (persist mode
  only; follows the runtime download dir). Interval:
  `OPENBOOKS_WANTED_POLL_INTERVAL` (Go duration; default 5m, floor 1m,
  0 = poller off). This is the biggest gap vs Mylar3/Shelfmark/
  ReadMeABook - they all have exactly this lifecycle.
- **Atom feed.** `GET /api/v1/feeds/atom` - Atom 0.3, library contents
  (persist mode, newest first) + recent completions, 100-entry cap,
  absolute token-carrying entry links. No downloader-shaped comparable
  project has an RSS/Atom feed; it is the zero-polling integration
  point.
- **OPDS 1.0 catalog.** `GET /opds[?search=term]` - the library tree as
  an OPDS acquisition feed (500 cap, hidden/.temp excluded, per-ext
  MIME on the links). `?search=` is the case-insensitive title filter
  (the OPDS contract calibre-web/COPS/Kavita use). Entry links are
  absolute with `?token=*** - OPDS clients cannot set Authorization
  headers on content fetches (same exposure model as the Newznab `<api>`
  element). 404 when persist is off.
- **Unified multi-source search.** `POST /api/v1/search/unified`
  `{query, sources?}` - IRC + Prowlarr in one request, normalized
  results, per-source status (`ok`/`not-configured`/`rate-limited`/
  `busy`/`bad-gateway`/`error`). One dead source never hides the
  others' results; the IRC leg's 429/409 is a per-source status, not a
  failed request.
- **Prowlarr wire shape (caught live).** The first live run of the unified
  search failed the Prowlarr leg's decode: `age` is an INT (whole days),
  `indexerFlags` is a string array (`["freeleech"]`), and the endpoint is
  Prowlarr's own `/api/v1/search` spelling - `magnetUrl` + a single
  `protocol` string, not the Radarr `magnetUri`/`downloadProtocols`
  names. `BookSearchResult` is now the measured wire shape (verified
  against 646 live records) and golden-fixture tested
  (`server/integrations/prowlarr_test.go`).
- **Metrics.** `openbooks_wanted_entries`, `openbooks_wanted_unmatched`
  (gauges); `openbooks_unified_searches_total`,
  `openbooks_wanted_matches_total`, `openbooks_wanted_autofetches_total`
  (counters).
- Version: `5.2.0` (`cmd/openbooks/main.go`, `server/openapi.json` - now
  16 paths / 14 schemas, the drift tests).

## v5.1.2 (2026-09-02) - liveness + observability

- **The api IRC session self-heals.** `startAPIClient` (`server/api.go`)
  now passes a death hook to `core.StartReader` (new `onDeath` parameter,
  `core/reader.go`): when the api session's connection drops - a gluetun
  netns flap, an irchighway server drop - `reapSession` flips
  `state.connected` and the next search re-establishes the session.
  Before this the flag stayed `true` over the dead conn: the first search
  after a drop burned the full 120s wait and every later search hit the
  dead conn, while every HTTP healthcheck still passed. Same failure shape
  as the v4.5.0 join race, in steady state - and invisible from the
  outside, which is why it sat unnoticed.
- **`GET /api/v1/health` reports `ircConnected`.** Process alive (the
  HTTP probe) and session up (the IRC connection) are now two signals,
  because they diverge: the process starts before any session exists, and
  the session can die under a live process.
- **`GET /api/v1/downloads`** - the completions the api session has
  recorded (`{name, completedAt}`, oldest last). The polling path for
  `POST /api/v1/download` callers without a `callbackUrl`; before this,
  learning that a file landed required diffing `GET /api/v1/library`.
  404 with a JSON error when persist mode is off.
- **`GET /api/v1/metrics`** - Prometheus text format: `openbooks_up`,
  `openbooks_version`, `openbooks_irc_connected`,
  `openbooks_irc_sessions_total`, `openbooks_searches_total`,
  `openbooks_downloads_total`, `openbooks_download_errors_total`,
  `openbooks_downloads_completed`, `openbooks_callback_queue_size`,
  `openbooks_http_requests_total{status}`. Hand-rolled (no client_golang):
  the consumer is the standard prometheus text parser.
- **CI** (`.github/workflows/ci.yml`): `go vet` + `go test` on
  push/PR to `integrated`/`master`, plus a docker image build on push.
  The tag-based release workflows did not gate the line between tags.
- Version: `5.2.0` (`cmd/openbooks/main.go`, `server/openapi.json`,
  the drift tests).

## v5.1.1 (2026-09-02) - API hardening

- **Retry-After on the 429 rate limit.** `POST /api/v1/search` (both wait
  paths) and `GET /torznab` now return a `Retry-After` header with the
  seconds to wait when rate-limited. Indexer tools (Prowlarr/Readarr) poll
  the Newznab endpoint on a timer; before this they had to parse the
  message body to learn the interval. `performSearch` / `sendSearchNow`
  return the remaining seconds; the header is set in the handlers.
  Documented on both 429 responses in `server/openapi.json`.
- **Download validation before the IRC session.** `POST /api/v1/download`
  validates `callbackUrl` (absolute http(s) URL) before `startAPIClient`,
  so a malformed request is a 400 without a connection attempt. The
  previous order (IRC first) meant a bad callback cost a failed IRC
  connect and read as a 502.
- **v5 delete name policy aligned with the legacy handler.**
  `DELETE /api/v1/library/{name}` now rejects dotfiles and backslashes via
  the shared `validBookName` check, the same policy the listing and the
  legacy `DELETE /library/{name}` enforce. A dotfile is not a book the
  API ever exposes.
- **Test coverage for every /api/v1 handler.** New `server/api_test.go`:
  health contract, public-route boundaries (SPA + openapi.json open,
  everything else token-gated), the 401 contract (JSON body +
  WWW-Authenticate), the v5 library list/file/delete handlers (including
  traversal and dotfile regressions), search + download request
  validation, `safeJoin`, the webhook retry-once path, and Retry-After on
  the 429 (REST and torznab).

## v5.1.0 patch line (2026-09-01) - integration layer

Upstream openbooks has no integration surface: it cannot call the stack's
services nor be called by them. v5.1.0 adds both. All peer config comes from
`OPENBOOKS_*` env (empty = disabled), so a standalone `openbooks server`
still works with no env at all.

**Inbound** - openbooks as a *book indexer* for the *arr stack:
- `GET <basepath>torznab` - Newznab endpoint (`t=caps`, `t=search`,
  `t=book`). Prowlarr and Readarr can now add openbooks as a book indexer
  the standard way (`?apikey=*** The caps document advertises the token
  via the standard `<api key=...>` element. Search reuses
  `performSearch` (`server/api.go`), so `/torznab` and `POST /api/v1/search`
  share one IRC session, one rate limit, and the single in-flight rule.
- `requireToken` / `tokenMatches` (`server/auth.go`) now accept the Newznab
  `?apikey=*** query form alongside Bearer, `X-OpenBooks-Token`, and
  `?token=*** constant-time compare, same as the other forms.
- `POST /api/v1/download` accepts `callbackUrl`: when the book lands over
  DCC, openbooks POSTs `{status,book,file}` there. FIFO queue in
  `apiState.downloadCallbacks`, drained in `recordAPIDownload` (the api
  IRC session handles book DCC transfers in request order); one retry,
  dead webhooks are logged and dropped (no queue poisoning). Closes the
  arr -> openbooks download-completion loop.

**Outbound** - `server/integrations/` package, one client per peer, all
behind one `Bundle` built in `server.New()`:
- **Prowlarr** (`prowlarr.go`): `SearchBooks` (`GET /api/v1/search`,
  `type=book` - Prowlarr is the indexer HUB, not a Torznab index:
  `/api/torznab` is 404), `ListIndexers` (`GET /api/v1/indexer`).
  `X-Api-Key`.
- **Audiobookshelf** (`audiobookshelf.go`): `ListLibraries`,
  `ScanLibrary`, `SearchLibraryItems`. Auth is `Authorization: Bearer ***
  (NOT `x-api-key` - that returns 401; measured 2026-09-01).
- **Calibre-Web** (`calibreweb.go`): `RootFeed`, `SearchOPDS`. CWA has no
  REST API - OPDS atom feeds only (`/opds/`, `?searchTerm=`).
- **Readarr** (`readarr.go`): `Lookup` (`GET /api/v1/book/lookup?title=`),
  `HasBook` (`GET /api/v1/book`). Dormant on potatostack (Readarr not
  deployed; `OPENBOOKS_READARR_URL` empty by default).
- 30s per-call timeout + 8 MiB response cap in the shared `http.go`
  plumbing; a misbehaving peer cannot hang or OOM a handler.

**Observability** - `GET /api/v1/integrations` (token-gated): which peers
are configured + `?probe=1` live reachability. Turns the bridge-IP
fragility into something observable: after a peer container recreate moves
its bridge IP, the probe for that peer reports unreachable until the
`OPENBOOKS_*_URL` env is updated (see
`docs/openbooks/stack-state-2026-09-01.md` section 6 for the measured
addresses and why only bridge IPs work from the gluetun netns).

**Tests:** `server/integrations_test.go` - Torznab caps/auth/search paths
(under the real `requireToken` middleware), Newznab item XML round-trip,
library download-URL derivation, integrations overview (disabled + live
httptest peers), all four client contracts against httptest servers shaped
like the live services, webhook FIFO pairing + dead-sink give-up.

## v5.0.0 patch line (2026-09-01)

On top of the original join-race patch below:

- **Auth:** `server/auth.go` - bearer token (`OPENBOOKS_TOKEN` or `--token`)
  guards every route except the static SPA and `GET /openapi.json`. Upstream
  had none: the `OpenBooks` cookie was a client UUID, `/stats`, `/servers`
  and the library endpoints were open to any tailnet peer (gluetun publishes
  port 8083). Constant-time compare; token accepted via
  `Authorization: Bearer`, `X-OpenBooks-Token` header or `?token=***
  No token set = upstream single-user behavior.
- **REST API:** `server/api.go` - `GET /api/v1/health`, `GET|DELETE
  /api/v1/library[/{name...}]`, `POST /api/v1/search` (waits for parsed
  results by default, 120s cap), `POST /api/v1/download` (async DCC request).
  Search + download run in a server-owned IRC session (reserved uuid
  `00000000-...-0001`) registered in the same clients map; `serveWs`'s
  single-browser-connection rule excludes it, so UI and API coexist.
- **OpenAPI:** `server/openapi.json` embedded (`//go:embed`, blank
  `import _ "embed"` - the directive alone does not mark the import used,
  types2 only counts `embed.X` references; this cost a whole debug session).
- **Fixes:** `GET /library/*` subfolders + traversal guard (`safeJoin`);
  `DELETE /library/{name}` arbitrary-file-deletion fix (chi routes on raw
  percent-encoding; one segment only + `filepath.Rel` containment); archive
  extraction hardened in `util/archiver.go` (entry path-traversal guard +
  5GiB size cap - archiver/v3 + rardecode advisories GO-2024-2698,
  GO-2025-3605, GO-2025-4020 have NO fixed release, so mitigated in code);
  `irc/irc.go` TLS verification restored; `GET /stats` nil-conn guard for
  the api client; `--bind` flag (default 127.0.0.1; image passes 0.0.0.0).
- **Toolchain/deps:** go1.26.6 (16 reachable stdlib advisories from 1.26.0
  cleared); `server/app` stray `npm@^12` runtime dep removed (5 vulnerable
  npm-CLI sub-packages); Dockerfile non-root distroless + pinned bases +
  `npm ci`; release workflows updated (go ^1.26.6, node 24).
- **Frontend:** token in `localStorage["openbooks-token"]`, sent on REST +
  WS; `/api/v1/health` probe gates the token prompt (no-token servers skip).

Deploy config: `.env` `OPENBOOKS_TOKEN` (64-char hex, `openssl rand -hex 32`),
compose `OPENBOOKS_TOKEN` passthrough, container command adds `--persist`
`--bind 0.0.0.0`. Reach over `tailscale serve` only (rail: no 0.0.0.0
publish on the host - the container bind is inside gluetun's namespace).

## v4.5.0 base patch: IRC join race

Upstream v4.5.0 joins the IRC channel after a **fixed 2 second sleep** after
connect (`core/Join`). irchighway reverse-DNSes the connecting IP during
registration; for a datacenter/VPN egress IP (no PTR record) that lookup
**times out** (~5s+), so the JOIN is sent *before registration completes* and
the server answers:

```
451 potatobooks JOIN :You have not registered
451 potatobooks PRIVMSG :You have not registered
```

The client never retries, so it sits connected-but-not-joined and **every
search silently fails** while the UI looks fine. From a residential IP the PTR
lookup fails fast (NXDOMAIN), which is why most upstream users never see this.

### The patch

`core/irchighway.go` - `Join()` no longer sleeps 2s; it reads the connection
byte-by-byte (no buffering, so the reader started after `Join()` still sees
every remaining line) until the **001 welcome** (the definitive "you are
registered" signal), answering PINGs on the way, with a 20s fallback that joins
anyway. This fixes the race for any egress (VPN or not).

## Rebuilding

```bash
docker compose --project-directory /opt/potatostack build openbooks
docker compose --project-directory /opt/potatostack up -d openbooks
```

The build needs network (npm install for the web UI, go modules).

## Reverting to upstream

Switch the `openbooks` service image back to `evanbuss/openbooks:${OPENBOOKS_TAG}`
and restore `OPENBOOKS_TAG` / the `# renovate: image=evanbuss/openbooks` comment
in `.env.example` (only worth it once upstream fixes the join race AND auth).
