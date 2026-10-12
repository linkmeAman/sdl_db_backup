package backupapp

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func parseSystemdShow(output string, unitName string) UnitState {
	state := UnitState{Name: unitName}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "TimersCalendar":
			state.Calendar = strings.TrimSpace(state.Calendar + " " + value)
		case "NextElapseUSecRealtime":
			state.NextTrigger = value
		case "LastTriggerUSec":
			state.LastTrigger = value
		case "Result":
			state.Result = value
		case "FragmentPath":
			state.FragmentPath = value
		case "Environment":
			for _, field := range strings.Fields(value) {
				if strings.HasPrefix(strings.Trim(field, "\""), "BACKUP_ENV_FILE=") {
					state.ConfigSource = strings.Trim(strings.TrimPrefix(strings.Trim(field, "\""), "BACKUP_ENV_FILE="), "\"")
				}
			}
		case "LoadState":
			state.LoadState = value
		case "ActiveState":
			state.Active = value
		case "SubState":
			state.SubState = value
		case "UnitFileState":
			state.Enabled = value
		}
	}
	return state
}

func GetSystemdStatus(ctx context.Context, envPath string) (UnitStatus, error) {
	status := UnitStatus{}
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return status, err
	}
	scope, scopeErr := resolveSystemdScope(cfg)
	if scopeErr != nil {
		return status, scopeErr
	}
	status.Scope = scope
	status.DesiredSchedule = cfg.LogicalSchedule
	serviceName := cfg.ServiceUnitName
	timerName := cfg.TimerUnitName
	readUnit := func(name string) (UnitState, error) {
		args := systemdArgs(scope, "show", name, "--property=LoadState,ActiveState,SubState,UnitFileState,TimersCalendar,NextElapseUSecRealtime,LastTriggerUSec,Result,FragmentPath,Environment")
		cmd := exec.CommandContext(ctx, "systemctl", args...)

		out, err := cmd.CombinedOutput()
		if err != nil {
			return UnitState{Name: name}, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(out)))
		}
		return parseSystemdShow(string(out), name), nil
	}

	status.Service, err = readUnit(serviceName)
	if err != nil {
		return status, err
	}
	status.Timer, err = readUnit(timerName)
	if err != nil {
		return status, err
	}
	status.ScheduleDrift = calendarConfigDrift(cfg, status.Timer.Calendar)
	if status.Service.ConfigSource != "" {
		want, _ := filepath.Abs(ResolveEnvFilePath(envPath))
		actual, _ := filepath.Abs(status.Service.ConfigSource)
		if want != actual {
			status.ScheduleDrift = true
		}
	}
	return status, nil
}

func RunSystemdAction(ctx context.Context, envPath string, action SystemdAction) error {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return err
	}
	if action == SystemdApplySchedule {
		return ApplySystemdSchedule(ctx, envPath, cfg)
	}
	serviceName := cfg.ServiceUnitName
	timerName := cfg.TimerUnitName
	scope, e := resolveSystemdScope(cfg)
	if e != nil {
		return e
	}
	args := systemdArgs(scope)
	switch action {
	case SystemdDaemonReload:
		args = append(args, "daemon-reload")
	case SystemdEnableTimer:
		args = append(args, "enable", "--now", timerName)
	case SystemdDisableTimer:
		args = append(args, "disable", "--now", timerName)
	case SystemdStartTimer:
		args = append(args, "start", timerName)
	case SystemdStopTimer:
		args = append(args, "stop", timerName)
	case SystemdRestartSvc:
		args = append(args, "restart", serviceName)
	case SystemdStartSvc:
		args = append(args, "start", serviceName)
	case SystemdStopSvc:
		args = append(args, "stop", serviceName)
	default:
		return fmt.Errorf("unsupported systemd action %q", action)
	}
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd %s action failed (authorization or unit error; no fallback): %s", scope, strings.TrimSpace(string(out)))
	}
	return nil
}

func isIgnorablePipeReadError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "file already closed") || strings.Contains(text, "read |0:")
}

func LoadRecentJournal(ctx context.Context, unit string, lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	args := []string{}
	if !fileExists(filepath.Join("/etc/systemd/system", unit)) {
		args = append(args, "--user")
	}
	args = append(args,
		"-u",
		unit,
		"-n",
		strconv.Itoa(lines),
		"--no-pager",
	)
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func resolveSystemdScope(cfg config) (string, error) {
	for _, name := range []string{cfg.ServiceUnitName, cfg.TimerUnitName} {
		if !regexp.MustCompile(`^[a-zA-Z0-9_.@-]+\.(service|timer)$`).MatchString(name) {
			return "", fmt.Errorf("invalid systemd unit name")
		}
	}
	switch cfg.SystemdScope {
	case "system", "user":
		return cfg.SystemdScope, nil
	case "", "auto":
		if fileExists(filepath.Join("/etc/systemd/system", cfg.ServiceUnitName)) {
			return "system", nil
		}
		return "user", nil
	default:
		return "", fmt.Errorf("BACKUP_SYSTEMD_SCOPE must be system, user or auto")
	}
}
func systemdArgs(scope string, args ...string) []string {
	if scope == "user" {
		return append([]string{"--user"}, args...)
	}
	return args
}
func calendarDrift(schedule, installed string) bool {
	return calendarConfigDrift(config{LogicalSchedule: schedule, PhysicalSchedule: "disabled"}, installed)
}
func calendarConfigDrift(cfg config, installed string) bool {
	desired, err := RenderTimerFromConfig(cfg)
	if err != nil {
		return true
	}
	calendars := func(input string) map[string]bool {
		out := map[string]bool{}
		for _, match := range regexp.MustCompile(`OnCalendar=([^;}\n]+)`).FindAllStringSubmatch(input, -1) {
			out[strings.Join(strings.Fields(match[1]), " ")] = true
		}
		return out
	}
	want, have := calendars(desired), calendars(installed)
	if len(want) != len(have) {
		return true
	}
	for calendar := range want {
		if !have[calendar] {
			return true
		}
	}
	return false
}

// RenderTimerFromConfig produces a reviewable timer; writing live units is a
// separate operator-authorized deployment step, never a side effect of saving.
func RenderTimerFromConfig(cfg Config) (string, error) {
	zone := cfg.ScheduleTimezone
	if zone == "" || zone == "Local" {
		zone = ""
	} else {
		if _, e := time.LoadLocation(zone); e != nil {
			return "", fmt.Errorf("invalid schedule timezone: %w", e)
		}
		zone = " " + zone
	}
	lines := []string{"[Unit]", "Description=Run SDL DB backup scheduler", "", "[Timer]"}
	calendars := map[string]bool{}
	for _, schedule := range []string{cfg.LogicalSchedule, cfg.PhysicalSchedule} {
		if schedule == "" || schedule == "disabled" {
			continue
		}
		if strings.HasPrefix(schedule, "daily@") {
			times, e := parseDailyTimes(strings.TrimPrefix(schedule, "daily@"))
			if e != nil {
				return "", e
			}
			for _, at := range times {
				calendars[fmt.Sprintf("*-*-* %02d:%02d:00", int(at.Hours()), int(at.Minutes())%60)] = true
			}
		} else if strings.HasPrefix(schedule, "weekly@") {
			parts := strings.Split(strings.TrimPrefix(schedule, "weekly@"), ",")
			if len(parts) != 2 {
				return "", fmt.Errorf("invalid weekly schedule")
			}
			day, e := parseWeekday(parts[0])
			if e != nil {
				return "", e
			}
			h, m, e := parseClock(parts[1])
			if e != nil {
				return "", e
			}
			calendars[fmt.Sprintf("%s *-*-* %02d:%02d:00", day.String()[:3], h, m)] = true
		} else {
			return "", fmt.Errorf("schedule %q requires an explicitly reviewed polling timer", schedule)
		}
	}
	ordered := make([]string, 0, len(calendars))
	for c := range calendars {
		ordered = append(ordered, c)
	}
	sort.Strings(ordered)
	for _, c := range ordered {
		lines = append(lines, "OnCalendar="+c+zone)
	}
	lines = append(lines, "Persistent=true", "AccuracySec=1min", "Unit="+cfg.ServiceUnitName, "", "[Install]", "WantedBy=timers.target", "")
	return strings.Join(lines, "\n"), nil
}
