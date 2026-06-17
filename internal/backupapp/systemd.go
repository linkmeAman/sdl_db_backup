package backupapp

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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
	serviceName := cfg.ServiceUnitName
	timerName := cfg.TimerUnitName
	readUnit := func(name string) (UnitState, error) {
		cmd := exec.CommandContext(
			ctx,
			"systemctl",
			"--user",
			"show",
			name,
			"--property=LoadState,ActiveState,SubState,UnitFileState",
		)
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
	return status, nil
}

func RunSystemdAction(ctx context.Context, envPath string, action SystemdAction) error {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return err
	}
	serviceName := cfg.ServiceUnitName
	timerName := cfg.TimerUnitName
	args := []string{"--user"}
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
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
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
	cmd := exec.CommandContext(
		ctx,
		"journalctl",
		"--user",
		"-u",
		unit,
		"-n",
		strconv.Itoa(lines),
		"--no-pager",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
