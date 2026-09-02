package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"sdl/sdl_db_backup/internal/backupapp"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if len(os.Args) > 1 && os.Args[1] == "decrypt" {
		if len(os.Args) != 3 {
			log.Fatalf("Usage: sdl-db-backup decrypt <artifact.sql.gz.enc>")
		}
		cfg, err := backupapp.LoadEffectiveConfig(os.Getenv("BACKUP_ENV_FILE"))
		if err != nil {
			log.Fatalf("Load configuration for decrypt: %v", err)
		}
		if cfg.EncryptionKey == "" {
			log.Fatal("BACKUP_ENCRYPTION_KEY is required to decrypt an artifact")
		}
		file, err := os.Open(os.Args[2])
		if err != nil {
			log.Fatalf("Open encrypted artifact: %v", err)
		}
		defer file.Close()
		if err := backupapp.DecryptArtifactTo(file, os.Stdout, cfg.EncryptionKey); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				log.Fatalf("Decrypt artifact: truncated artifact: %v", err)
			}
			log.Fatalf("Decrypt artifact: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "physical-validate" {
		if len(os.Args) != 3 {
			log.Fatalf("Usage: sdl-db-backup physical-validate <run-id>")
		}
		cfg, err := backupapp.LoadEffectiveConfig(os.Getenv("BACKUP_ENV_FILE"))
		if err != nil {
			log.Fatalf("Load configuration for physical validation: %v", err)
		}
		result, err := backupapp.ValidatePhysicalRun(cfg, os.Args[2], func(line string) {
			log.Printf("physical validation: %s", line)
		})
		if err != nil {
			log.Fatalf("Physical validation failed: %v", err)
		}
		if !result.Valid {
			log.Fatalf("Physical validation failed: %s", result.Error)
		}
		fmt.Printf("Physical validation succeeded for run %s (%s)\n", result.RunID, result.ObjectKey)
		return
	}
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
