#!/usr/bin/env bash
# Publish the installer .exe to the backend downloads dir so it is served at
# /downloads/tzohar-android-installer.exe (a stable alias humans download) plus
# a versioned archival copy. The installer is self-contained and does not
# self-update, so there is no manifest to publish (unlike the agents).
#
#   scripts/deploy.sh                       build here (needs Go), then publish
#   scripts/deploy.sh --prebuilt FILE.exe   publish an exe built elsewhere
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
# Deploy target: production on DigitalOcean (tzohar-specs/infra.md).
VPS="${TZOHAR_VPS:-tzohar-prod}"
REMOTE_DOWNLOADS="/root/tzohar/backend/downloads"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
STABLE="tzohar-android-installer.exe"
VERSIONED="tzohar-android-installer_${VERSION}.exe"

if [ "${1:-}" = "--prebuilt" ] && [ -n "${2:-}" ]; then
  EXE="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
  [ -f "$EXE" ] || { echo "Error: $EXE not found"; exit 1; }
  echo "[1/2] Using prebuilt: $EXE"
else
  echo "[1/2] Building installer..."
  "$SCRIPT_DIR/build.sh" "$ROOT/$STABLE"
  EXE="$ROOT/$STABLE"
fi

# ssh/scp to the droplet resets on quiet steps; keep the connection alive
# (see tzohar-specs/infra.md and the agent-build-box notes).
SSH_OPTS="-o ServerAliveInterval=15 -o ServerAliveCountMax=8"
echo "[2/2] Uploading versioned + stable alias to $VPS ..."
scp $SSH_OPTS "$EXE" "$VPS:$REMOTE_DOWNLOADS/$VERSIONED"
scp $SSH_OPTS "$EXE" "$VPS:$REMOTE_DOWNLOADS/$STABLE"
echo "Published:"
echo "  https://api.tzohar.tech/downloads/$STABLE"
echo "  https://api.tzohar.tech/downloads/$VERSIONED"
