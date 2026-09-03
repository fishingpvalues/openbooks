# openbooks

openbooks is an IRC/DCC ebook downloader: it searches the #bookz channel on
irc.irchighway.net via the channel search bot and downloads matched books
over DCC transfers. A React web UI is served from the same process.

This tree is the PotatoStack patched line, version 5.4.0, built as the
openbooks:local image. Upstream base: evan-buss/openbooks at v4.5.0. The
patch line adds:

- static-token authentication for the whole HTTP surface
- a REST API under /api/v1 backed by a server-owned IRC session
- security fixes (path traversal in library download/delete, archive
  extraction hardening, IRC TLS certificate pinning, IRC join race,
  loopback bind default)
- the v5.1.0 integration layer (inbound Newznab /torznab book-indexer
  endpoint, outbound clients for Prowlarr/Audiobookshelf/Calibre-Web/
  Readarr, download-completion webhook, /api/v1/integrations overview)
- the v5.1.2 endpoints (GET /api/v1/downloads completions, GET
  /api/v1/metrics Prometheus text format, ircConnected on the health
  probe) and the api IRC session's self-healing re-establishment after
  a connection drop
- the v5.2.0 acquisition layer: the persistent Wanted watchlist
  (POST/GET/DELETE /api/v1/wanted with a re-search poller and
  auto-fetch), the Atom feed of library activity (GET
  /api/v1/feeds/atom), the OPDS 1.0 catalog of the local library (GET
  /opds with ?search=), and the unified multi-source search (POST
  /api/v1/search/unified over IRC + Prowlarr with per-source status)
- the v5.3.0 hardening + observability line: scoped bearer tokens
  (OPENBOOKS_SCOPED_TOKENS), the search-result TTL cache (stats +
  clean), quality filters (format/language/maxSize/prefer) on search
  + wanted, cross-source result dedupe (dedupGroup), per-job download
  tracking + retry (GET /api/v1/jobs, POST /api/v1/jobs/{id}/retry),
  sha256 verification (POST /api/v1/verify), and the wanted entry's
  failed-release lifecycle (backoff, staleSince, seenReleases,
  withSidecar)
- the v5.4.0 web UI line: the web app exposes the v5.2/v5.3 API
  (Wanted watchlist with the lifecycle state, the download job log
  with retry) as first-class views; the sidebar is History / Wanted /
  Jobs / Library
- runtime-mutable settings (GET/PUT /api/v1/settings)
- an embedded OpenAPI document at GET /openapi.json
- Go test suites
- a hardened Docker image

## Features

- Search #bookz on irc.irchighway.net through the IRC search bot
- Download books over DCC with automatic archive extraction
- React web UI served by the same process
- Static-token auth across the HTTP surface (constant-time compare)
- REST API under /api/v1 on a server-owned IRC session
- Library listing, download and delete with traversal protection
- Newznab /torznab book-indexer endpoint (Prowlarr/Readarr)
- Outbound clients: Prowlarr, Audiobookshelf, Calibre-Web, Readarr
- Download-completion webhook (static and per-request)
- Runtime-mutable settings (download directory, persist flag)
- Persistent Wanted watchlist with re-search poller (auto-fetch)
- Atom feed of library activity (GET /api/v1/feeds/atom)
- OPDS 1.0 catalog of the local library (GET /opds, ?search=)
- Unified multi-source search (POST /api/v1/search/unified)
- Embedded OpenAPI 3.x document at /openapi.json
- go 1.26.6 toolchain, hardened distroless Docker image
- Book-completion polling (GET /api/v1/downloads) and Prometheus metrics (GET /api/v1/metrics)
- The api IRC session re-establishes itself after a connection drop

## Running

### Binary

`openbooks --help` lists all flags. Two modes exist: CLI (terminal
interface) and server (web application). Obtain the binary from the
upstream releases page or build from source (see Development).

Server mode:

    ./openbooks server --name yournick --token SECRET

With a token set, every route except the static SPA and /openapi.json
requires it. Without a token the server runs in single-user mode (all
routes open, upstream behavior).

### Docker

    docker run -d --name openbooks -p 8080:80 \
        -v /path/to/books:/books \
        -e OPENBOOKS_TOKEN=SECRET \
        -e OPENBOOKS_PERSIST=true \
        openbooks:local

Image layout: multi-stage build (node:24-alpine frontend with npm ci,
golang:1.26.6-alpine build stage, gcr.io/distroless/static:nonroot runtime
running as uid 1000:1000). EXPOSE 80, VOLUME /books, entrypoint:

    ["./openbooks","server","--dir","/books","--port","80","--bind","0.0.0.0"]

OPENBOOKS_TOKEN is read from the environment. There is no HEALTHCHECK by
design: distroless has no shell or curl; container alerts come from
elsewhere.

### Base path

For reverse-proxy setups, --basepath (flag) or BASE_PATH (env) sets the
mount point. The value must include leading and trailing slashes
(default /):

    ./openbooks server --basepath /openbooks/
    docker run -p 8080:80 -e BASE_PATH=/openbooks/ openbooks:local

## Configuration

### Flags (openbooks server)

| Flag | Default | Description |
|------|---------|-------------|
| --port, -p | 5228 | HTTP listen port |
| --dir, -d | $TMPDIR/openbooks | download directory |
| --persist, -P | false | keep downloaded files on disk |
| --rate-limit, -r | 10 | seconds between searches (min 10) |
| --bind | 127.0.0.1 | bind address (the image passes 0.0.0.0) |
| --basepath | / | base path, including leading/trailing / |
| --name, -n | - | IRC nick (required) |
| --server, -s | irc.irchighway.net:6697 | IRC server |
| --tls | true | use TLS for the IRC connection |
| --searchbot | search | name of the IRC search bot |
| --useragent, -u | OpenBooks <version> | user agent string |
| --token | - | API token |
| --browser, -b | - | browser for desktop mode |
| --log, -l | false | enable logging |
| --no-browser-downloads | false | disable browser downloads |

### Environment variables

Short names are accepted too; an explicitly set flag always wins.

| Variable | Description |
|----------|-------------|
| OPENBOOKS_PORT / PORT | HTTP listen port |
| OPENBOOKS_DIR / DOWNLOAD_DIR | download directory |
| OPENBOOKS_PERSIST / PERSIST | true/false |
| OPENBOOKS_RATE_LIMIT / RATE_LIMIT | seconds between searches |
| OPENBOOKS_USER_AGENT / USER_AGENT | user agent string |
| OPENBOOKS_TOKEN | API token |
| BASE_PATH | base path |

Integration env vars (empty = that peer disabled; standalone mode works
with none set):

| Variable | Description |
|----------|-------------|
| OPENBOOKS_PROWLARR_URL, OPENBOOKS_PROWLARR_API_KEY | Prowlarr |
| OPENBOOKS_AUDIOBOOKSHELF_URL, OPENBOOKS_AUDIOBOOKSHELF_API_KEY | Audiobookshelf; auth is Authorization: Bearer, not x-api-key |
| OPENBOOKS_CALIBREWEB_URL | Calibre-Web; OPDS only, no key |
| OPENBOOKS_READARR_URL, OPENBOOKS_READARR_API_KEY | Readarr |
| OPENBOOKS_DOWNLOAD_CALLBACK | static completion webhook fired for every download; independent of the per-request callbackUrl |
| OPENBOOKS_CALLBACK_ALLOWED_HOSTS | comma-separated hostnames allowed as callback targets (see the download-completion webhook below); empty by default |

## REST API

Machine-readable spec: GET /openapi.json (public). Every other route
requires the token (auth forms below).

| Method | Path | Purpose |
|--------|------|---------|
| GET | /api/v1/health | {name, version, persist, ircConnected}; the token probe the UI uses; ircConnected is the shared api IRC session's liveness (self-healing on the next search) |
| GET | /api/v1/library | JSON array of persisted books {name, downloadLink, size, modifiedAt}; 404 when persist is off |
| GET | /api/v1/library/{path...} | download a book file; subfolders allowed; percent-encoded traversal (..%2F) rejected with 400 |
| DELETE | /api/v1/library/{name} | delete one top-level book file; 204 on success |
| POST | /api/v1/search | {"query": "...", "wait": true\|false (default true)}; waits up to 120 s for parsed results |
| POST | /api/v1/download | {"book": "!<identifier from search>", "callbackUrl": "https://..." (optional)}; 200 {status: requested} |
| GET | /api/v1/settings | {downloadDir, persist} |
| PUT | /api/v1/settings | change downloadDir and/or persist without restart; 405 for other methods |
| GET | /api/v1/downloads | JSON array of completions {name, completedAt}, oldest last; 404 when persist is off; the polling path for downloads without a callbackUrl |
| GET | /api/v1/metrics | Prometheus text format (openbooks_up, version, irc_connected, searches, downloads, http status counts) |
| GET | /api/v1/integrations | which peer integrations are configured (prowlarr/audiobookshelf/calibreweb/readarr + downloadCallback); ?probe=1 adds live reachability |
| GET | /torznab | Newznab book-indexer endpoint (t=caps, t=search); auth via ?apikey=<token> |
| GET | /openapi.json | embedded OpenAPI 3.x document (public) |

Legacy browser endpoints (still token-gated, used by the React UI):
GET /ws (websocket), GET /stats, GET /servers, GET /library,
DELETE /library/{name}, GET /library/*.

### Auth forms

The token is set via the OPENBOOKS_TOKEN env var or the --token flag.
Accepted forms:

- Authorization: Bearer <token>
- X-OpenBooks-Token: <token> header
- ?token=<token> query parameter
- ?apikey=<token> (Newznab form)

Comparison is constant-time. No token set = single-user mode, all routes
open (upstream behavior).

### Search and download flow

POST /api/v1/search waits up to 120 s for parsed results when wait is
true. Errors: 429 on the rate limit (default 10 s between searches) and
409 when a search is already in flight, both with the shared session
state; 502 if the IRC connection fails. A 429 carries a Retry-After
header with the seconds to wait, so indexer clients (Prowlarr/Readarr)
can back off without parsing the body.

POST /api/v1/download requests the DCC transfer and returns 200
{status: requested}. The file arrives over DCC; poll GET /api/v1/library
until it appears. Request validation (book identifier, callbackUrl)
runs before the IRC session is opened, so a malformed request is a 400
regardless of IRC availability.

### Download-completion webhook

When callbackUrl is given to POST /api/v1/download, a completion POST
{status, book, file} is delivered to that URL when the file lands. One
retry is attempted; dead sinks are logged and dropped.
OPENBOOKS_DOWNLOAD_CALLBACK sets a static webhook fired for every
download, independent of the per-request callbackUrl.

The callbackUrl is caller-supplied and the server POSTs to it, so it is
SSRF-guarded. The policy:

- link-local, metadata (169.254.169.254), unspecified and multicast
  targets are always denied.
- loopback and private targets are allowed only if the hostname is in
  OPENBOOKS_CALLBACK_ALLOWED_HOSTS.
- public targets are allowed, unless an allowlist is set (in which case
  only allowlisted hosts are).
- userinfo credentials in the URL are rejected; logs carry
  scheme+host+path only.

The check runs at request time (a bad URL is a 400 before the IRC
session opens) and again at DIAL time (net.Dialer.Control), after DNS
resolution, so a hostname that resolves to a public address during
validation cannot resolve to a denied address by the time the POST goes
out. Redirects are not followed, so a 302 cannot walk a permitted host
into a denied address.

### Settings

GET /api/v1/settings returns {downloadDir, persist}. PUT accepts the
same shape and applies changes without restart. The new downloadDir
must be absolute, must not be /, and is probed writable before the
switch. Other methods return 405.

## Newznab / torznab indexer

GET /torznab is an inbound Newznab book-indexer endpoint. Point Prowlarr
or Readarr at it with the apikey as the Newznab index key:

    http://<host>:<port>/torznab?t=caps&apikey=<token>

Authentication uses the Newznab form: ?apikey=<token>.

- t=caps: capability document; advertises the token via the standard
  <api key="..."> element.
- t=search&q=<query>: Newznab XML search results.

The endpoint shares the IRC session, rate limit and single-flight rule
with POST /api/v1/search: a 429 carries Retry-After (the indexer client
backs off on it), a 409 means a search is already in flight, and a 502
means the IRC connection failed.

## Peer integrations

Outbound clients talk to up to four peers, each enabled by its env vars
(empty = disabled):

| Peer | Env vars | Notes |
|------|----------|-------|
| Prowlarr | OPENBOOKS_PROWLARR_URL, OPENBOOKS_PROWLARR_API_KEY | - |
| Audiobookshelf | OPENBOOKS_AUDIOBOOKSHELF_URL, OPENBOOKS_AUDIOBOOKSHELF_API_KEY | auth is Authorization: Bearer, not x-api-key |
| Calibre-Web | OPENBOOKS_CALIBREWEB_URL | OPDS only, no key |
| Readarr | OPENBOOKS_READARR_URL, OPENBOOKS_READARR_API_KEY | - |

GET /api/v1/integrations reports which peers are configured
(prowlarr/audiobookshelf/calibreweb/readarr plus downloadCallback).
?probe=1 adds live per-peer reachability (a transport ping; any HTTP
status counts as reachable) and a functional apiProbe.

## Security

Changes in the patch line relative to upstream v4.5.0:

- Authentication. Upstream had none: the OpenBooks cookie was a
  client-generated UUID, and /stats, /servers and the library were open
  to anyone who could reach the port. With a token set, every route
  except the static SPA and /openapi.json requires it (constant-time
  compare).
- DELETE /library/{name} was arbitrary file deletion: chi routes on raw
  percent-encoding, so ..%2F..%2Fx unescaped to a traversal after routing
  accepted it as one segment. Now: one path segment only, plus a
  containment check.
- The download-completion callbackUrl was an open SSRF relay: the server
  POSTs to a caller-supplied URL, so a token holder could reach every
  service on the bridge, the host loopback and the metadata address.
  It is now allowlist-guarded (OPENBOOKS_CALLBACK_ALLOWED_HOSTS),
  re-checked at dial time after DNS resolution, and the client refuses
  to follow redirects. See the download-completion webhook section.
- GET /library/* now validates subfolder paths with safeJoin;
  percent-encoded traversal is rejected with 400.
- Archive extraction rejects entries escaping the download directory.
  The archiver/v3 and rardecode advisories GO-2024-2698, GO-2025-3605
  and GO-2025-4020 have no fixed release, so the mitigation is in code
  (safeArchiveTarget), with a 5 GiB entry-size cap.
- IRC TLS: upstream dialed with verification disabled. irchighway
  backends serve self-signed certs with no SAN, so their SHA-256
  fingerprints are pinned per host; any other server gets strict
  verification.
- IRC join race: the join no longer sleeps a fixed 2 s after connect;
  it reads until the 001 welcome (the definitive registered signal),
  answering PINGs, with a 20 s fallback. Previously, slow-egress
  connections joined before registration and searches failed with 451
  (You have not registered).
- --bind defaults to 127.0.0.1; upstream bound every interface. The
  Docker image passes 0.0.0.0 explicitly.

## Development

Toolchain: go 1.26.6. Frontend lives in server/app
(React/TypeScript/Redux/Mantine: npm ci, npm run build).

Build:

    ./build.sh                # npm ci, build the React app, compile the binary
    go build                  # binary only, when the frontend is unchanged
    go build -tags webview    # desktop mode

Mock IRC/DCC server for development against a fake IRC:

    cd cmd/mock_server && go run .
    # in another terminal:
    openbooks server --server localhost --log

Tests. Go unit tests in server/: routes_test.go (library handlers,
traversal regressions, the token middleware in all forms, settings),
integrations_test.go (torznab caps/auth/search paths, Newznab XML
round-trip, integrations overview, all four peer client contracts
against httptest servers shaped like the live services, webhook FIFO
and dead-sink give-up, OpenAPI drift) and api_test.go (health, the v5
library handlers, public-route boundaries, the 401 contract,
search/download request validation, safeJoin, Retry-After on the 429),
plus core/, dcc/, irc/, util/ tests.

    go test ./...

## License

MIT (upstream). The patch line described here is PotatoStack's work on
top of evan-buss/openbooks v4.5.0.
