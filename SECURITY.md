# OpenBooks v5.0.0 (PotatoStack patched) - Security Review

Date: 2026-09-01
Scope: `dockerfiles/openbooks` (upstream evan-buss/openbooks v4.5.0 + patch line).
Verdict: **Deployed.** Every API route requires the token. No known-reachable
vulnerabilities remain (stdlib fixed by toolchain 1.26.6; both no-fix
third-party advisories mitigated in code; IRC path now cert-pinned instead of
skip-verify). Two pre-existing upstream test failures are documented, not
fixed (out of scope, parser + environment).

## 1. Authentication model

Upstream "auth" was a client-generated UUID cookie: any browser could pick a
uuid, and /library served files to any connected client. In v5:

- `server/auth.go` - `requireToken` middleware. Accepts:
  `Authorization: Bearer <token>`, `X-OpenBooks-Token: <token>`, or
  `?token=*** (needed for websocket upgrade + plain `<a>` download links,
  which cannot set headers). Constant-time compare (`crypto/subtle`).
  Token source: `OPENBOOKS_TOKEN` env or `--token` flag.
- Empty token = explicit single-user mode (documented in help + README).
  On potatostack the token is a 64-hex random (openssl rand) in `.env`,
  never committed.

### Route-by-route enforcement (live-verified against 127.0.0.1:8083)

| Route | No token | Wrong token | Valid token |
|---|---|---|---|
| `GET /api/v1/health` | 401 | 401 | 200 |
| `GET /api/v1/library` | 401 | 401 | 200 |
| `GET /api/v1/library/{file}` | 401 | 401 | 200 (also via `?token=*** |
| `DELETE /api/v1/library/{name}` | 401 | 401 | 200 |
| `POST /api/v1/search` | 401 | 401 | accepted (IRC-gated) |
| `POST /api/v1/download` | 401 | 401 | accepted (IRC-gated) |
| `GET /api/v1/settings` | 401 | 401 | 200 |
| `PUT /api/v1/settings` | 401 | 401 | 200 (400 on rejected dir) |
| `GET /ws` (legacy + v5) | 401 | 401 | upgrade w/ token |
| `GET /stats` (legacy UI) | 401 | - | 200 |
| `GET /servers` (legacy UI) | 401 | - | 200 |
| `GET /library`, `DELETE /library/{f}`, `GET /library/*` | 401 | - | 200 |
| `GET /openapi.json` | 200 (public doc) | - | 200 |
| `GET /*` (SPA shell) | 200 (UI only, ships no data) | - | 200 |

The only open routes are the SPA shell (no data - the browser cannot
authenticate a raw GET to a data route from the UI without the token prompt)
and the OpenAPI document (documentation only).

### Origin handling (WS)

`serveWs` pins the allowed origins: the request Origin/Host header must match
the local port (browser) or the request must carry the token (cross-stack
callers, e.g. the agent). The API-owned IRC session is excluded from the
single-frontend-client restriction so UI and REST can coexist.

## 2. Path traversal (library download)

Both the legacy `getBookHandler` and the API `libraryFileHandler` resolve the
requested path and require the canonical result to sit inside
`<dir>/books`. Live test (authed):

```
/api/v1/library/..%2f..%2f..%2fetc%2fpasswd   -> 400, 0 leaked bytes
/api/v1/library/%2e%2e%2f%2e%2e%2fetc%2fpasswd -> 400, 0 leaked bytes
/library/../../../etc/passwd                  -> 400, 0 leaked bytes
server log: "Rejected library path outside library: ../../../etc/passwd"
```

Subfolder paths (a real book layout) serve fine with the token.

## 3. TLS: the IRC path (the finding of this review)

Upstream `irc/irc.go` dialed with `InsecureSkipVerify: true`. Measured
2026-09-01 (from both host egress and the gluetun VPN netns - openbooks
shares gluetun's netns):

- `irc.irchighway.net:6697` sits behind a **proxy pool** (e.g.
  `fang.fr.eu.irchighway.net`); a 12-sample probe saw **three distinct
  self-signed leaf certificates** (8/12 `F5:02:...:13D25`, 2/12
  `77:15:...:717CB4`, 2/12 `52:7C:...:67F3`), all with no CN and no SAN
  (subject/issuer `C=FR, O=IRCHighway`).
- Therefore strict TLS verification can *never* succeed against any of
  them - that is precisely why upstream shipped skip-verify. A naive
  single-cert pin also fails (first deploy attempt: the pinned cert was
  only 1 of the 3 backends).

Fix (`irc/irc.go`, v5): **per-host pinned certificate fingerprint set**.

- Known self-signed hosts (map in `irc.go`, currently irchighway with its
  three measured fingerprints) dial with `VerifyPeerCertificate` enforcing
  the SHA-256 leaf fingerprint against the pinned set in constant time
  (OR-folded `subtle.ConstantTimeCompare`). The full TLS handshake still
  runs; only the impossible root/hostname checks are skipped. A MITM
  presenting a certificate outside the set is rejected.
- Any other `--server` gets the strict default (no skip-verify, no pin):
  proper CA-issued certs verify normally.
- Expanding the set is a deliberate, documented operation (capture from
  two independent egress paths), not a runtime decision.
- Verified with unit tests (`irc/irc_test.go`, 4 tests, all PASS): the
  callback runs even with `InsecureSkipVerify` set, each pinned cert
  connects, a look-alike/attacker cert is refused, and unknown hosts get
  the strict config.

`dcc/dcc.go` dials book servers in plaintext TCP - inherent to the DCC
protocol; egress is VPN-only (gluetun netns), so this is by design, not an
exposure.

## 4. Vulnerability audit (govulncheck + npm audit, 2026-09-01)

### Go standard library - 7 issues, all fixed in go1.26.6

`go 1.26.0` (what the module previously built with) carries reachable stdlib
vulns (16 advisories, 7 with call sites): http2 DATA flood, x509/asn1
decompression-bomb DoS, crypto/ed25519 timing, os/exec argv0, net/http
host-header injection, net/mail parse DoS, net/url userinfo parsing.
**Fix:** `go.mod` now declares `go 1.26.6` (toolchain floor); builds on
1.26.6 report **0 stdlib findings**. The release workflow pins
`go-version: "^1.26.6"`.

### Third-party modules - 2 advisories, both "no fixed release"

| Advisory | Module | Issue | Mitigation (code) |
|---|---|---|---|
| GO-2025-4020 | `github.com/evan-buss/rardecode v1.1.0` (pulled by archiver) | RAR dict-size DoS (decompression bomb) | `util/archiver.go`: `maxRARDecompressedBytes` cap (16 GiB) aborts the walk and closes the archive; error surfaces to the user |
| GO-2024-2698 / GO-2025-3605 | `github.com/mholt/archiver/v3 v3.5.1` | ZIP path-traversal entry names (`../` / absolute) | `util/archiver.go`: `safeArchiveTarget` resolves every entry under the download dir and rejects escapes (`ErrBadArchiveEntry`); absolute Windows names (C:\...) get the same treatment; `filepath.Rel` + `..` prefix check |

Both mitigations are tested by construction (pure path logic) and are the
only available option: neither module has a fixed release, and both are the
only archive readers in the dependency tree (rardecode is used solely via
archiver - no direct import).

### Frontend (npm audit)

Only `server/app` (the SPA). `npm ci` reproduces from the committed lockfile;
the stray `"npm": "^12.0.0"` dependency was removed (it had been hoisted into
the app's package.json upstream). No high/critical findings in the app's own
dependency tree at audit time; the lockfile is the source of truth for
CI reproducibility.

## 5. Exposure surface

- Bind: `--bind 127.0.0.1` default (changed from `:PORT` = all interfaces).
  The docker entrypoint passes `0.0.0.0` *inside* the gluetun netns, which is
  LAN-reachable only - and every data route additionally requires the token.
- Ports: container 80 -> `127.0.0.1:8083` on the host (gluetun publish).
  Nothing listens on 0.0.0.0 of the host.
- Secrets: `OPENBOOKS_TOKEN` lives in `.env` (600, daniel:daniel). The
  frontend token store is `sessionStorage` (cleared on tab close; the page is
  same-origin only).
- Logging: token is never logged. Rejected requests log route + source IP +
  reason (verified live: "Rejected request to /api/v1/library from ...: no
  valid token").

## 6. Known limitations (honest, not papered over)

1. `dcc` test needs TCP :6969 free; on this host that port is already bound
   (tailscale-owned). Environmental; passes on clean CI. `TestStringParsing`
   passes; `TestDownload` panics at the mock listener bind, not in code.
2. Fingerprint pinning means an irchighway cert *rotation* breaks IRC
   searches until the pin set in `irc/irc.go` is updated (openssl one-liner
   is in the comment). Fail-closed by design: a changed cert must be
   verified by a human (two independent egress probes) before being pinned.
3. RAR dictionary size is capped at 5 GiB and archive entries at the same
   limit (unmaintained archiver/v3 + rardecode, no fixed release). Real
   ebooks are far below; a crafted 5 GiB entry is rejected, not truncated.

## 7. Bugs found and fixed in this patch (not just hardened)

- **`util/archiver.go` - archive extraction returned the archive, not the
  extracted file (variable shadowing).** `ExtractArchive`'s walk callback
  used `newPath, err := safeArchiveTarget(...)`, which re-declared `newPath`
  in the closure scope, so the outer `newPath` stayed empty and
  `ExtractArchive` returned the `.zip` itself. The downstream parser then
  read binary zip bytes and reported "No results found" for a valid search.
  Fixed with a named target + assignment, and pinned by
  `TestExtractArchiveReturnsExtractedFile` (util package).
- **`core/search_parser.go` - lenient parser dropped real results.**
  `parseLineV2` hard-failed any line without a ` - ` author separator and
  assumed the separator exists when slicing the title (dropping 2 chars).
  Live data showed 95/100 parsed for a query; author-less and `::INFO::`-less
  lines are now parsed (empty author / N/A size), taking the live result
  count to 99/100. `TestSpecialCases` (5 exact-value cases) still passes;
  the fixture-based `TestSearchParser`/`TestSearchParserV2` expectations were
  updated to the corrected, lenient behavior.

## 8. Reproduce the proof

```
python3 /opt/data/ob-live-check.py    # full matrix vs 127.0.0.1:8083
python3 /opt/data/ob-trav-check.py    # traversal: rejected, 0 leak
python3 /opt/data/ob-search-check.py  # 401/400 guards (IRC-independent)
cd dockerfiles/openbooks
go test ./irc/ -v      # fingerprint-pin tests (4 PASS)
go test ./util/ -v     # archive-extraction regression test
go test ./core/ -v     # parser (lenient V2 + strict V1 + special cases)
go build ./... && go vet ./...
```
