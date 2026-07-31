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

	GO_VERSION="${GO_VERSION:-1.24.2}"
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
	local has_mysql=true
	if ! command -v mysqldump >/dev/null 2>&1 && ! command -v mysql >/dev/null 2>&1; then
		has_mysql=false
		warn "MySQL client (mysqldump/mysql) not found."
		warn "To dump MySQL databases, install mysql-client or mariadb-client via your package manager:"
		warn "  Debian/Ubuntu: sudo apt update && sudo apt install -y mysql-client"
		warn "  RHEL/CentOS:   sudo dnf install -y mariadb"
	fi

	if ! command -v xtrabackup >/dev/null 2>&1 || ! command -v xbcloud >/dev/null 2>&1; then
		log "Notice: Percona XtraBackup/xbcloud not found. Physical backup features will be disabled."
	fi

	if ! command -v php >/dev/null 2>&1; then
		log "Notice: PHP CLI not found. (Only needed if BACKUP_LOGICAL_UPLOAD_MODE=php)"
	fi

	return 0
}

ensure_local_go
check_required_tools

log "Local bootstrap completed successfully."
log "Next step: run ./install.sh or configure .env file."
