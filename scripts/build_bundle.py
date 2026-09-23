#!/usr/bin/env python3
"""Build a self-contained, offline-installable vq-monitor-db-go bundle: the Go
vq-db binary (web dashboard embedded) + deploy files, packed into ONE archive
that installs under /opt/vq-monitor-db-go to run IN PARALLEL with the C++ and
Python vq-db builds.

Produces:
  backend-go/dist/vq-monitor-db-go-<version>-<os>-<arch>.tgz  containing:
      bin/vq-db                 the static Go binary (CGo-free, dashboard embedded)
      vq-monitor-db-go.service  systemd unit
      vq-monitor.cfg.sample     sample config (parallel-run defaults)
      install.sh                offline installer
      run_test.sh               run in-place (local sqlite, no install)
      README.md, VERSION, LICENSE, NOTICE
  backend-go/dist/install.sh    a copy of the installer, so the two ship together

No venv, no wheels, no separate web assets: one static binary — the dashboard
(internal/web, htmx) is compiled in via go:embed and served at /ui/.
Cross-compile freely — the binary is the only host-specific artifact:
  GOOS=linux GOARCH=amd64 scripts/build_bundle.py   (default: host os/arch)

Usage (from anywhere; paths are resolved relative to this script):
  scripts/build_bundle.py
  GOOS=linux GOARCH=arm64 scripts/build_bundle.py
  VERSION=2.1.3 scripts/build_bundle.py          # override version (default: backend-go/VERSION)
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
from datetime import datetime, timezone
from pathlib import Path

SELF_DIR = Path(__file__).resolve().parent          # repo/scripts
REPO_ROOT = SELF_DIR.parent
GO_DIR = REPO_ROOT / "backend-go"                    # the Go module


def die(msg: str) -> "None":
    print(msg, file=sys.stderr)
    raise SystemExit(1)


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, check=True, **kw)


def go_env(var: str) -> str:
    return subprocess.run(["go", "env", var], capture_output=True, text=True, check=True).stdout.strip()


def human_size(n: int) -> str:
    size = float(n)
    for unit in ("B", "K", "M", "G"):
        if size < 1024 or unit == "G":
            return f"{size:.0f}{unit}" if unit == "B" else f"{size:.1f}{unit}"
        size /= 1024
    return f"{size:.1f}G"


def main() -> int:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("-h", "--help", action="store_true")
    args, extra = parser.parse_known_args()
    if args.help:
        print(__doc__)
        return 0
    if extra:
        die(f"unknown arg: {extra[0]} (try --help)")

    if shutil.which("go") is None:
        die("go toolchain not found")

    # --- Version + target triple ---------------------------------------------
    version = os.environ.get("VERSION") or ""
    if not version:
        try:
            version = (GO_DIR / "VERSION").read_text().strip()
        except OSError:
            version = ""
    if not version:
        die("could not determine version (backend-go/VERSION missing?)")
    goos = os.environ.get("GOOS") or go_env("GOOS")
    goarch = os.environ.get("GOARCH") or go_env("GOARCH")
    name = f"vq-monitor-db-go-{version}-{goos}-{goarch}"
    dist = GO_DIR / "dist"

    with tempfile.TemporaryDirectory() as stage:
        stage_path = Path(stage)
        pkg = stage_path / name
        (pkg / "bin").mkdir(parents=True)

        # --- Compile the binary (static, CGo-free) ---------------------------
        print(f">> Building vq-db {version} for {goos}/{goarch} ...")
        env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
        run(
            [
                "go", "build", "-trimpath",
                f"-ldflags=-s -w -X main.version={version}",
                "-o", str(pkg / "bin" / "vq-db"), "./cmd/vq-db",
            ],
            cwd=GO_DIR, env=env,
        )
        os.chmod(pkg / "bin" / "vq-db", 0o755)

        # --- Deploy/control files --------------------------------------------
        print(">> Staging deploy files ...")
        stage_file(SELF_DIR / "install_bundle.sh", pkg / "install.sh", 0o755)
        stage_file(SELF_DIR / "run_test.sh", pkg / "run_test.sh", 0o755)
        stage_file(SELF_DIR / "vq-monitor-db-go.service", pkg / "vq-monitor-db-go.service", 0o644)
        stage_file(SELF_DIR / "vq-monitor-db-go.cfg.sample", pkg / "vq-monitor.cfg.sample", 0o644)
        if (GO_DIR / "README.md").is_file():
            stage_file(GO_DIR / "README.md", pkg / "README.md", 0o644)
        for legal in ("LICENSE", "NOTICE"):
            stage_file(REPO_ROOT / legal, pkg / legal, 0o644)

        git_sha = git_short_sha()
        go_ver = subprocess.run(["go", "version"], capture_output=True, text=True, check=True).stdout.split()[2]
        (pkg / "VERSION").write_text(
            f"name=vq-monitor-db-go\n"
            f"version={version}\n"
            f"git={git_sha}\n"
            f"built={datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')}\n"
            f"go={go_ver}\n"
            f"goos={goos}\n"
            f"goarch={goarch}\n"
        )

        # --- Pack ------------------------------------------------------------
        print(">> Packing bundle ...")
        dist.mkdir(parents=True, exist_ok=True)
        archive = dist / f"{name}.tgz"
        with tarfile.open(archive, "w:gz") as tar:
            tar.add(pkg, arcname=name)
        stage_file(SELF_DIR / "install_bundle.sh", dist / "install.sh", 0o755)

    print()
    print("Built:")
    print(f"  {human_size(archive.stat().st_size)}  {archive}")
    print(f"  (vq-db {version}, dashboard embedded, {goos}/{goarch})")
    print(f"Installer copied to {dist / 'install.sh'} — ship both to the target, then run:")
    print("   sudo ./install.sh            # picks the newest vq-monitor-db-go-*.tgz beside it")
    print("Or test in-place without installing (unpack the .tgz, then from inside it):")
    print("   ./run_test.sh               # serves the dashboard on :9156 with a local sqlite DB")
    return 0


def stage_file(src: Path, dst: Path, mode: int) -> None:
    shutil.copyfile(src, dst)
    os.chmod(dst, mode)


def git_short_sha() -> str:
    try:
        return subprocess.run(
            ["git", "-C", str(REPO_ROOT), "rev-parse", "--short", "HEAD"],
            capture_output=True, text=True, check=True,
        ).stdout.strip() or "unknown"
    except (subprocess.CalledProcessError, OSError):
        return "unknown"


if __name__ == "__main__":
    raise SystemExit(main())
