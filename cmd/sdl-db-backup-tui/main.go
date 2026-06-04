package main

import (
	"fmt"
	"os"

	"sdl/sdl_db_backup/internal/backupapp"
	"sdl/sdl_db_backup/internal/tui"
)

func main() {
	envPath := backupapp.ResolveEnvFilePath(os.Getenv("BACKUP_ENV_FILE"))
	cfg, err := backupapp.LoadConfig(envPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if err := tui.Run(envPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "tui failed: %v\n", err)
		os.Exit(1)
	}
}
