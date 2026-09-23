# Deploying vq-db

> The vq-db backend is now the Go implementation in [`../backend-go`](../backend-go)
> (the Python backend was removed in the `refactor-golang` migration). The web
> dashboard is embedded in the Go binary (`internal/web`, htmx) and served at
> `/ui/`. This directory holds the backend build/deploy scripts (`build.py`,
> `build_bundle.py`, `install_bundle.sh`, `run_test.sh`, the systemd unit and
> config sample) and the reverse-proxy config.

vq-db is a drop-in peer of vq-core: it subscribes to vq-core's ZeroMQ bus,
persists to the configured database, and serves the dashboard. It reads the same
`vq-monitor.cfg` as vq-core (only the `logging`, `server.instance`,
`server.zeromq-*`, `dashboard`, and `database` blocks are used).

## Backend (Go) — build & install

```sh
# Build a self-contained bundle (static binary with embedded dashboard + installer):
scripts/build_bundle.py            # -> backend-go/dist/vq-monitor-db-go-<version>-<os>-<arch>.tgz
GOOS=linux GOARCH=arm64 scripts/build_bundle.py   # cross-compile

# Copy the .tgz + install.sh to the target, then:
sudo ./install.sh                             # unpacks the newest vq-monitor-db-go-*.tgz beside it
sudo systemctl enable --now vq-monitor-db-go
journalctl -u vq-monitor-db-go -f
```

Installs under `/opt/vq-monitor-db-go` (dashboard port **9146**), running in
parallel with any earlier C++/Python build. To smoke-test a bundle in place
without installing, unpack it and run `./run_test.sh` (dashboard + local SQLite
on :9156). Full details: [`../backend-go/README.md`](../backend-go/README.md).

## Frontend

The dashboard is server-rendered by the backend itself
(`backend-go/internal/web`: Go templates + htmx, all assets embedded via
`go:embed`) — there is no separate frontend build or deploy step. (The old
Flutter app and its `frontend_*.py` helpers were removed from the repo;
recover them from git history if ever needed.)

`nginx-beta.conf` is a sample reverse-proxy config (sets `X-Forwarded-Prefix`
for sub-path mounting, which the backend honours when generating UI links and
redirects).

## Config expectations

```yaml
server:
  instance: { id: agent_1, name: First instance }
  zeromq-port: 9125            # vq-core PUB bus (default)
  zeromq-control-port: 9127    # vq-core REP control socket (default)
  track-resync-interval: 60    # re-apply persisted track patterns if vq-core restarts (0 disables)
  ghost-stream-timeout: 120s   # finalize a stream whose stream_finish never arrived (0 disables)
dashboard:
  host: 0.0.0.0                # bind address; 127.0.0.1 behind a reverse proxy (--host overrides)
  port: 9146                   # web UI is embedded, served at /ui/ ("root:" is deprecated/ignored)
  max-streams: 40
  good-mos-threshold: 3.6
database:
  engine: sqlite3
  connection: "db=/opt/vq-monitor-db-go/data/vq-monitor.sqlite"
  records-lifetime: 7d
  audio-lifetime: 1d
```

Postgres/MySQL: set `engine: postgresql` / `mysql` and a SOCI-style `connection`
(`dbname=… host=… user=… password=…`). The Go build currently wires only the
SQLite driver — enable the pg/mysql drivers in `backend-go/internal/db/open.go`
before using those engines.
