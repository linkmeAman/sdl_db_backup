# Contributing to SDL DB Backup

First off, thank you for considering contributing to SDL DB Backup! It's people like you that make open source such a great community.

## Development Setup

To start developing, you'll need Go 1.21+ installed on your system.

1. **Clone the repository:**
   ```bash
   git clone https://github.com/your-username/sdl-db-backup.git
   cd sdl-db-backup
   ```

2. **Set up the dependencies:**
   This project does not rely on complex CGO bindings. You just need `go mod tidy`:
   ```bash
   go mod tidy
   ```

3. **Install Host Tools:**
   You will need `mysql`, `mysqldump` (or `pg_dump`), `xtrabackup`, and `xbcloud` if you want to test the full backup flow locally. You can use our provided script to download local binaries:
   ```bash
   ./scripts/install-tools.sh
   ```

## Architecture Overview

The codebase is split into three main operational surfaces:
- **TUI (`cmd/sdl-db-backup-tui`)**: The interactive terminal UI built with `charmbracelet/bubbletea`. All UI states, keybindings, and rendering live in `internal/tui/`.
- **API (`cmd/sdl-db-backup-api`)**: The REST API.
- **Core (`internal/backupapp`)**: The engine that actually runs the backups, talks to S3, encrypts data, and interfaces with MySQL/PostgreSQL.

## Running Tests

Before submitting a Pull Request, please ensure all tests pass:

```bash
go test ./internal/... ./cmd/...
```

If you are modifying the TUI, ensure you have not broken the scroll or keybinding logic in `internal/tui/app_test.go`.

## Submitting a Pull Request

1. Fork the repository and create your branch from `main`.
2. If you've added code that should be tested, add tests.
3. Ensure the test suite passes (`go test ./...`).
4. Format your code with `go fmt ./...` and `goimports`.
5. Submit your PR with a clear description of what changed and why!
