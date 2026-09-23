#!/usr/bin/env bash
# Install (or upgrade) vq-monitor-db-go (the Go vq-db; the dashboard is
# embedded in the binary) under /opt/vq-monitor-db-go, from a bundle built by
# scripts/build_bundle.py.
#
# Designed to run IN PARALLEL with the C++ vq-db and the Python vq-monitor-db:
# the whole install is self-contained under /opt/vq-monitor-db-go and uses its
# OWN config, dashboard port (9146), and database. It does NOT touch
# /etc/vq-monitor, /var/vq-monitor, /opt/vq-monitor-db, or any other service
# unit — the three services coexist (same vq-core bus ports, different dashboard
# ports).
#
# No venv, no wheels, no interpreter, no separate web assets: one static
# binary. Idempotent — re-running upgrades in place (stops a running service,
# replaces the binary, restarts).
#
# Usage (as root on the target host):
#   sudo ./install.sh                                   # newest vq-monitor-db-go-*.tgz beside this script (or $PWD)
#   sudo ./install.sh vq-monitor-db-go-2.1.3-linux-amd64.tgz
set -euo pipefail

APP=/opt/vq-monitor-db-go
BIN="$APP/bin"
ETC="$APP/etc"
DATA="$APP/data"
SERVICE=vq-monitor-db-go
UNIT="/etc/systemd/system/${SERVICE}.service"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 1. Resolve the bundle: explicit arg, else the newest vq-monitor-db-go-*.tgz
#    next to this script or in the current directory ("the latest version").
ARCHIVE="${1:-}"
if [ -z "$ARCHIVE" ]; then
    ARCHIVE="$(ls -t "$SELF_DIR"/vq-monitor-db-go-*.tgz ./vq-monitor-db-go-*.tgz 2>/dev/null | head -1 || true)"
fi
[ -n "$ARCHIVE" ] && [ -f "$ARCHIVE" ] || {
    echo "no vq-monitor-db-go-*.tgz found; pass one as an argument" >&2; exit 1; }
echo ">> Installing from: $ARCHIVE"

# 2. Unpack to a scratch dir and sanity-check it's really a bundle.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
tar xzf "$ARCHIVE" -C "$TMP"
SRC="$(find "$TMP" -mindepth 1 -maxdepth 1 -type d -name 'vq-monitor-db-go-*' | head -1)"
[ -f "${SRC:-}/bin/vq-db" ] || { echo "bundle missing bin/vq-db — not a vq-monitor-db-go bundle" >&2; exit 1; }

# 3. Stop a running instance so the binary can be replaced atomically.
was_active=no
if systemctl is-active --quiet "$SERVICE" 2>/dev/null; then
    was_active=yes
    echo ">> Stopping running $SERVICE ..."
    systemctl stop "$SERVICE"
fi

echo ">> Creating directory tree under $APP ..."
mkdir -p "$BIN" "$ETC" "$DATA"

# 4. Binary: install into a temp name and mv into place (atomic swap).
echo ">> Installing binary ..."
install -m 0755 "$SRC/bin/vq-db" "$BIN/vq-db.new"
mv -f "$BIN/vq-db.new" "$BIN/vq-db"

# 5. Old on-disk dashboards are obsolete: the web UI ships inside the binary.
if [ -d "$APP/dashboard" ]; then
    echo ">> Removing obsolete on-disk dashboard (now embedded in the binary) ..."
    rm -rf "$APP/dashboard"
fi

# 6. systemd unit.
echo ">> Installing systemd unit ..."
install -m 0644 "$SRC/vq-monitor-db-go.service" "$UNIT"
systemctl daemon-reload

# 7. Config: always refresh the sample; never clobber an existing real config.
install -m 0644 "$SRC/vq-monitor.cfg.sample" "$ETC/vq-monitor.cfg.sample"
fresh_config=no
if [ ! -e "$ETC/vq-monitor.cfg" ]; then
    install -m 0644 "$SRC/vq-monitor.cfg.sample" "$ETC/vq-monitor.cfg"
    fresh_config=yes
fi

# 8. If it was running before, bring it back on the new version.
if [ "$was_active" = yes ]; then
    echo ">> Restarting $SERVICE ..."
    systemctl start "$SERVICE"
fi

ver="$( [ -f "$SRC/VERSION" ] && grep -m1 '^version='  "$SRC/VERSION" | cut -d= -f2 || echo '?' )"
cat <<EOF

vq-monitor-db-go ${ver} installed into ${APP} (dashboard embedded, served at /ui/).
  binary:    ${BIN}/vq-db
  config:    ${ETC}/vq-monitor.cfg   (sample: vq-monitor.cfg.sample)
  data:      ${DATA}
  service:   ${SERVICE}   (runs in parallel with the C++ and Python vq-db)
EOF
if [ "$fresh_config" = yes ]; then
    cat <<EOF
  NOTE: a default config was just created. It connects to vq-core on the SAME
        bus ports as the other builds (9125/9127) but serves its OWN dashboard
        on port 9146. Edit ${ETC}/vq-monitor.cfg if those defaults clash with
        your host, then start the service.
EOF
fi
if [ "$was_active" = no ]; then
    cat <<EOF
  start:  systemctl enable --now ${SERVICE}
  logs:   journalctl -u ${SERVICE} -f
EOF
fi
