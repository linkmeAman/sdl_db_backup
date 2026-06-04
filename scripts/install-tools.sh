#!/usr/bin/env bash
set -euo pipefail

log() {
	printf '%s\n' "$*"
}

warn() {
	printf 'warning: %s\n' "$*" >&2
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_DIR="$ROOT_DIR/.tools"
LOCAL_BIN_DIR="$TOOLS_DIR/bin"
LOCAL_GO_DIR="$TOOLS_DIR/go"

mkdir -p "$TOOLS_DIR" "$LOCAL_BIN_DIR"

go_arch() {
	case "$(uname -m)" in
		x86_64|amd64)
			printf '%s\n' amd64
			;;
		aarch64|arm64)
			printf '%s\n' arm64
			;;
		*)
			die "unsupported CPU architecture: $(uname -m)"
			;;
	esac
}

ensure_local_go() {
	if command -v go >/dev/null 2>&1; then
		log "System Go already available: $(command -v go)"
		return 0
	fi

	if [[ -x "$LOCAL_GO_DIR/bin/go" ]]; then
		log "Local Go toolchain already present in $LOCAL_GO_DIR"
		export PATH="$LOCAL_GO_DIR/bin:$LOCAL_BIN_DIR:$PATH"
		return 0
	fi

	GO_VERSION="${GO_VERSION:-1.23.10}"
	GO_TARBALL_URL="${GO_TARBALL_URL:-https://go.dev/dl/go${GO_VERSION}.linux-$(go_arch).tar.gz}"
	tmp_tarball="$(mktemp /tmp/go.XXXXXX.tar.gz)"

	log "Downloading local Go toolchain ${GO_VERSION}..."
	curl -fsSL "$GO_TARBALL_URL" -o "$tmp_tarball"
	rm -rf "$LOCAL_GO_DIR"
	mkdir -p "$TOOLS_DIR"
	tar -C "$TOOLS_DIR" -xzf "$tmp_tarball"
	rm -f "$tmp_tarball"
	mv "$TOOLS_DIR/go" "$LOCAL_GO_DIR"
	ln -sf "$LOCAL_GO_DIR/bin/go" "$LOCAL_BIN_DIR/go"
	export PATH="$LOCAL_GO_DIR/bin:$LOCAL_BIN_DIR:$PATH"
	log "Installed local Go toolchain under $LOCAL_GO_DIR"
}

check_required_tools() {
	missing=()
	for tool in mysql mysqldump php xtrabackup xbcloud; do
		if ! command -v "$tool" >/dev/null 2>&1; then
			missing+=("$tool")
		fi
	done

	if [[ ${#missing[@]} -gt 0 ]]; then
		warn "This script does not install system packages. Missing tools: ${missing[*]}"
		warn "Ask the server administrator to install them, or provide them via the target host's standard package manager."
		return 1
	fi
}

ensure_local_go

if ! check_required_tools; then
	log "Local bootstrap completed, but host prerequisites are still missing."
	log "Next step: copy .env.example to .env and configure the target server."
	exit 1
fi

log "Local bootstrap completed successfully."
log "Next step: copy .env.example to .env and configure the target server."
