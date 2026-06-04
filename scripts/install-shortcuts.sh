#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET_DIR="${XDG_BIN_HOME:-$HOME/.local/bin}"

mkdir -p "$TARGET_DIR"
ln -sf "$ROOT_DIR/scripts/tui" "$TARGET_DIR/sdl-db-backup-tui"
ln -sf "$ROOT_DIR/scripts/run-main" "$TARGET_DIR/sdl-db-backup-run"

printf 'Installed shortcuts in %s\n' "$TARGET_DIR"
printf '  sdl-db-backup-tui\n'
printf '  sdl-db-backup-run\n'
