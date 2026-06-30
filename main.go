package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"sdl/sdl_db_backup/internal/backupapp"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if len(os.Args) > 1 && os.Args[1] == "inspect" {
		if len(os.Args) < 3 {
			log.Fatalf("Usage: sdl-db-backup inspect <run-id>")
		}
		runID := os.Args[2]
		out, err := backupapp.InspectRunToString(context.Background(), os.Getenv("BACKUP_ENV_FILE"), runID)
		if err != nil {
			log.Fatalf("Inspect failed: %v", err)
		}
		fmt.Print(out)
		os.Exit(0)
	}

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
