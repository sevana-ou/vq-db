#!/usr/bin/env python3
"""Dev helper: build a native vq-db binary into backend-go/dist/vq-db.

For a full, shippable archive (binary with embedded dashboard + installer +
systemd unit packed into one .tgz), use scripts/build_bundle.py instead.

Honours GOOS / GOARCH from the environment for cross-compiles.
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

SELF_DIR = Path(__file__).resolve().parent          # repo/scripts
GO_DIR = (SELF_DIR / ".." / "backend-go").resolve()  # the Go module
OUT = GO_DIR / "dist"


def main() -> int:
    version = "dev"
    try:
        version = (GO_DIR / "VERSION").read_text().strip() or "dev"
    except OSError:
        pass

    OUT.mkdir(parents=True, exist_ok=True)
    goos = os.environ.get("GOOS", "native")
    goarch = os.environ.get("GOARCH", "native")
    print(f"Building vq-db {version} (GOOS={goos} GOARCH={goarch})...")
    env = dict(os.environ, CGO_ENABLED="0")
    subprocess.run(
        [
            "go", "build", "-trimpath",
            f"-ldflags=-s -w -X main.version={version}",
            "-o", str(OUT / "vq-db"), "./cmd/vq-db",
        ],
        cwd=GO_DIR, env=env, check=True,
    )
    print(f"  -> {OUT / 'vq-db'}")
    print("For a full deployable archive: scripts/build_bundle.py")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except subprocess.CalledProcessError as exc:
        print(f"build failed: {exc}", file=sys.stderr)
        raise SystemExit(exc.returncode or 1)
