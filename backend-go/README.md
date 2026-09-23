# vq-db (Go)

Go re-implementation of the `vq-db` component, ported from the (now removed)
Python reference. It is a
drop-in peer of the earlier C++ and Python builds: same `vq-monitor.cfg`,
same SQL schema, same ZeroMQ bus/control protocol, and the same REST API
([../docs/vq_db_api.md](../docs/vq_db_api.md)), so it can run side by side on a
different `dashboard.port` during validation.

Single static, CGo-free binary (~19 MB) — no venv, no wheels, cross-compilable
from any host.

## Build & run

```sh
go build ./cmd/vq-db                      # or: ../scripts/build.py
./vq-db --config /etc/vq-monitor/vq-monitor.cfg
```

Reads the shared `vq-monitor.cfg`, connects its ZMQ SUB to vq-core's bus,
persists to the configured SQLite database, serves the JSON API + the embedded
web dashboard (`internal/web`, htmx, served at `/ui/`) on `dashboard.port`, and
proxies `/track*` to vq-core's control socket. Cross-compile with `GOOS`/`GOARCH`; Docker via the `Dockerfile`
(distroless, single binary).

## Packaging

`../scripts/build_bundle.py` compiles the binary (dashboard embedded via
`go:embed`) and packs it with `install.sh` + systemd unit + config sample +
`run_test.sh` into one `dist/vq-monitor-db-go-<version>-<os>-<arch>.tgz`.
On the target, `sudo ./install.sh` installs it under `/opt/vq-monitor-db-go`
(dashboard :9146), running in parallel with the C++ and Python builds.

To try a bundle without installing, unpack it and run it in place — it serves
the dashboard with a local SQLite DB (under `./.run_test/`), no root needed:

```sh
tar xzf vq-monitor-db-go-*.tgz && cd vq-monitor-db-go-*/
./run_test.sh                       # dashboard + API on :9156 (DASH_PORT= to change)
curl -s http://localhost:9156/version
```

## Layout

```
cmd/vq-db/        entrypoint: config → assembly → http.Server, signals, pidfile
internal/config/  YAML loader + duration parser
internal/proto/   generated capturemessage.pb.go (generate.sh to regenerate)
internal/model/   domain event types
internal/translate/  protobuf Event → model
internal/db/      SOCI→DSN, schema + additive migrations, single-writer,
                  cleanup, track store, read queries
internal/filter/  filter mini-language (tokenizer, Pratt parser, SQL + eval
                  emitters), SearchFilter transport, name maps
internal/stats/   active-list pipeline + action-based paging
internal/pvqa/    detector-report parse + decomposition
internal/state/   active-stream registry + ghost sweep + instance snapshot
internal/bus/     ZMQ SUB subscriber + REQ control client
internal/ingest/  event dispatcher (writer + registry + snapshot)
internal/api/     read API handlers, serializers, CSV, WAV, /stats merge
internal/web/     embedded dashboard: Go templates + htmx at /ui/ (go:embed)
internal/worker/  track restore/resync, records/audio cleanup, ghost sweeper
```

## Tests

```sh
go test ./...
```

Each package ports the corresponding Python `tests/` suite, so the two
implementations are checked against the same behavioral spec — including the
filter mini-language semantics, PVQA decomposition, serializer/CSV output
shapes, the DB writer/cleanup, the active-stream registry/ghost logic, and a
real ZeroMQ PUB/SUB round-trip (`internal/ingest`) that doubles as the
go-zeromq ↔ libzmq interop check.

## In-memory / bounded retention (opt-in)

The database can run entirely in RAM, keeping only recent and/or SIP-selected
calls without touching disk. All three knobs default off (persist everything):

```yaml
database:
  engine:     sqlite3
  connection: "db=:memory:"     # nothing on disk; data is lost on restart
  records-limit: 500            # keep only the 500 most-recent calls, evict oldest
  store-sip-filter:             # persist only calls whose SIP src/dst matches
    - "sip:vip@"
```

- `records-limit` (N, default 0 = unlimited) — after each completed call, evicts
  the oldest calls beyond N. A call is a distinct `sip_callid` (a stream with no
  Call-ID counts as one). Bounds both the DB and the writer's in-memory maps.
- `store-sip-filter` (list, default empty = keep all) — only persists a call
  whose SIP source/destination contains one of the substrings (case-insensitive,
  same matching as the track list). Non-matching calls (including network-MOS-only
  streams with no SIP) are not stored; anything written before the SIP
  correlation was known is purged when the call completes.
- They compose: the SIP filter selects *which* calls, `records-limit` bounds *how
  many*. Both work on-disk too, but are most useful with `db=:memory:`.

The live/active-stream list (the in-memory registry powering `show_active`) is
unaffected by these — it always reflects all in-progress streams; the options
only shape what lands in the finished/stored list.

## Notes & known gaps

- **DB engines:** SQLite (`modernc.org/sqlite`, pure Go) is wired and is the
  deployment target. `SOCIToDSN` already produces PostgreSQL/MySQL DSNs; blank-
  import those drivers in `internal/db/open.go` to enable them.
- **JSON field order** is alphabetical (Go marshals maps sorted) rather than the
  Python insertion order; API clients parse by key, so this is behaviorally
  equivalent. Whole-number floats marshal as `20` where Python emitted `20.0` —
  tolerated by clients.

## License

Licensed under the [Apache License, Version 2.0](../LICENSE). See
[NOTICE](../NOTICE) for attribution and bundled third-party components.
