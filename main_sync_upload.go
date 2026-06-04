package main

import (
	"context"
	"log"
	"os"

	"sdl/sdl_db_backup/internal/backupapp"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	result, err := backupapp.RunFromEnvFile(
		context.Background(),
		os.Getenv("BACKUP_ENV_FILE"),
		backupapp.RunSinks{Console: os.Stdout},
	)
	if err != nil {
		log.Printf("backup startup failed: %v", err)
		os.Exit(1)
	}
	os.Exit(result.ExitCode)
}
