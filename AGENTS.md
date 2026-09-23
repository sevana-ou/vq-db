# AGENTS.md

Guidance for coding agents working in this repository. This is the only
AGENTS.md — do not add per-directory ones.

## What this repo is

`vq-db` is the dashboard/persistence component of the Sevana VQ monitor: it
subscribes to `vq-core`'s ZeroMQ bus, persists call-quality data to a
database, and serves a REST API plus an embedded web dashboard:

- `backend-go/` — Go re-implementation of the `vq-db` daemon (module
  `github.com/sevana-ou/vq-db`, binary `vq-db`). A single static, CGo-free binary; it
  replaced the earlier Python (FastAPI) backend, which is gone. The Python
  backend is the **behavioral reference** the port was matched against — its
  reverse-engineering and migration notes live in `docs/`. The dashboard UI
  (server-rendered Go templates + htmx) lives in `backend-go/internal/web`;
  the earlier Flutter Web dashboard it replaced is not part of this repo.
- `docs/` — `vq_db_api.md` is the authoritative REST API reference (endpoints,
  filter grammar, response shapes); read it before touching any API surface.
  `FILTER.md` (filter mini-language), `STATS.md` (`/stats` endpoint), and
  `COMPARISON_TEST.md` (side-by-side validation) are the design/reverse-engineering
  docs.
- `scripts/` — all build & deploy scripts. Backend (Go): `build.py` (dev
  binary), `build_bundle.py` (self-contained bundle), `install_bundle.sh` +
  `run_test.sh` (bash, run on the target/inside the bundle), plus
  `vq-monitor-db-go.service` and `vq-monitor-db-go.cfg.sample`.
  `nginx-beta.conf` is the reverse-proxy config. `README.md` covers install
  paths and the config reference.

## Commands

### Backend (`backend-go/`)

Requires the Go toolchain (`go.mod` pins go 1.25; `GOTOOLCHAIN=auto` fetches it).
Run from `backend-go/`:

```sh
go test ./...                              # run the whole test suite
go vet ./...                               # vet
go build ./cmd/vq-db                       # build the binary (or: ../scripts/build.py)
./vq-db --config /etc/vq-monitor/vq-monitor.cfg    # run the daemon
go run ./cmd/vq-db --config <cfg>          # or run without building
```

The stack is CGo-free: `modernc.org/sqlite` (pure-Go SQLite), pure-Go
`github.com/go-zeromq/zmq4`, and `google.golang.org/protobuf`. Cross-compile
with `GOOS`/`GOARCH` (`CGO_ENABLED=0`) — the binary is the only host-specific
artifact. The protobuf bindings are pre-generated
(`internal/proto/capturemessage.pb.go`); regenerate with
`internal/proto/generate.sh` only if `internal/proto/capturemessage.proto`
changes (needs `protoc` + `protoc-gen-go`).

Each `internal/*` package ports the matching Python test suite, so the two
implementations share one behavioral spec — keep every package tested. The
version string is single-sourced from `backend-go/VERSION` (baked into the
binary via `-ldflags -X main.version`).

Deployment (a single static binary — no venv, no wheels):
- **Bundle (recommended)** — `scripts/build_bundle.py` compiles the binary
  (dashboard embedded via `go:embed`) and packs it with `install.sh` +
  `run_test.sh` + systemd unit + config sample into
  `backend-go/dist/vq-monitor-db-go-<version>-<os>-<arch>.tgz`. On the target,
  `scripts/install_bundle.sh` (shipped as `install.sh`) unpacks it under
  `/opt/vq-monitor-db-go` and registers the `vq-monitor-db-go` systemd unit
  (dashboard port **9146**), running in parallel with any earlier C++/Python
  build on the same vq-core bus ports.
- **In-place test** — unpack a bundle and run its `./run_test.sh`: it
  generates a local `run_test.cfg` with a throwaway SQLite DB under
  `./.run_test/`, and serves on `:9156` (override with `DASH_PORT=`). No
  install, no root. `LOG_LEVEL=`, `SUB_PORT=`/`CTRL_PORT=` override the
  obvious bits; delete `.run_test/` to reset.

### Dashboard UI (`backend-go/internal/web/`)

The dashboard is part of the backend: server-rendered `html/template` pages +
vendored htmx (`static/htmx.min.js`), all embedded with `go:embed` and served
at `/ui/` (`/` redirects there; legacy `/#/route` hash links are mapped
client-side). It reuses the `internal/api` query/serialize layer directly —
change API behavior there, presentation in `internal/web`. `go test
./internal/web/` covers rendering; there is no separate frontend build.

The Flutter app this UI replaced (and its `scripts/frontend_*.py` helpers)
is not part of this repo.

## Backend architecture

Runtime wiring is in `cmd/vq-db/main.go`:

```
zmq4 SUB  ->  ingest.Pipeline.OnBytes  ->  translate.DecodeBytes  ->  db.Writer
 internal/bus   internal/ingest            internal/translate        internal/db
net/http (read API + /track* control proxy + embedded htmx dashboard at /ui/)
 internal/api + internal/web
```

The bus SUB goroutine owns the single-writer `db.Writer` and drives the ingest
pipeline; HTTP handlers read concurrently. This mirrors the Python single-owner
contract — **do not add concurrent writers or a writer mutex**; enforce it by
construction (the writer lives on the bus goroutine).

- `internal/filter/` — dependency-free re-implementation of the C++ `Calc`
  filter engine: tokenizer + Pratt parser, **parameterized** SQL emitter
  (`BuildWhere`/`BuildOrderBy`), in-memory predicate (`EvaluateStream`), and
  the `SearchFilter` transport string (`sort/dir/offset/limit[/base64_expr]`).
  The C++ name-map bugs are deliberately **fixed, not reproduced** (see
  `docs/FILTER.md`) — keep the SQL and in-memory paths agreeing. Watch the
  parity traps: `repr(float)`-style numeric inlining, truncating integer
  division, the mixed-type coercion ladder, IP-literal tokenization.
- `internal/stats/` — `/stats` paging + active-list pipeline.
- `internal/db/` — hand-written SQL over `database/sql`: schema + additive
  migrations (`schema.go`), SOCI-style connection-string → driver/DSN
  (`dsn.go`, `SafeConnDescription` for password-free logging), `Open`
  (`open.go`; `db=:memory:` runs fully in RAM), the single-writer `Writer`
  (`writer.go`), lifetime `cleanup.go`, read `queries.go`, and `TrackStore`
  (persists track patterns in `rtpmon_property`). Only SQLite is blank-imported;
  `SOCIToDSN` also emits pg/mysql DSNs but those drivers aren't wired yet.
- `internal/state/` — `ActiveStreamRegistry` (live streams for `/stats` +
  `/summary`, mutex-guarded, with ghost-stream synthesis) and `InstanceSnapshot`
  (vq-core server stats).
- `internal/bus/` — `Subscriber` (zmq4 SUB loop with reconnect + an on-idle
  tick used by the ghost sweep) and `ControlClient` (zmq4 REQ, mutex-serialized,
  recreates the socket on timeout) for the `/track*` proxy to vq-core.
- `internal/ingest/` — `Pipeline` routes decoded events to the writer, the
  registry, and the instance-stats snapshot.
- `internal/api/` — `NewApp(Deps{…}).Handler()` (a `net/http` mux); read
  queries are parameterized in `queries.go`, JSON shaping in `serialize.go`,
  CSV export in `csv.go`, audio concatenation in `wav.go`, the SPA static
  fallback + `X-Forwarded-Prefix` `<base href>` rewrite in `app.go`. Responses
  use `map[string]any`, so JSON field order is alphabetical (the frontend
  parses by key).
- `internal/model/` + `internal/translate/` — domain event types (`model.go`,
  e.g. `MediaStreamId`) decoded from the protobuf bus by
  `translate.DecodeEvent`; all timestamps are UNIX milliseconds, mirroring the
  C++ wire shapes. Stream reports carry AMR/EVS **DTX/SID silence** stats
  (`DtxSid/DtxCount/DtxTotal`) and a `LostPacketCounter`.
- `internal/pvqa/` — PVQA detector / R-factor decomposition for `/report` and
  `/streamhistory`.
- `internal/worker/` — background workers assembled in `main.go`: `CleanupWorker`
  (record/audio lifetime retention), `TrackSyncWorker` (re-applies persisted
  track patterns if vq-core restarts), and `GhostSweeper` (finalizes ghosts on
  the bus goroutine via the subscriber's on-idle tick). Workers must never
  panic out of their loop.

Config is the shared `vq-monitor.cfg` YAML (same file vq-core reads); only the
`logging`, `server.instance`, `server.zeromq-*`, `server.track-resync-interval`,
`server.ghost-stream-timeout`, `server.pidfile-db`, `dashboard`, and `database`
blocks are used (`internal/config/config.go`). Defaults: PUB bus 9125, control
9127, dashboard 9126 (the Go bundle sample uses 9146 to coexist with earlier
builds). The `dashboard` block also carries quality tuning: `good-mos-threshold`
(3.6) and `silence-ratio-threshold` (0.8), and `sevana-mos` (`auto`/`on`/`off`):
`auto` hides every Sevana MOS / PVQA figure in the UI once vq-core reports a
build without PVQA (version "... (no PVQA)"); the JSON API is unaffected. The `database` block supports opt-in
**bounded retention** (both default off): `records-limit` (keep only the N
most-recent calls) and `store-sip-filter` (persist only SIP-matching calls) —
most useful with an in-memory DB (`connection: "db=:memory:"`) to keep bounded
data in RAM without touching disk. See `backend-go/README.md`.

API conventions (see `docs/vq_db_api.md`, authoritative): GET-only endpoints,
plain HTTP without auth (TLS termination is the deployer's problem). On failure
handlers return a 4xx/503 with a small JSON error body; a malformed `/stats`
filter degrades to `200` with an `error` field and empty lists rather than
failing. Keep all SQL parameterized — closing the C++ injection hole is a stated
design goal.

## Frontend architecture

The dashboard is the `internal/web` package: server-rendered `html/template`
pages driven by vendored htmx, all embedded with `go:embed` (`render.go`) and
served under `/ui/`. It is not a separate build or SPA — the one binary serves
the whole UI. It reads through the **same `internal/api` query/serialize layer**
as the JSON API, so behavior changes belong in `internal/api`, presentation in
`internal/web`. `go test ./internal/web/` covers rendering.

- **Layout:** `app.go` (the `/ui/...` mux + `X-Forwarded-Prefix` → `<base href>`
  rewrite), `pages.go` (one handler per page), `views.go` (per-page query/view
  models built from the request), `render.go` (template parse + `go:embed` of
  `templates/` and `static/`), `markdown.go` (the `copyJSON` "Copy JSON"
  payload helper). Templates live in `templates/*.html` (`layout.html` is the
  shell); assets in `static/` (`htmx.min.js`, `app.css`, `app.js`).
- **Routes** (all under `/ui/`, `GET` unless noted): `summary` (landing, via
  `/ui/{$}` redirect), `core`, `streams`, `stream/{id}`, `sip-calls`,
  `sip-call/{id}`, `chunk/{id}/{ts}`, `track`, plus `POST track/add`,
  `track/remove`, `track/clear`. `/` and `/{$}` redirect to `/ui/summary`; the
  API mux also maps legacy `/#/route` hash links client-side.
- **htmx drives interactivity:** pagination, sorting, filtering, and the ~10 s
  auto-refresh are `hx-get` swaps, not a client framework. The two stream cards
  on `/ui/streams` keep independent list state via `a_`/`f_`-prefixed query
  params (`streamsQuery` in `views.go`) — active vs finished.

### Gotchas

- SIP endpoints use Unix **milliseconds**; the `/stats` family uses
  **seconds** — don't cross the helpers. Timestamp `0` means "not observed"
  (renders as an em dash).
- When the DB or vq-core control socket is unavailable, the backend answers
  `503`; the UI renders an explicit unavailable state rather than a blank
  page — preserve that.
- `/track*` mutations pass patterns as repeated `?pattern=a&pattern=b` keys.
  Patterns are sent verbatim; vq-core normalizes.
- Backend payloads are loosely typed — stream IDs may contain `/` and SIP
  `call_id`s contain `@`, so path params are URL-decoded and ID/number
  lookups stay defensive (multiple ID keys, tolerant numeric parse). Keep
  that shape when adding parameterized routes.
- The "Copy JSON" button ships the backend's verbatim `json_report` string
  when present (`copyJSON` in `markdown.go`) — don't re-encode it.

## Conventions

- Backend: standard library + the deps in `backend-go/go.mod`; the
  `filter`/`stats`/`pvqa` packages stay dependency-free (stdlib only). Tests
  live beside the code as `internal/**/*_test.go` (run with `go test ./...`)
  and every `internal/*` package is expected to stay tested. Run `gofmt` and
  `go vet` before committing.
- `backend-go/dist/` is build output — never edit it (gitignored).
