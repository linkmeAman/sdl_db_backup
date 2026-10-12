package backupapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ApplySystemdSchedule is an explicitly invoked operation. Ordinary config
// saves remain non-disruptive and report drift until this action is authorized.
func ApplySystemdSchedule(ctx context.Context, envPath string, cfg Config) error {
	scope, e := resolveSystemdScope(cfg)
	if e != nil {
		return e
	}
	dir := "/etc/systemd/system"
	if scope == "user" {
		base, e := os.UserConfigDir()
		if e != nil {
			return e
		}
		dir = filepath.Join(base, "systemd/user")
	}
	timer, e := RenderTimerFromConfig(cfg)
	if e != nil {
		return e
	}
	dropin := "[Timer]\nOnCalendar=\nOnUnitActiveSec=\nOnUnitInactiveSec=\nOnBootSec=\nOnStartupSec=\n"
	for _, line := range strings.Split(timer, "\n") {
		if strings.HasPrefix(line, "OnCalendar=") {
			dropin += line + "\n"
		}
	}
	path := filepath.Join(dir, cfg.TimerUnitName+".d", "90-sdl-schedule.conf")
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "systemctl", systemdArgs(scope, args...)...)
		b, e := cmd.CombinedOutput()
		if e != nil {
			return "", fmt.Errorf("systemd %s authorization or unit failure: %s", scope, strings.TrimSpace(string(b)))
		}
		return string(b), nil
	}
	validate := func(calendar string) error {
		cmd := exec.CommandContext(ctx, "systemd-analyze", "calendar", calendar)
		if b, e := cmd.CombinedOutput(); e != nil {
			return fmt.Errorf("invalid calendar: %s", b)
		}
		return nil
	}
	return applyScheduleTransaction(envPath, cfg, path, dropin, run, validate)
}
func applyScheduleTransaction(envPath string, cfg config, path, dropin string, run func(...string) (string, error), validate func(string) error) error {
	for _, line := range strings.Split(dropin, "\n") {
		if strings.HasPrefix(line, "OnCalendar=") && len(line) > len("OnCalendar=") {
			if e := validate(strings.TrimPrefix(line, "OnCalendar=")); e != nil {
				return e
			}
		}
	}
	release, e := acquireLock(ResolveEnvFilePath(envPath) + ".schedule.lock")
	if e != nil {
		return e
	}
	defer release()
	oldTimer, e := os.ReadFile(path)
	hadTimer := e == nil
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	oldEnv, e := os.ReadFile(ResolveEnvFilePath(envPath))
	if e != nil {
		return e
	}
	state, e := run("show", cfg.TimerUnitName, "--property=ActiveState")
	if e != nil {
		return e
	}
	active := strings.Contains(state, "ActiveState=active")
	if e = atomicWriteFile(path, []byte(dropin), 0644); e != nil {
		return fmt.Errorf("cannot write authoritative timer drop-in; authorize a narrowly scoped installer: %w", e)
	}
	rollback := func(cause error) error {
		var restore error
		if hadTimer {
			restore = atomicWriteFile(path, oldTimer, 0644)
		} else {
			restore = os.Remove(path)
		}
		restore = errors.Join(restore, atomicWriteFile(ResolveEnvFilePath(envPath), oldEnv, 0600))
		_, reload := run("daemon-reload")
		restore = errors.Join(restore, reload)
		if active {
			_, restart := run("restart", cfg.TimerUnitName)
			restore = errors.Join(restore, restart)
		}
		return errors.Join(cause, restore)
	}
	if e = SaveConfig(envPath, cfg); e != nil {
		return rollback(e)
	}
	if _, e = run("daemon-reload"); e != nil {
		return rollback(e)
	}
	if active {
		if _, e = run("restart", cfg.TimerUnitName); e != nil {
			return rollback(e)
		}
	}
	effective, e := run("show", cfg.TimerUnitName, "--property=TimersCalendar")
	if e != nil {
		return rollback(e)
	}
	if calendarConfigDrift(cfg, effective) {
		return rollback(fmt.Errorf("installed timer failed schedule verification"))
	}
	audit := fmt.Sprintf("applied_at=%s scope=%s timer=%s schedule=%s\n", time.Now().UTC().Format(time.RFC3339), cfg.SystemdScope, cfg.TimerUnitName, cfg.LogicalSchedule)
	return atomicWriteFile(ResolveEnvFilePath(envPath)+".schedule-audit", []byte(audit), 0600)
}
