#!/usr/bin/env bash
# Regenerate capturemessage.pb.go from capturemessage.proto in this directory.
# Requires protoc and protoc-gen-go on PATH (go install
# google.golang.org/protobuf/cmd/protoc-gen-go@latest).
set -euo pipefail

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

protoc \
  --proto_path="$SELF_DIR" \
  --go_opt=Mcapturemessage.proto=github.com/sevana-ou/vq-db/internal/proto \
  --go_opt=paths=source_relative \
  --go_out="$SELF_DIR" \
  capturemessage.proto

echo "generated $SELF_DIR/capturemessage.pb.go"
