#!/usr/bin/env bash
set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

echo -e "${CYAN}=== SDL DB Backup One-Command Installer ===${NC}"

if [ "$EUID" -eq 0 ]; then
  echo -e "${YELLOW}Warning: Running as root is not recommended for this user-scoped installation.${NC}"
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="$HOME/.local/bin"
CONF_DIR="$HOME/.config/sdl-db-backup"
SYSTEMD_DIR="$HOME/.config/systemd/user"

mkdir -p "$BIN_DIR"
mkdir -p "$CONF_DIR"
mkdir -p "$SYSTEMD_DIR"

# Ensure local bin and bootstrapped Go binaries are in PATH
export PATH="$ROOT_DIR/.tools/go/bin:$ROOT_DIR/.tools/bin:$BIN_DIR:$PATH"

echo -e "${GREEN}[1/5] Verifying System Dependencies...${NC}"
if [ -f "$ROOT_DIR/scripts/install-tools.sh" ]; then
    bash "$ROOT_DIR/scripts/install-tools.sh"
    export PATH="$ROOT_DIR/.tools/go/bin:$ROOT_DIR/.tools/bin:$BIN_DIR:$PATH"
else
    echo -e "${YELLOW}Warning: ./scripts/install-tools.sh not found. Skipping dependency check.${NC}"
fi

echo -e "${GREEN}[2/5] Compiling and Installing Binaries...${NC}"
if ! command -v go >/dev/null 2>&1; then
    echo -e "${RED}Error: Go compiler is not installed or available on PATH.${NC}"
    exit 1
fi

# Compile the Go binaries
go build -buildvcs=false -o "$BIN_DIR/sdl-db-backup" "$ROOT_DIR/main.go"
go build -buildvcs=false -o "$BIN_DIR/sdl-db-backup-tui" "$ROOT_DIR/cmd/sdl-db-backup-tui"
go build -buildvcs=false -o "$BIN_DIR/sdl-db-backup-health" "$ROOT_DIR/cmd/sdl-db-backup-health"
go build -buildvcs=false -o "$BIN_DIR/sdl-db-backup-api" "$ROOT_DIR/cmd/sdl-db-backup-api"

echo -e "${GREEN}[3/5] Setting up configuration...${NC}"
ENV_FILE="$CONF_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    cp "$ROOT_DIR/.env.example" "$ENV_FILE"
    
    # Auto-generate a 32-byte (64 char hex) encryption key
    if command -v openssl >/dev/null 2>&1; then
        NEW_KEY=$(openssl rand -hex 32)
    else
        NEW_KEY=$(head -c 32 /dev/urandom | xxd -p | tr -d '\n')
    fi
    
    # Replace BACKUP_ENCRYPTION_KEY in the new .env
    sed -i "s/^BACKUP_ENCRYPTION_KEY=.*/BACKUP_ENCRYPTION_KEY=$NEW_KEY/" "$ENV_FILE"
    echo -e "${GREEN}Created new configuration at $ENV_FILE${NC}"
else
    echo -e "${YELLOW}Configuration already exists at $ENV_FILE. Skipping .env creation.${NC}"
fi

echo -e "${GREEN}[4/5] Configuring Systemd Background Service...${NC}"
# Generate service unit
cat > "$SYSTEMD_DIR/sdl-db-backup.service" << EOF
[Unit]
Description=SDL DB Backup Service
After=network.target

[Service]
Type=oneshot
ExecStart=$BIN_DIR/sdl-db-backup
Environment="BACKUP_ENV_FILE=$ENV_FILE"
Environment="BACKUP_EXECUTION_SOURCE=runner"
WorkingDirectory=$HOME
TimeoutStartSec=0

[Install]
WantedBy=default.target
EOF

# Generate timer unit
cat > "$SYSTEMD_DIR/sdl-db-backup.timer" << EOF
[Unit]
Description=SDL DB Backup Timer

[Timer]
OnUnitActiveSec=1h
Persistent=true

[Install]
WantedBy=timers.target
EOF

echo -e "${GREEN}[5/5] Enabling Systemd Timer...${NC}"
if command -v systemctl >/dev/null 2>&1 && systemctl --user status >/dev/null 2>&1; then
    systemctl --user daemon-reload
    systemctl --user enable --now sdl-db-backup.timer
else
    echo -e "${YELLOW}Notice: systemctl --user session not active right now. Skipping systemd enable.${NC}"
    echo -e "${YELLOW}To enable background timer later, run: systemctl --user enable --now sdl-db-backup.timer${NC}"
fi

echo -e "${CYAN}========================================================================${NC}"
echo -e "${GREEN}✓ Installation Complete!${NC}"
echo ""
echo -e "${YELLOW}🚨 ACTION REQUIRED: SECRETS AND PASSWORDS 🚨${NC}"
echo -e "Your configuration has been created at: ${CYAN}$ENV_FILE${NC}"
echo -e "You MUST manually edit this file to configure your credentials:"
echo -e "  - DB_USER and DB_PASS"
echo -e "  - BACKUP_S3_KEY_ID and BACKUP_S3_KEY_SECRET"
echo -e "  - BACKUP_XTRABACKUP_PASS (if physical backups are enabled)"
echo ""
echo -e "We have automatically generated a secure BACKUP_ENCRYPTION_KEY for you."
echo ""
echo -e "You can edit these settings seamlessly by running:"
echo -e "  ${CYAN}sdl-db-backup-tui${NC} (Then press '4' for the Config Editor)"
echo -e "${CYAN}========================================================================${NC}"
