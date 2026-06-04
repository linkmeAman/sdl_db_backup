package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"sdl/sdl_db_backup/internal/backupapp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := backupapp.RunAPIServer(ctx, os.Getenv("BACKUP_ENV_FILE")); err != nil {
		fmt.Fprintf(os.Stderr, "api server failed: %v\n", err)
		os.Exit(1)
	}
}
