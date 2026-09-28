#!/usr/bin/env bash
# Cross-compile the installer to a single self-contained windows/amd64 .exe.
# Runs on any box with Go (the Linux build box, per tzohar-specs/infra.md §4) —
# this project has no Windows build path of its own.
#
#   scripts/build.sh [OUTPUT.exe]
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
cd "$ROOT"
VERSION="$(tr -d '[:space:]' < VERSION)"
OUT="${1:-$ROOT/tzohar-android-installer.exe}"
echo "Building tzohar-android-installer $VERSION -> $OUT (windows/amd64)"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "$OUT" .
echo "Built: $OUT"
