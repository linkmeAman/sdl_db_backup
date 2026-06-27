package tui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"sdl/sdl_db_backup/internal/backupapp"
)

type commandDef struct {
	id    string
	label string
}

func (m model) commands() []commandDef {
	context := []commandDef{}
	switch m.activeScreen {
	case screenSchedule:
		context = append(context, commandDef{"schedule-save", "Save schedule to .env"})
	case screenConfig:
		context = append(context, commandDef{"save", "Save config"})
	case screenLogs:
		context = append(context, commandDef{"daily-log", "Load daily log"}, commandDef{"run-index", "Load run index"})
	case screenBackup:
		context = append(context, commandDef{"backup", "Start manual backup workflow"})
	case screenHealth:
		context = append(context, commandDef{"health", "Refresh health"})
	case screenObservability:
		context = append(context, commandDef{"observability", "Refresh observability"})
	case screenSystemd:
		context = append(context, commandDef{"systemd-refresh", "Refresh systemd status"})
	}
	global := []commandDef{
		{"dashboard", "Go to dashboard"},
		{"backup", "Start manual backup workflow"},
		{"schedule", "Open schedule manager"},
		{"config", "Open config editor"},
		{"logs", "Open logs"},
		{"history", "Open run history"},
		{"health", "Refresh health"},
		{"observability", "Open observability"},
		{"systemd", "Open systemd"},
		{"save", "Save config"},
		{"rotate-api-token", "Rotate API bearer token"},
		{"toggle-api-auth", "Toggle API auth"},
		{"daily-log", "Load daily log"},
		{"run-index", "Load run index"},
		{"quit", "Quit"},
	}
	seen := map[string]bool{}
	commands := []commandDef{}
	for _, command := range append(context, global...) {
		if seen[command.id] {
			continue
		}
		seen[command.id] = true
		commands = append(commands, command)
	}
	return commands
}

func (m model) filteredCommands() []commandDef {
	query := strings.ToLower(strings.TrimSpace(m.commandInput.Value()))
	commands := []commandDef{}
	for _, command := range m.commands() {
		label := strings.ToLower(command.label)
		if query == "" || strings.Contains(label, query) || strings.Contains(command.id, query) || fuzzyMatch(label, query) || fuzzyMatch(command.id, query) {
			commands = append(commands, command)
		}
	}
	if m.commandIndex >= len(commands) {
		m.commandIndex = max(0, len(commands)-1)
	}
	return commands
}

func fuzzyMatch(text, query string) bool {
	if query == "" {
		return true
	}
	pos := 0
	for _, r := range query {
		found := false
		for pos < len(text) {
			if rune(text[pos]) == r {
				found = true
				pos++
				break
			}
			pos++
		}
		if !found {
			return false
		}
	}
	return true
}

func (m model) runCommand(id string) (model, tea.Cmd) {
	switch id {
	case "dashboard":
		m.setScreen(screenDashboard)
	case "backup":
		m.setScreen(screenBackup)
	case "schedule":
		m.setScreen(screenSchedule)
	case "config":
		m.setScreen(screenConfig)
	case "logs":
		m.setScreen(screenLogs)
	case "history":
		m.setScreen(screenHistory)
		return m, loadHistory(m.cfg.RunLogPath)
	case "health":
		m.setScreen(screenHealth)
		return m, loadHealth(m.envPath)
	case "observability":
		m.setScreen(screenObservability)
		return m, loadHealth(m.envPath)
	case "systemd":
		m.setScreen(screenSystemd)
		return m, loadSystemd(m.envPath)
	case "save":
		if m.dirty {
			return m, saveConfig(m.envPath, m.draft)
		}
		m.pushToast("info", "no config changes to save")
	case "rotate-api-token":
		token, err := backupapp.GenerateBearerToken()
		if err != nil {
			m.pushToast("error", "could not generate API token: "+err.Error())
			return m, nil
		}
		m.draft.APIBearerToken = token
		m.fields = buildConfigFields(m.draft)
		m.dirty = true
		m.pushToast("ok", "generated new API bearer token in draft config")
	case "toggle-api-auth":
		m.draft.APIAuthEnabled = !m.draft.APIAuthEnabled
		m.fields = buildConfigFields(m.draft)
		m.dirty = true
		m.pushToast("ok", "API auth set to "+strconv.FormatBool(m.draft.APIAuthEnabled))
	case "schedule-save":
		m.scheduleModeTemp = false
		return m.applyScheduleChanges()
	case "daily-log":
		m.setScreen(screenLogs)
		return m, loadLogs("daily", filepath.Join(m.cfg.LogDir, time.Now().Format("2006-01-02")+".log"))
	case "run-index":
		m.setScreen(screenLogs)
		return m, loadLogs("run index", m.cfg.RunLogPath)
	case "systemd-refresh":
		m.setScreen(screenSystemd)
		return m, loadSystemd(m.envPath)
	case "quit":
		if m.dirty {
			m.confirm("Discard unsaved config changes and quit?", func() tea.Cmd { return tea.Quit })
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

type logChannelWriter struct {
	ch chan<- string
}

func (w logChannelWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		w.ch <- line
	}
	return len(p), nil
}

func waitForRunLog(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return runLogsClosedMsg{}
		}
		return runLogMsg{line: line}
	}
}

func runBackup(cfg backupapp.Config, ch chan string) tea.Cmd {
	return func() tea.Msg {
		result, err := backupapp.RunBackup(context.Background(), cfg, backupapp.RunSinks{Console: logChannelWriter{ch: ch}})
		close(ch)
		return runFinishedMsg{result: result, err: err}
	}
}

func loadHealth(envPath string) tea.Cmd {
	return func() tea.Msg {
		report, err := backupapp.GetHealthReport(context.Background(), envPath)
		return healthMsg{report: report, err: err}
	}
}

func loadSystemd(envPath string) tea.Cmd {
	return func() tea.Msg {
		status, err := backupapp.GetSystemdStatus(context.Background(), envPath)
		return systemdMsg{status: status, err: err}
	}
}

func loadHistory(path string) tea.Cmd {
	return func() tea.Msg {
		runs, err := readRunHistory(path)
		return historyMsg{runs: runs, err: err}
	}
}

func loadScopeDatabases(cfg backupapp.Config) tea.Cmd {
	return func() tea.Msg {
		databases, err := backupapp.ListLogicalDatabases(cfg)
		return scopeDatabasesMsg{databases: databases, err: err}
	}
}

func loadScopeTables(cfg backupapp.Config, dbName string) tea.Cmd {
	return func() tea.Msg {
		tables, err := backupapp.ListLogicalTables(cfg, dbName)
		return scopeTablesMsg{dbName: dbName, tables: tables, err: err}
	}
}

func loadLogs(source, path string) tea.Cmd {
	return func() tea.Msg {
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				lines := []string{
					fmt.Sprintf("No %s log exists yet.", source),
					"Path: " + path,
					"Run a backup or wait for the scheduled service to create this log.",
				}
				return logsMsg{source: source, lines: lines, missing: true}
			}
			return logsMsg{source: source, err: err}
		}
		defer file.Close()
		var ring [1000]string
		var count int
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 1024), 1024*1024)
		for scanner.Scan() {
			ring[count%1000] = scanner.Text()
			count++
		}
		if err := scanner.Err(); err != nil {
			return logsMsg{source: source, err: err}
		}

		var lines []string
		if count > 1000 {
			lines = make([]string, 0, 1000)
			lines = append(lines, ring[count%1000:]...)
			lines = append(lines, ring[:count%1000]...)
		} else {
			lines = make([]string, count)
			copy(lines, ring[:count])
		}

		if count > 1000 {
			lines = append([]string{fmt.Sprintf("... (showing last 1000 lines of %d total lines) ...", count)}, lines...)
		}

		return logsMsg{source: source, lines: lines, err: nil}
	}
}

func saveConfig(envPath string, cfg backupapp.Config) tea.Cmd {
	return func() tea.Msg {
		return saveConfigMsg{cfg: cfg, err: backupapp.SaveConfig(envPath, cfg)}
	}
}

func saveSchedulePermanently(envPath string, cfg backupapp.Config) tea.Cmd {
	return func() tea.Msg {
		return temporaryOverridesSavedMsg{action: "permanent", err: backupapp.SaveConfig(envPath, cfg)}
	}
}

func loadTemporaryOverrides(envPath string) tea.Cmd {
	return func() tea.Msg {
		overrides, active, err := backupapp.LoadTemporaryOverrides(envPath)
		return temporaryOverridesMsg{overrides: overrides, active: active, err: err}
	}
}

func saveTemporaryOverrides(envPath string, overrides backupapp.TemporaryOverrides) tea.Cmd {
	return func() tea.Msg {
		return temporaryOverridesSavedMsg{action: "temporary", err: backupapp.SaveTemporaryOverrides(envPath, overrides)}
	}
}

func clearTemporaryOverrides(envPath string) tea.Cmd {
	return func() tea.Msg {
		return temporaryOverridesSavedMsg{action: "clear", err: backupapp.ClearTemporaryOverrides(envPath)}
	}
}

func reloadConfig(envPath string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := backupapp.LoadConfig(envPath)
		if err != nil {
			return reloadConfigMsg{err: err}
		}
		return reloadConfigMsg{cfg: cfg}
	}
}

func runSystemd(envPath, label string, action backupapp.SystemdAction) tea.Cmd {
	return func() tea.Msg {
		return systemdActionMsg{action: label, err: backupapp.RunSystemdAction(context.Background(), envPath, action)}
	}
}

func readRunHistory(path string) ([]backupapp.RunResult, error) {
	return backupapp.ReadRunHistory(path)
}

func validateLogicalRunCmd(cfg backupapp.Config, runID string) tea.Cmd {
	return func() tea.Msg {
		res, err := backupapp.ValidateLogicalRun(cfg, runID)
		return validationMsg{mode: "logical validation", result: res, err: err}
	}
}

func testRestoreRunCmd(cfg backupapp.Config, runID string) tea.Cmd {
	return func() tea.Msg {
		// Pass nil for progress currently since tea.Cmd is synchronous
		res, err := backupapp.FullRestoreValidation(cfg, runID, nil)
		return validationMsg{mode: "sandbox restore test", result: res, err: err}
	}
}
