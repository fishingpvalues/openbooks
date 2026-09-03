# Self-hosted ebook project feature research (for openbooks)

> Comparative feature research to inform openbooks roadmap. Researched via SearxNG
> + GitHub API / raw README fetches (the configured `web_search` backend returned
> 404s during the session). Items marked `?` were not documented and were left
> unverified rather than guessed.
>
> **Baseline:** openbooks v5.1.2 (Go; multi-source ebook/audiobook search +
> download; REST + React UI; NewzNab-compatible indexer endpoint; push-outs to
> Audiobookshelf/Calibre-Web/Prowlarr).
>
> **Tooling note:** GitHub search for an *arr-integrated "bookdl" (Python,
> Prowlarr, wanted queue) found no such project. The two real `bookdl` projects
> are the pdf-drive CLI (kaushalpurohit/Bookdl) and the one actually running on
> this stack (billmal071/bookdl). The *arr-adjacent shape the brief describes
> (wanted queue, NZB+torrent, Prowlarr) actually matches **LazyLibrarian**.

## 1. bookdl

The stack's `bookdl` / `bookdl-web` containers are built from
`github.com/billmal071/bookdl` (verified in `dockerfiles/bookdl/Dockerfile`), a
**Go CLI for searching/downloading books from Anna's Archive, Z-Library and
Liber3** — multi-source scraping, not an *arr-integrated Python service. Its
"queue" is a manual multi-select download queue, not a poll-until-found wanted
list.

Sources: https://github.com/billmal071/bookdl (README); local
`dockerfiles/bookdl-web/app.py` (route list).

- **Search:** multi-source (Anna's Archive, Z-Library, Liber3); filters: format
  (EPUB/PDF), language, year or range, max file size, result limit; pagination;
  search-result caching with TTL (24h default) + cache stats/clean.
- **Wanted/auto-download queue:** partial — manual queue only (`-q` multi-select,
  `queue list/clear/remove`). No polling, no re-search on failure, no RSS.
- **Catalog & metadata:** no library DB. Books identified by MD5; no
  series/authors/covers persistence.
- **Feeds:** none (no OPDS/RSS).
- **Integrations:** none upstream of download; the local `bookdl-web` Flask
  wrapper adds a REST API over the CLI: `/api/search`, `/api/download`,
  `/api/downloads` (+ pause/resume), `/api/downloads/retry-failed`, `/api/verify`
  (re-download corrupt), `/api/queue`, `/api/bookmarks`, `/api/cache`,
  `/api/history`, `/api/config`, `/api/files`, `/health`; plus a DDoS-Guard cookie
  refresher (Playwright) for Anna's Archive.
- **Download pipeline:** resumable chunk-based downloads (SQLite-tracked),
  pause/resume/restart, concurrent downloads, automatic mirror fallback,
  headless-browser fallback for Cloudflare/DDoS-Guard, verify with checksum and
  `--fix` re-download.
- **Stats & metrics:** no (history endpoint only).
- **Auth & sharing:** none in CLI; bookdl-web has no auth in the route list
  (sits behind gluetun netns, bound 127.0.0.1:7799).
- **Formats:** whatever the sources serve — EPUB, PDF, etc. via format filter.
- **Distinctive:** three-source fan-out with per-source client abstraction
  (API, scraper, browser); desktop notifications; shell completions.

## 2. Calibre-Web (janeczku/calibre-web)

Web app for browsing, reading and downloading eBooks from a Calibre
`metadata.db` — a pure catalog/serving layer, not a downloader.

Source: https://github.com/janeczku/calibre-web (README).

- **Search:** advanced search and filtering over Calibre fields (author, title,
  tags, series...); no full-text of book contents documented.
- **Wanted/auto-download:** no.
- **Catalog & metadata:** full Calibre library: books, shelves (custom
  collections), metadata editing/deletion, metadata download from multiple
  sources (extensible via plugins), Calibre custom columns.
- **Feeds:** OPDS feed at `/opds`. RSS not documented — treat as no.
- **Integrations:** reads/writes Calibre's sqlite library directly; eBook
  conversion through Calibre binaries; "Send to e-Reader" one-click; Kobo sync
  with the Calibre library; upload new books (incl. audio); Google Drive hosting.
- **Download pipeline:** n/a (downloads *from* the app, restricted to logged-in
  users).
- **Stats & metrics:** none documented.
- **Auth & sharing:** comprehensive — per-user fine-grained permissions, admin
  UI, public registration, LDAP, Google/GitHub OAuth, proxy auth, "Magic Link"
  login (read-only-ish access URL for e-readers), content hiding by
  category/custom-column per user.
- **Formats:** all Calibre formats (epub, mobi, pdf, ...) plus audio for upload;
  in-browser reader for multiple formats.
- **Distinctive:** self-update; 20+ UI languages. The big automation fork is
  **Calibre-Web Automated** (crocodilestick): adds OAuth2/OIDC, KOReader sync
  (KOSync), e-reader device handling, Calibre-core conversion —
  https://github.com/crocodilestick/Calibre-Web-Automated.

## 3. Komga (gotson/komga)

Media server for comics, mangas, BDs, magazines and eBooks; SPA web UI + REST
API + reader integrations. Pure serving layer, no downloading.

Sources: https://github.com/gotson/komga (README);
https://komga.org/docs/guides/opds; https://komga.org/docs/guides/search.

- **Search:** strong — SQLite FTS, case-insensitive, word-order-independent,
  quoted phrases, `AND/OR/NOT`, field queries (`publisher:`, `genre:`, `tag:`,
  `author:`, role-based `writer:`/`penciller:`, `status:`,
  `release_date:[1990 TO 2000]` range queries, `complete:true`), **ISBN matched
  by default for books**, relevance-ordered.
- **Wanted/auto-download:** no.
- **Catalog & metadata:** libraries -> series -> books; collections and read
  lists; manual metadata editing; automatic embedded-metadata import; tags at
  series/book level; genres, publisher, status, age rating, language, reading
  direction, release date.
- **Feeds:** OPDS v1 (1.2) and v2; v1 includes OpenSearch (search by series) and
  OPDS Page Streaming Extension. RSS: not in docs — no.
- **Integrations:** Kobo Sync (incl. sync points), KOReader sync,
  CDisplayEx/Mihon/Panels/Chunky clients, full OpenAPI/REST (per-user API keys),
  import external books into series folders, import ComicRack `.cbl` read lists.
- **Download pipeline:** n/a (client-side downloads of books/series/read lists).
- **Stats & metrics:** reading progress tracking + sync; no server-side metrics
  endpoint documented.
- **Auth & sharing:** multiple users, per-library access control, age/label
  restrictions; API keys per user; OAuth2/OIDC installation path.
- **Formats:** comic archive formats (cbr/cbz/zip/rar) + ebooks (epub/pdf).
- **Distinctive:** duplicate files detection, duplicate page detection and
  removal, and a scan-analysis/OCR refresh pipeline.

## 4. Kavita (Kareadita/Kavita)

Cross-platform reading server for comics/manga and books (C#/.NET + React);
serving + user management + reader UX, no acquisition.

Sources: https://github.com/Kareadita/Kavita (README);
https://wiki.kavitareader.com/guides/features/opds/.

- **Search:** rich metadata search, filtering, "smart filters" (saved queries);
  OPDS feeds expose OpenSearch across collections/series/read lists.
- **Wanted/auto-download:** no — "Want to Read" is a *grouping list*, not a
  fetch queue.
- **Catalog & metadata:** collections, reading lists (CBL import), Want to Read,
  series/volume/file hierarchy; metadata download, reviews, ratings,
  recommendations, scrobbling via the paid Kavita+ tier.
- **Feeds:** OPDS + OPDS-PS (page streaming), enabled by default; per-user unique
  URL + auth key; pagination; OpenSearch; niceties: "Continue From X" alias item
  and reading-progress icons encoded into feed titles (empty/25/50/75/full).
- **Integrations:** REST API (encouraged over OPDS for rich clients), CBL import,
  theme repo; no Calibre sqlite import (scans folders).
- **Download pipeline:** n/a.
- **Stats & metrics:** reading progress (per user, synced into OPDS); no server
  metrics endpoint documented.
- **Auth & sharing:** rich RBAC — roles, age restrictions, per-ability toggles,
  OIDC, per-user OPDS auth keys; full localization (Weblate).
- **Formats:** cbr, cbz, zip/rar/rar5, 7zip, raw images, epub, pdf — notably no
  mobi/azw.
- **Distinctive:** EPUB annotation/highlight support, web readers (webtoon/
  continuous/virtual epub pages), dashboard customization, custom theming.

## 5. Booksonic / Booksonic-Air (popeen)

Audiobook *streaming* server (successor to the discontinued original Booksonic;
built on the Airsonic fork of Subsonic). Not an ebook app and not a downloader.

Source: https://github.com/popeen/Booksonic-Air (README); booksonic.org.

- **Search:** Subsonic API search over the library (via clients); details not in
  README.
- **Wanted/auto-download:** no.
- **Catalog & metadata:** album/artwork/artist hierarchy from ODM sidecar files
  (Booksonic-specific metadata format; ODM2Meta conversion script exists); cover
  art with zoom.
- **Feeds:** no OPDS/RSS documented (Subsonic API is the interface).
- **Integrations:** full Subsonic API compatibility — any Subsonic client works;
  no Calibre import, no server pushes.
- **Download pipeline:** n/a (streams; designed for very large collections;
  on-the-fly transcoding plugins, bitrate capping for constrained bandwidth).
- **Stats & metrics:** none documented.
- **Auth & sharing:** Subsonic user system (users/passwords, remote access).
- **Formats:** MP3-optimized but "any audio or video format that streams over
  HTTP" (AAC, OGG, WMA, FLAC, APE, Musepack, WavPack, Shorten via transcoder
  plugins).
- **Distinctive:** essentially an audiobook Subsonic server; the "OPDS focus" in
  the brief does not match — its integration surface is the Subsonic REST API.

## 6. Other projects found

- **ReadMeABook** (kikootwo) — https://github.com/kikootwo/ReadMeABook: closest
  modern analog to openbooks's shape, for *audiobooks*: request a book ->
  Prowlarr searches (torrents+NZBs) -> qBittorrent or SABnzbd downloads -> files
  organized -> Plex/Audiobookshelf import. Features: "BookDate" AI
  recommendations (OpenAI/Claude/local, swipe UI) that create requests;
  Audible-backed metadata searches; multi-file chapter merging into single M4B
  with chapters; optional e-book sidecar (EPUB/PDF from Shadow Library alongside
  the audiobook); admin approval workflow for multi-user requests; OIDC OAuth;
  setup wizard with connection testing; Postgres + Redis.
- **Readarr** (Readarr/Readarr) — https://github.com/Readarr/Readarr:
  **RETIREMENT ANNOUNCED IN THE README** ("metadata has become unusable...
  project has been retired"). Historically the reference "Sonarr for ebooks":
  monitored RSS feeds of your authors, grabbed/sorted/renamed releases, quality
  upgrades (PDF -> AZW3), automatic failed-download handling (retry another
  release), manual search, profiles, SABnzbd/NZBGet/qBittorrent/Deluge/rTorrent/
  Transmission clients, Calibre integration via Calibre Content Server, API
  docs. Its retirement is a signal, not just a data point.
- **LazyLibrarian** — https://gitlab.com/LazyLibrarian/LazyLibrarian (active;
  GitHub mirror nearly stale): the project the brief's "bookdl" description
  actually matches. Python; author-following book automation: import existing
  Calibre library; find authors via HardCover/OpenLibrary/LibraryThing/Goodreads/
  GoogleBooks; list an author's books and mark ebooks or audiobooks "wanted";
  then searches for NZB, torrent or magnet and sends to SABnzbd/NZBGet/Synology
  (usenet) or Deluge/Transmission/uTorrent/qBittorrent/rTorrent (torrents), or
  blackholes for a client to pick up; on processing saves cover + `metadata.opf`
  (Calibre-compatible) next to the file; AutoAdd for flattened-directory Calibre;
  magazine monitoring for new issues; docs site has dedicated `api/` and `rss/`
  sections (RSS feed and API confirmed to exist; exact semantics unverified).
- **Calibre-Web Automated** (crocodilestick) —
  https://github.com/crocodilestick/Calibre-Web-Automated: Calibre-Web fork
  adding OAuth2/OIDC, KOReader sync, device support, Calibre-core features.
  Catalog side only, like calibre-web.

## Feature matrix

Legend: yes / partial / no / ?. `openbooks` column is the baseline from the
brief (not re-verified this session).

| Feature | openbooks | bookdl(+web) | calibre-web | Komga | Kavita | Booksonic-Air | LazyLibrarian | ReadMeABook | Readarr (retired) |
|---|---|---|---|---|---|---|---|---|---|
| Search: title/author/ISBN | yes (sources) | partial (source search; ISBN via sources) | yes | yes (ISBN by default) | yes | ? | yes (author-centric) | yes (Audible-backed) | yes |
| Search: faceted/advanced | partial (per-source filters) | yes (format/lang/year/size) | yes (fields) | yes (FTS+fields+ops) | yes (smart filters) | ? | ? | ? | yes (profiles) |
| Multi-source fan-out | yes (IRC+NewzNab+Prowlarr) | yes (AA+ZLib+Liber3) | no | no | no | no | yes (NZB/torrent/magnet) | yes (Prowlarr: tor+NZB) | yes (RSS/indexers) |
| Wanted queue (poll until found) | no | partial (manual queue) | no | no | no (list only) | no | yes | yes (requests) | yes (RSS monitor) |
| RSS feed of results/wanted | no | no | no | no | no | no | yes (docs/rss) | no (docs) | yes (monitors RSS) |
| Catalog/metadata persistence | partial (library dir) | no (MD5, SQLite jobs) | yes (Calibre db) | yes | yes | yes (ODM) | yes (sqlite) | yes (Postgres) | yes |
| Covers, series, genres, ratings | ? | no | yes | yes | yes (Kavita+ for reviews/ratings) | yes (ODM) | yes (metadata.opf) | yes | yes |
| OPDS feed | no | no | yes | yes (v1+v2+PSE) | yes (OPDS+PS) | no | ? | no | no |
| RSS/Atom out | no | no | no | no | no | no | yes | no | yes |
| NewzNab-compatible indexer endpoint | yes | no | no | no | no | no | no | no | n/a |
| Push to reader server (AB/CWA etc.) | yes | no | n/a (is the server) | no | no | n/a | partial (Calibre import/AutoAdd) | yes (Plex/AB) | yes (Calibre CS) |
| Trigger library scans | yes | no | n/a | no | no | no | partial (post-import) | yes (waits for scan) | yes |
| Download progress | yes (queue endpoint) | yes (per-job progress) | n/a | n/a | n/a | n/a | ? | ? | yes |
| Retry/restart failed | ? | yes (retry-failed) | n/a | n/a | n/a | n/a | ? | ? | yes (auto, other release) |
| Dedupe | ? | no | no | yes (dup files+pages) | no | no | ? | ? | yes (existing detection) |
| Quality control (format/lang/size/quality) | partial (source choice) | yes (filters) | no | no | no | no | partial (ebook/audiobook wanted flags) | yes (ebook/audiobook) | yes (quality profiles, upgrades) |
| Verify/re-download corrupt | ? | yes (verify --fix) | no | yes (scan analysis) | no | no | ? | ? | partial (failed handling) |
| Stats & metrics | yes (Prometheus) | no | no | progress only | progress only | no | ? | ? | ? |
| Auth: tokens | yes (single token 401 wall) | no | yes (users+OAuth+magic link) | yes (users+API keys+OIDC) | yes (OIDC, per-user keys) | yes (Subsonic users) | yes (single-user web auth) | yes (OIDC) | yes (API keys) |
| Auth: multi-user/roles | no | no | yes | yes | yes (RBAC) | yes | no | yes (approval workflow) | yes |
| Sharing: read-only/magic links | partial (token) | no | yes (magic link) | yes (per-user access) | yes (per-user OPDS key) | yes (remote) | no | no | yes |
| Formats: epub/mobi/pdf | yes | yes (source-dependent) | yes | epub/pdf | epub/pdf | audio | yes | epub (sidecar)/audio | epub/pdf/azw3 |
| Formats: comics | no | no | no | yes | yes | no | no | no | no |
| Formats: audiobooks | yes | no | upload only | no | no | yes | yes | yes (M4B merge) | yes |
| In-browser reader | no (small UI) | no | yes | yes | yes | via clients | no | no | no |
| Distinctive | NewzNab out endpoint, IRC sources | chunked resumable DL, CF/DDoS-Guard browser fallback, TTL search cache | Calibre-native, Kobo sync, conversion | Kobo/KOReader sync, dup-page removal, scan analysis | EPUB annotations, progress-in-OPDS-titles, OIDC | Subsonic API compat, transcode plugins | metadata.opf sidecars, magazine monitoring | chapter merge to M4B, AI recs->requests, e-book sidecar, approval workflow | quality upgrades PDF->AZW3, automatic failed-release retry |

## Adoptable for openbooks

Ranked by (fit to a downloader's shape x value x implementation cost).

1. **Wanted queue with re-search polling (LazyLibrarian/Readarr pattern).**
   Today an openbooks search is one-shot; the single biggest value gap vs *arr
   users is "request once, system fetches when it appears." Minimal impl:
   persist wanted items (query, filters, created_at, status
   `wanted|found|downloading|done|failed|stale`), a scheduled job re-running
   each source's search for open items with backoff (15m -> hourly -> daily,
   drop after N misses = `stale`); `GET /wanted`, `POST /wanted`,
   `POST /wanted/{id}/cancel`. This also turns the existing NewzNab endpoint
   into a loop: Prowlarr results for a wanted title become grab candidates.
2. **Failed-download retry with "try another release" (Readarr).** IRC/DCC and
   usenet releases die; openbooks currently lacks this. On download failure
   keep the wanted item open, mark the specific result failed, re-pick the
   next-ranked result from the last search; expose `POST /downloads/{id}/retry`
   and a `retry_failed` bulk endpoint (bookdl-web's `retry-failed` is a working
   reference).
3. **Per-job download status + progress (bookdl-web).** A stable per-job id with
   `queued|downloading|done|failed` + progress makes the API drivable by other
   tools (dagu steps, scripts). Job id + status enum + last-updated on the
   existing queue endpoint; no new storage needed if state already lives in
   memory/DB.
4. **Quality-control filters as first-class request fields (bookdl + Readarr
   profiles).** Accept `format`, `language`, `max_size_mb`, `prefer`
   (ebook|audiobook) on search+wanted; store them so polling re-searches
   respect them; upgrade = re-search a `done` item when a better-format result
   appears (optional later).
5. **Search-result cache with TTL + per-source attribution (bookdl).** IRC and
   indexer polling are cheap locally but polite-behavior-limited remotely.
   Cache table keyed on (query, source, filters-hash) with TTL; `/search`
   returns cache hits with a `cached` flag; `GET /cache/stats` +
   `POST /cache/clean`.
6. **RSS/Atom feed of completed downloads (LazyLibrarian docs/rss pattern).**
   Lets any consumer (other *arr tools, a hypothetical Readarr revival) hook in
   without scraping the REST API; same shape as the NewzNab endpoint already
   exposed. `GET /feed?token=...` (Atom), items = last N completed downloads
   (title, source, format, path, timestamp), 1-hour cache.
7. **Verification + re-download (bookdl verify --fix).** DCC and NZB delivery
   both produce corrupt/partial files. Checksum at completion;
   `POST /downloads/{id}/verify` returns ok/corrupt; corrupt items feed
   straight into feature 2 (retry).
8. **Result dedupe across sources (Komga's dedupe discipline, applied earlier).**
   The same book hits IRC, a NewzNab indexer and Prowlarr. Normalize results to
   one schema with a `source` field; group by (isbn if present, else
   lowercased title+author); show group count in search results; wanted items
   track "all results in group failed" before re-searching.
9. **Per-token scoping / API keys (Komga per-user API keys; Kavita per-user
   auth keys).** One global token means no different privileges for UI, the
   NewzNab endpoint and a future *arr poller. Multiple tokens in settings, each
   with a scope (`ui`, `search`, `newznab`, `admin`); 403 instead of 401 on
   scope mismatch.
10. **e-book sidecar + approval workflow (ReadMeABook) — later, only if
    multi-user.** RMAB is the only project validating openbooks's exact
    request->search->download->organize->import pipeline. Sidecar = one extra
    wanted filter (`with_ebook: true`); approval = a `pending` status on wanted
    items plus an admin-only endpoint. Adopt only when a second human starts
    using it.

**Strategic note:** Readarr's retirement (announced in its own README) leaves
the "book *arr" slot empty and is the main reason openbooks's shape (search
aggregator + NewzNab endpoint + wanted pipeline) is worth extending in the
direction of items 1-4 rather than staying a one-shot search box. LazyLibrarian
is the maintained proof that the wanted->NZB/torrent->opf+cover pipeline holds
up; ReadMeABook is the proof for the audiobook+Plex/AB side. Neither has
openbooks's IRC source or NewzNab-out endpoint, so no single existing project
covers this ground — the adoption list above is the gap, not a reimplementation.
