package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sdl/sdl_db_backup/internal/backupapp"
	"time"
)

func main() {
	env := flag.String("env", ".env", "configuration source")
	apply := flag.Bool("apply", false, "explicitly authorize timer transaction; default is render only")
	flag.Parse()
	cfg, e := backupapp.LoadEffectiveConfig(*env)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if !*apply {
		render, e := backupapp.RenderTimerFromConfig(cfg)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		fmt.Print(render)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if e := backupapp.ApplySystemdSchedule(ctx, *env, cfg); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fmt.Println("timer applied and effective schedule verified")
}
