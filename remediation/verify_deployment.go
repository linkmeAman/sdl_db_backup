package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sdl/sdl_db_backup/internal/backupapp"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := backupapp.GetSystemdStatus(ctx, ".env")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("remediation/evidence/deployed-systemd-status.json", append(data, '\n'), 0600); err != nil {
		panic(err)
	}
	fmt.Println(string(data))
}
