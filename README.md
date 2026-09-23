# vq-db

The dashboard and storage component of the **Sevana VQ monitor**.

`vq-db` subscribes to `vq-core`'s ZeroMQ event bus and saves call-quality data
(RTP stream reports, SIP calls, MOS / R-factor / PVQA metrics, audio chunks) to
a database. It serves that data through a read-only REST API and a built-in
web dashboard.

- **One static binary.** Written in Go with no CGo: pure-Go SQLite
  (`modernc.org/sqlite`) and ZeroMQ (`go-zeromq/zmq4`). It cross-compiles
  with `GOOS`/`GOARCH` and needs no runtime dependencies.
- **Built-in dashboard.** The pages are rendered on the server with Go
  templates and htmx, compiled into the binary, and served at `/ui/`. There
  is no separate frontend build.
- **REST API.** GET-only JSON endpoints plus CSV export and WAV audio
  download. The API has a filter mini-language, and every SQL query uses
  parameters.
- **Live view.** Shows active streams, vq-core instance stats, and a proxy
  for vq-core's `/track*` control socket.
- **Retention controls.** Old records and audio are cleaned up by age. You
  can also cap the number of stored calls or store only calls that match a
  SIP filter, and the database can run fully in memory.

## Quick start

Requires Go 1.25+ (`GOTOOLCHAIN=auto` fetches it).

```sh
cd backend-go
go build ./cmd/vq-db
./vq-db --config /etc/vq-monitor/vq-monitor.cfg
```

Open `http://<host>:9126/ui/`; the port is `dashboard.port` in the config.

`vq-db` reads the same `vq-monitor.cfg` YAML file as `vq-core`. It uses only
the `logging`, `server.*` (instance, ZeroMQ ports, resync/ghost timeouts),
`dashboard` and `database` blocks. See
[`scripts/vq-monitor-db-go.cfg.sample`](scripts/vq-monitor-db-go.cfg.sample)
for an annotated example.

### Try a release bundle without installing

```sh
scripts/build_bundle.py                 # -> backend-go/dist/vq-monitor-db-go-<ver>-<os>-<arch>.tgz
tar xzf backend-go/dist/vq-monitor-db-go-*.tgz && cd vq-monitor-db-go-*/
./run_test.sh                           # dashboard + API on :9156, throwaway SQLite DB
```

### Install as a systemd service

```sh
sudo ./install.sh                       # from the bundle; installs to /opt/vq-monitor-db-go
sudo systemctl enable --now vq-monitor-db-go
```

A distroless `Dockerfile` is included in `backend-go/`.

## Repository layout

| Path | Contents |
| --- | --- |
| [`backend-go/`](backend-go) | The Go daemon (`cmd/vq-db`) and its `internal/*` packages. See [`backend-go/README.md`](backend-go/README.md). |
| [`backend-go/internal/web/`](backend-go/internal/web) | The embedded dashboard (templates, htmx, CSS). |
| [`docs/`](docs) | [REST API reference](docs/vq_db_api.md), the [filter language](docs/FILTER.md), and the [`/stats` endpoint](docs/STATS.md). |
| [`scripts/`](scripts) | Build, bundle, install and test scripts, the systemd unit, the config sample, and the nginx config. See [`scripts/README.md`](scripts/README.md). |

## Development

```sh
cd backend-go
go test ./...
go vet ./...
gofmt -l .
```

Every `internal/*` package has tests. The protobuf bindings are
pre-generated. Regenerate them with `internal/proto/generate.sh` only if
`capturemessage.proto` changes.

## License

Copyright 2026 Sevana Ou PTE. LTD.

Licensed under the [Apache License, Version 2.0](LICENSE). See
[NOTICE](NOTICE) for third-party attributions.
