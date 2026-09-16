# openbooks:local - PotatoStack patch notes

This directory is the [evan-buss/openbooks](https://github.com/evan-buss/openbooks)
source at tag **v4.5.0** plus the **PotatoStack v5.4.6 patch line**, built as
`openbooks:local` (same pattern as `bookdl:local`). Full changelog and API
docs: `README.md`; machine-readable API spec: `server/openapi.json`, served
at `GET /openapi.json`.

## v5.4.6 (2026-09-16) - the audiobook rows parse too

v5.4.5 took a le-guin search from 138 parse errors to 17, and 14 of those 17 were
audiobooks: the bots report those as "(M4B)", and m4b/mp3/... live in
`audioFormats`, not in `fileTypes` - which is where the parenthesised-format
lookup stopped. `parenFormat` checks both lists now. The two remaining lines are
`.opf` metadata files, which are not books. Measured after the change: 983 books
and 3 errors on the same query (v5.4.4: 862 and 138).

## v5.4.5 (2026-09-16) - one result line in seven now parses

A live `le guin` search returned 862 books and **138 parse errors**. Those lines
are not junk - they are the bots' other output shapes, and until now they were
dropped from every search:

    !Ashurbanipal aeEcHkB1cpn6xAUQKhfedg - Jorge Luis Borges & Anthony Kerrigan - Ficciones [eng]  (AZW3) 419.4 KB - [Fiction, Short Stories (Single Author)].
    !Bsk LeGuin, Ursula K. - El mundo de Rocannon.PDF ::INFO:: 429.59KB.
    !Horla Le Guin & Ursula K. - Winter's King (v1.0).RTF.

Three separate causes:

- **The extension search was case sensitive.** `fileTypes` holds lowercase
  names and the parser compared bytes, so `.PDF`, `.RTF` and `.EPUB` all ended
  in "unable to parse title". `indexExtCI` compares ASCII case-insensitively
  (only ASCII - `strings.ToLower` can change byte length, which would invalidate
  the offsets the caller slices with).
- **Some bots do not print a dotted extension at all** - they put the format in
  parentheses before the size (`(AZW3)`). Those lines now take the parenthesised
  format as the title terminator, and a trailing `[eng]` language marker is
  dropped from the title.
- **Some bots prefix the DCC file hash** (`!Ashurbanipal <hash> - Author -
  Title`). The hash was read as the author and the author as part of the title.
  `stripLeadingHash` removes it when it is hash-shaped (20-28 base64 characters
  with a digit - narrow enough that a single-token author like `Tolkien` or a
  13-character name stays put), while `Full` is still rebuilt from the ORIGINAL
  line so the hash the bot expects survives in the download request.

Sizes on lines without a ` ::INFO:: ` block are picked up too (`419.4 KB`), but
only from a match that sits AFTER the title end, because `Full` is what the
Download button sends and a cut inside the title would produce a broken request.

## v5.4.4 (2026-09-16) - the REST session survives the UI holding the nick

The v5.4.1 nick fallback was not enough, and the way it failed was measurable.

When the colliding session has the SAME user@host - which is exactly what this
stack does to itself: the UI's websocket session and the REST /api/v1 session
are two connections built from one configured nickname - irchighway answers

    433 * potatobooks :Nickname is already in use.

and then CLOSES the socket immediately (0.1s, measured from the gluetun netns
with two connections carrying identical USER/NICK lines; the earlier belief that
it waits out its ~10s registration timeout only holds for a colliding session
with a different user@host). v5.4.1 renamed the candidate on that dying socket,
so the next read returned EOF and the REST path answered

    502 {"error":"api IRC connect: EOF"}

while the UI searched happily with the nickname it had won. The same-name case
is the everyday one here, so REST search - the documented automation path - was
broken whenever a browser tab was open.

- `Join` now dials again under the next candidate when the socket dies
  mid-registration (bounded by `maxNickAttempts`), instead of only renaming on
  the current one.
- `nickCandidates` suffixes are RANDOM (`potatobooks_7fq2`), not the
  deterministic `_`, `__`, `___` ladder: our own sessions picked the same
  fallback nick as predictably as the base one, so the second session lost the
  same race every time and the third could exhaust the ladder.
- New tests: `TestJoinReconnectsWhenTheServerClosesAfter433` reproduces the
  measured server behaviour against a stub that refuses and hangs up, and
  `TestNickCandidatesShape` pins the random-suffix contract.

## v5.4.3 (2026-09-16) - UI downloads are tracked, and the watchlist is clickable

Two gaps that only showed up while driving the real UI.

### A Download clicked in the UI left no trace

The Download button in a search result runs over the WEBSOCKET session, so it
never touched the v5.3.0 job log - `GET /api/v1/jobs` said `[]` right after a
successful download (`CLIENT (potatobooks): Sending book entitled '...'` in the
log, the file in the library) and the Jobs view stayed at "No download jobs
yet.". Jobs only ever contained `POST /api/v1/download` requests.

- `apiState.addJobLocked(book, source, withSidecar)` is now the single place a
  job row is built (the API handler used to inline it), and `DownloadJob`
  carries `source` ("api" or "ui").
- `recordUIDownload` opens the row from the websocket request path.
- `completeUIDownload` closes it when the DCC transfer lands, called from
  `bookResultHandler` for browser clients only.

`completeUIDownload` is deliberately NOT `recordAPIDownload`: that one also
drains the completion-callback FIFO, which belongs to the api session's
REQUEST ORDER, so a UI completion consuming it would fire some API caller's
webhook for the wrong book. Only `source=ui` rows are touched, and
`server/uidownload_test.go` pins both halves.

### "Stop watching" was only reachable with a mouse

The watchlist card hid the delete action in a Mantine context menu. That menu
opens on a real pointer sequence, so a scripted/headless click cannot reach it
(measured: synthetic `click()`, dispatched `PointerEvent`s and the CDP input
path all leave it closed or time out), and two clicks for the one destructive
action was poor anyway. The card now carries a visible trash `ActionIcon` next
to it, calling the same mutation; the menu item stays.

## v5.4.2 (2026-09-16) - the failures now say what went wrong

Two gaps left over from the v5.4.1 session, both about a service that is
up but not working.

### The 502 body was empty

`performSearch` returned a ZERO `APISearchResponse` when the api IRC session
could not be established, and the handler writes `resp.Note` into the error
body - so a dead session, a 433 and a broken tunnel all produced the same
`502 {"error":""}`. The reason existed only in the container log, which is
exactly what made the v5.4.1 hunt slow. `performSearch` (and
`sendSearchNow`, which gained a third return value and now says "rate
limited, retry after Ns" / "search already in flight, retry later" instead
of a blanket "search not sent") now carry the cause; because the plain
search handler, the unified IRC leg (`st.Note`) and `/torznab` all render
`resp.Note`, one fix covers every surface.

### The container had no healthcheck - and could not have one

The runtime is distroless: no shell, no curl, no wget, so a
`healthcheck:` had nothing to run. It has one now, implemented in the
binary itself:

```
openbooks healthcheck [--url http://127.0.0.1:80/api/v1/health] [--timeout 5s]
```

Exit 0 only on HTTP 200; the token comes from `--token` or
`OPENBOOKS_TOKEN`, like the server. `compose.media.yml` runs it every 30s
with a 15s start period, and the image is labelled with its source commit.

**It checks HTTP liveness only, never `ircConnected`.** The api session is
established lazily and rebuilt after the reader sees a dead connection, so
it is legitimately false right after a restart; a healthcheck that failed
on it would have the stack's autoheal restart a healthy container and drop
the session it was rebuilding. Session health is an ALERTING concern
instead (see `scripts/openbooks/openbooks-metrics.sh` and the
`OpenbooksIrcSessionDown` rule in `config/prometheus/alerts/`).

For the same reason the metrics path is unchanged in the image: the api
exporter on the host scrapes `GET /api/v1/metrics` with the token and
publishes it through node-exporter's textfile collector, so the
VictoriaMetrics side gets `openbooks_up`, `openbooks_irc_connected`,
`openbooks_searches_total`, `openbooks_http_requests_total{status=...}` and
friends without a new port, a new secret on another machine, or an
unauthenticated endpoint.

## v5.4.1 (2026-09-16) - IRC registration nick fallback (the 502 cure)

Measured live on potatostack: every `POST /api/v1/search` answered
**502 `unable to connect to IRC server`** (`{"error":""}`) after ~10s, the
log showed `SERVER: api IRC connect: EOF`, and `GET /api/v1/health`
reported `ircConnected: false` - while the web UI happily said "Welcome,
connection established" and `GET /stats` listed a live client. The
container was up, the UI was up, the search was dead: the
"healthy-but-broken" shape.

### What it actually was

`GET /stats` listed one websocket client holding the nick `potatobooks`.
A hand-registered probe from gluetun's netns proved the server was not
throttling and the VPN exit was not banned:

```
NICK potatobooks  -> :srv 433 * potatobooks :Nickname is already in use.
                     ERROR :Closing link: [...] [Registration timeout]
NICK potatobooks2 -> :srv 001 potatobooks2 :Welcome to the irchighway IRC Network
```

**The nickname is a network-global resource, and this stack collides with
itself.** The web UI opens a websocket IRC session on every page load, and
the shared `/api/v1` session - the one the UI's own searches, the DAGs and
the wanted poller use - is a second connection built from the SAME
`server.config.UserName`. Whoever registers second gets `433`, upstream's
`core.Join` never handles it, and the server closes the socket at its
registration timeout. Callers saw only `read: EOF`.

### The patch

`core/irchighway.go` - the registration loop classifies the numerics it
reads while waiting for `001`:

- `432` (erroneous nickname) / `433` (already in use) / `436` (collision):
  retry with the next candidate from `nickCandidates` - the configured
  nick, then `_`, `__`, `___`, `____` (`maxNickAttempts = 5`) - and keep
  waiting for `001`.
- PING is still answered on the way; the 20s deadline and the
  "join anyway" fallback are unchanged.

`irc/irc.go` - new `Conn.ChangeNick(nick)` sends `NICK` and records the
new name on the connection, so the UI's connection detail, the IRC log
file name and `GET /stats` show the nickname the server actually gave us
instead of the one we asked for. It is written while `Join` still owns the
connection exclusively (no reader goroutine, not yet in the hub), so the
`Username` readers never race.

Tests (`core/irchighway_test.go`, loopback only): the candidate list, the
numeric classification, and a real `Join` against a stub that answers
`433` on the first `NICK` and `001` on the second, asserting the client
ends up as `potatobooks_` and sends `JOIN #ebooks`.

### Note for the next reader

A `433` during registration does NOT mean the network is down, and the
server's close is not a throttling ban. Naming the failure "unable to
connect to IRC server" cost a session; the raw numeric is in the socket
and only `--log`/a hand probe shows it.

### Integration peers addressed by name, and a Prowlarr budget that fits

Same session, two follow-on fixes found while testing the UI end to end:

- `compose.media.yml` (potatostack repo): the `OPENBOOKS_*_URL` defaults were
  bridge IPs, stale by then (prowlarr .7 -> .95, audiobookshelf .71 -> .76,
  calibre-web .35 -> .70), so the unified search's Prowlarr leg answered
  "connection refused". The v5.1.0 note claimed docker names do not resolve
  from gluetun's netns; measured 2026-09-16 they do
  (`docker exec gluetun busybox nslookup prowlarr` -> 172.22.0.95, and
  `prowlarr:9696` / `audiobookshelf:80` / `calibre-web:8083` / `dagu:8080`
  all answered HTTP 200 by name from that netns). Defaults are now service
  names; `GET /api/v1/integrations?probe=1` reports `ok` for all three.
- `server/integrations/config.go`, `server/integrations/http.go`,
  `server/unified.go`: the peer budget was 30s in three places. Prowlarr's
  own `/api/v1/search?type=book` round measured **29.3s** (1238 releases),
  so the unified Prowlarr leg raced the deadline and reported `context
  deadline exceeded` on roughly every other identical query. All three are
  60s now; `POST /api/v1/search/unified` returns IRC 259 + Prowlarr 1198
  hits in ~55s, and the UI's `+ Prowlarr` toggle renders 1457 rows.

## v5.4.0 (2026-09-03) - web UI line (the acquisition API gets a face)

The v5.2.0/v5.3.0 acquisition API was built and tested over REST but the web
app still only drove the v4.5 surface (one-shot IRC search + the v1
library). This line wires the web app to the v5.x API so the operator can
drive the acquisition layer without a curl. No server change - it is a
consumer of the existing `openapi.json` contract.

- **Sidebar is now four views.** History (one-shot search results + the
  search-cache strip), **Wanted** (the watchlist), **Jobs** (the download
  log), Library (previous downloads). The `sidebar-state` localStorage key
  survives the tab-set change; a saved value that is no longer a tab falls
  back to History.
- **Search page: the unified multi-source search** (v5.2.0 API). A
  "+ Prowlarr" toggle: off = the existing one-shot IRC websocket search
  (untouched); on = `POST /api/v1/search/unified` (IRC + Prowlarr in one
  REST call) with per-source status chips (ok / not-configured /
  rate-limited / busy / error, hit counts) and a `UnifiedTable` of the
  normalized results: the IRC leg's rows carry a Download button (the
  `!`-prefixed BookID through the shared session), the Prowlarr leg's rows
  a Magnet button (the operator's torrent client), and `dedupGroup > 1`
  marks the same book surfaced by several sources.
- **Wanted view** (`server/app/src/components/sidebar/Wanted.tsx`). A form
  to add a watchlist entry (`POST /api/v1/wanted`) with `autoFetch` and
  `withSidecar` switches plus the v5.3.0 **quality filters** (a "Filters"
  expander: formats comma-list, language, max size MB, prefer
  ebook/audiobook - sent as the `QualityFilters` body and persisted on the
  entry so poll rounds re-search the same shape), and a one-shot Search
  button (the existing IRC search, kept distinct from "watch" because the
  two have very different lifetimes). Each entry is a card with a status
  badge - `matched` (green), `stale` (yellow), `watching` (brand) - and a
  details menu showing added/matched dates, the next poll time (backoff),
  attempts, consecutive no-match rounds, releases seen, auto-fetch/sidecar
  flags, and the persisted quality filters (human-readable, e.g. "epub,
  pdf · german · ≤ 50 MB · ebook"). Delete = `DELETE /api/v1/wanted/{query}`.
- **Jobs view** (`server/app/src/components/sidebar/Jobs.tsx`). Lists the
  download job log (`GET /api/v1/jobs`, newest first, 30 shown, 15 s poll).
  Each job is a card with a status badge (requested/downloading/completed/
  failed) and a menu with the timeline, retries, sha256 (copy-to-clipboard),
  a **Re-request** button for failed/completed jobs
  (`POST /api/v1/jobs/{id}/retry`), and a **Verify sha256** button for
  completed jobs with a file name (`POST /api/v1/verify` with
  `recompute=true` - the result toast reports ok/mismatch/missing and the
  hash).
- **History view: the search-cache strip** (v5.3.0 API) - `entries / hits /
  misses / ttl` (30 s poll) with a clean button
  (`POST /api/v1/search-cache/clean`); the cache is off by default, and the
  strip reports `ttl 0s` plainly instead of implying caching is on.
- **`state/api.ts` extended** with the v5.x RTK Query endpoints and types
  (`WantedItem`, `DownloadJob`, `QualityFilters`, `SearchCacheStats`,
  `UnifiedResult`, `UnifiedSourceStatus`, `UnifiedSearchResponse`,
  `VerifyResponse`): `getWanted`/`addWanted`/`deleteWanted`, `getJobs`/
  `retryJob`/`verifyBook`, `getSearchCache`/`cleanSearchCache`,
  `unifiedSearch`. The `wanted`/`jobs`/`searchCache` tags invalidate on
  their respective mutations.
- Frontend gates: `tsc && vite build` exit 0; the Docker image's web stage
  builds the new views. No Go test surface changed (the drift test still
  pins the v5.3.0 paths; no new REST paths were added).
- Version: `5.4.0` (`cmd/openbooks/main.go`, `server/openapi.json` info.version,
  and the pinned version strings in `server/integrations_test.go` and
  `server/api_test.go` - the version is pinned in FOUR files, the drift
  test alone does not catch the boundary test's pin).

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
