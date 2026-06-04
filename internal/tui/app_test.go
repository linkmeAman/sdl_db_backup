package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"sdl/sdl_db_backup/internal/backupapp"
)

func TestRunProgressStateTracksDatabaseProgress(t *testing.T) {
	var progress runProgressState
	progress.applyLogLine("2026/05/28 21:56:10.631283 found 4 databases: a, b, c, d")
	if progress.total != 4 {
		t.Fatalf("expected total=4, got %d", progress.total)
	}
	progress.applyLogLine("2026/05/28 21:56:10.631342 [1/4] processing a")
	if progress.current != 1 || progress.currentDB != "a" {
		t.Fatalf("unexpected current progress: %+v", progress)
	}
	inProgress := progress.percent()
	if inProgress <= 0 || inProgress >= 0.25 {
		t.Fatalf("expected in-progress percent between 0 and 0.25, got %f", inProgress)
	}
	progress.applyLogLine("2026/05/28 21:56:15.395220 completed dump for database=a output=/tmp/a.sql.gz size_bytes=123")
	if progress.completed != 1 {
		t.Fatalf("expected completed=1, got %d", progress.completed)
	}
	if progress.percent() != 0.25 {
		t.Fatalf("expected 25%% complete, got %f", progress.percent())
	}
	progress.applyLogLine("2026/05/28 21:56:22.660696 [2/4] processing b")
	if progress.percent() <= 0.25 || progress.percent() >= 0.50 {
		t.Fatalf("expected second database in-progress percent between 0.25 and 0.50, got %f", progress.percent())
	}
}

func TestScheduleChoicesExposePresetOptions(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	logical := m.scheduleChoices("logical_schedule")
	if len(logical) == 0 {
		t.Fatalf("expected logical schedule presets")
	}
	foundFourPerDay := false
	for _, choice := range logical {
		if choice.value == "daily@00:00,06:00,12:00,18:00" {
			foundFourPerDay = true
		}
	}
	if !foundFourPerDay {
		t.Fatalf("expected four-per-day logical preset, choices=%v", logical)
	}

	retention := m.scheduleChoices("retention")
	if len(retention) == 0 {
		t.Fatalf("expected retention presets")
	}
}

func TestScopeBulkAndRangeSelection(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.scopeDatabases = []string{"db1", "db2", "db3"}
	m.scopeTables = []string{"alpha", "beta", "gamma", "delta", "epsilon"}

	m.selectAllDatabases()
	for _, dbName := range m.scopeDatabases {
		if !m.scopeSelectedDBs[dbName] {
			t.Fatalf("expected %s to be selected", dbName)
		}
		if len(m.scopeSelectedTables[dbName]) != 0 {
			t.Fatalf("expected %s to include all tables, got %+v", dbName, m.scopeSelectedTables[dbName])
		}
	}

	m.scopeLevel = "tables"
	m.scopeDBIndex = 1
	m.scopeTableIndex = 1
	m.markScopeRangeStart()
	m.scopeTableIndex = 3
	m.selectMarkedScopeRange()
	selected := m.scopeSelectedTables["db2"]
	for _, tableName := range []string{"beta", "gamma", "delta"} {
		if !selected[tableName] {
			t.Fatalf("expected range-selected table %s, got %+v", tableName, selected)
		}
	}

	m.selectAllTablesForCurrentDB()
	if len(m.scopeSelectedTables["db2"]) != len(m.scopeTables) {
		t.Fatalf("expected all tables selected, got %+v", m.scopeSelectedTables["db2"])
	}

	m.scopeTableMark = 1
	m.scopeTableIndex = 2
	m.clearMarkedScopeRange()
	selected = m.scopeSelectedTables["db2"]
	if selected["beta"] || selected["gamma"] {
		t.Fatalf("expected marked range to be cleared, got %+v", selected)
	}
	if !selected["alpha"] || !selected["delta"] || !selected["epsilon"] {
		t.Fatalf("expected tables outside range to remain selected, got %+v", selected)
	}

	m.clearSelectedTablesForCurrentDB()
	if !m.scopeSelectedDBs["db2"] {
		t.Fatalf("expected db2 to stay selected for all-table scope")
	}
	if len(m.scopeSelectedTables["db2"]) != 0 {
		t.Fatalf("expected explicit table checks to be cleared, got %+v", m.scopeSelectedTables["db2"])
	}
}

func TestScopeTableGridUsesMultipleColumnsOnWideScreens(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.height = 28
	rows, cols := m.scopeTableGridShape(120)
	if rows < 4 {
		t.Fatalf("expected usable table rows, got %d", rows)
	}
	if cols < 2 {
		t.Fatalf("expected multiple columns on a wide terminal, got %d", cols)
	}
}

func TestContentViewportScrollKeysMoveLongPages(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.focus = focusContent

	m.activeScreen = screenDashboard
	m.dashboardView.Height = 3
	m.dashboardView.SetContent("one\ntwo\nthree\nfour\nfive")
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if updated.dashboardView.YOffset == 0 {
		t.Fatalf("expected dashboard viewport to scroll down on j")
	}

	m.activeScreen = screenHealth
	m.healthView.Height = 3
	m.healthView.SetContent("one\ntwo\nthree\nfour\nfive")
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if updated.healthView.YOffset != 0 {
		t.Fatalf("expected health viewport to stay at top on k from top, got %d", updated.healthView.YOffset)
	}
	updated.healthView.ScrollDown(2)
	updated, _ = updated.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if updated.healthView.YOffset == 0 {
		t.Fatalf("expected health viewport to scroll up on k")
	}

	m.activeScreen = screenSystemd
	m.systemdView.Height = 3
	m.systemdView.SetContent("one\ntwo\nthree\nfour\nfive")
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if updated.systemdView.YOffset == 0 {
		t.Fatalf("expected systemd viewport to scroll down on down")
	}
}

func TestSystemdFallsBackToActionsWhenViewportCannotScroll(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.activeScreen = screenSystemd
	m.focus = focusContent
	m.systemdView.Height = 200
	m.systemdView.SetContent("short\ncontent")
	m.manualSelected = 0

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if updated.manualSelected != 1 {
		t.Fatalf("expected systemd action selection to move when viewport cannot scroll, got %d", updated.manualSelected)
	}
}

func TestScopeTableSearchFiltersBulkSelection(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.scopeLevel = "tables"
	m.scopeDatabases = []string{"db1"}
	m.scopeTables = []string{"orders", "order_items", "users", "user_roles"}
	m.scopeTableFilter = "order"
	m.ensureScopeTableVisible()

	visible := m.visibleScopeTableIndexes()
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible order tables, got %d", len(visible))
	}

	m.selectAllTablesForCurrentDB()
	selected := m.scopeSelectedTables["db1"]
	if !selected["orders"] || !selected["order_items"] {
		t.Fatalf("expected filtered tables to be selected, got %+v", selected)
	}
	if selected["users"] || selected["user_roles"] {
		t.Fatalf("did not expect hidden tables to be selected, got %+v", selected)
	}
}

func TestScopeTableBackKeysReturnToDatabases(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.backupStep = stepScope
	m.scopeLevel = "tables"
	m.focus = focusContent
	updated, _ := m.handleBackupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if updated.scopeLevel != "databases" {
		t.Fatalf("expected b to return to databases, got %s", updated.scopeLevel)
	}

	m.scopeLevel = "tables"
	updated, _ = m.handleBackupKey(tea.KeyMsg{Type: tea.KeyLeft})
	if updated.scopeLevel != "tables" {
		t.Fatalf("expected left to stay in table grid, got %s", updated.scopeLevel)
	}

	updated, _ = updated.handleBackupKey(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.scopeLevel != "databases" {
		t.Fatalf("expected esc to return to databases, got %s", updated.scopeLevel)
	}
}

func TestSaveScopePermanentlyAppliesScopeToDraft(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.scopeDatabases = []string{"db1", "db2"}
	m.scopeSelectedDBs = map[string]bool{"db1": true, "db2": true}
	m.scopeSelectedTables = map[string]map[string]bool{
		"db2": {"orders": true, "users": true},
	}

	updated, _ := m.saveScopePermanently()
	if len(updated.draft.LogicalDatabases) != 2 || updated.draft.LogicalDatabases[0] != "db1" || updated.draft.LogicalDatabases[1] != "db2" {
		t.Fatalf("expected selected databases in draft, got %+v", updated.draft.LogicalDatabases)
	}
	tables := updated.draft.LogicalTables["db2"]
	if len(tables) != 2 || tables[0] != "orders" || tables[1] != "users" {
		t.Fatalf("expected selected tables in draft, got %+v", updated.draft.LogicalTables)
	}
}

func TestConfigNavigationUsesLocalGAndVisibleContent(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.activeScreen = screenConfig
	m.focus = focusContent
	m.configView.Height = 3
	m.configSelected = 5
	m.configView.SetYOffset(5)

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if updated.activeScreen != screenConfig {
		t.Fatalf("expected g to stay on config page, got %v", updated.activeScreen)
	}
	if updated.configSelected != 0 {
		t.Fatalf("expected g to move to first config row, got %d", updated.configSelected)
	}
	if updated.configView.YOffset != 0 {
		t.Fatalf("expected config viewport at top, got %d", updated.configView.YOffset)
	}

	lines, _ := updated.configContentLines(updated.visibleFields())
	if len(lines) == 0 {
		t.Fatalf("expected config content")
	}
	if lines[0] == "" {
		t.Fatalf("did not expect config content to start with a blank line")
	}
}

func TestConfigNavigationKeepsSelectedRowVisible(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.configView.Height = 2
	visible := m.visibleFields()
	if len(visible) < 4 {
		t.Fatalf("expected several config fields")
	}

	for i := 0; i < 4; i++ {
		var cmd tea.Cmd
		m, cmd = m.handleConfigKey(tea.KeyMsg{Type: tea.KeyDown})
		if cmd != nil {
			t.Fatalf("did not expect command from config navigation")
		}
	}
	_, selectedLine := m.configContentLines(visible)
	if selectedLine < m.configView.YOffset || selectedLine >= m.configView.YOffset+m.configView.Height {
		t.Fatalf("selected line %d not visible in offset=%d height=%d", selectedLine, m.configView.YOffset, m.configView.Height)
	}
}

func TestNotificationsRenderOnEveryPage(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.width = 100
	m.height = 30
	m.activeScreen = screenBackup
	m.toasts = []toast{{level: "ok", text: "scope saved"}}

	view := m.View()
	if !strings.Contains(view, "Notifications") || !strings.Contains(view, "scope saved") {
		t.Fatalf("expected global notification popup, got:\n%s", view)
	}
}

func TestLayoutReservesSpaceForNotifications(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.width = 120
	m.height = 24
	m.activeScreen = screenHealth
	m.toasts = []toast{
		{level: "info", text: "opened History"},
		{level: "info", text: "opened Health"},
		{level: "ok", text: "health refreshed"},
	}

	view := m.View()
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("expected rendered height <= terminal height %d, got %d\n%s", m.height, got, view)
	}
}

func TestConfigFieldsIncludeAPIAndSystemdSettings(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	foundAPI := false
	foundServiceUnit := false
	for _, field := range m.fields {
		if field.Key == "BACKUP_API_ENABLED" {
			foundAPI = true
		}
		if field.Key == "BACKUP_SYSTEMD_SERVICE_NAME" {
			foundServiceUnit = true
		}
	}
	if !foundAPI || !foundServiceUnit {
		t.Fatalf("expected API and systemd config fields, got %+v", m.fields)
	}
}

func TestMissingLogFileShowsInformationalMessage(t *testing.T) {
	msg := loadLogs("daily", "/tmp/sdl-db-backup-test-missing.log")()
	logs, ok := msg.(logsMsg)
	if !ok {
		t.Fatalf("expected logsMsg, got %T", msg)
	}
	if logs.err != nil {
		t.Fatalf("did not expect missing log to be an error: %v", logs.err)
	}
	if !logs.missing {
		t.Fatalf("expected missing flag")
	}
	if len(logs.lines) == 0 || !strings.Contains(logs.lines[0], "No daily log exists yet") {
		t.Fatalf("unexpected missing log message: %+v", logs.lines)
	}
}

func TestScopeActionsNotifyOperator(t *testing.T) {
	m := newModel(".env", zeroConfigForTUITest())
	m.toasts = nil
	m.backupStep = stepScope
	m.scopeLevel = "databases"
	m.scopeDatabases = []string{"db1", "db2"}

	updated, _ := m.handleScopeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if len(updated.toasts) == 0 {
		t.Fatalf("expected scope action notification")
	}
	last := updated.toasts[len(updated.toasts)-1]
	if last.level != "info" || !strings.Contains(last.text, "selected all listed databases") {
		t.Fatalf("unexpected notification: %+v", last)
	}
}

func zeroConfigForTUITest() backupapp.Config {
	return backupapp.Config{
		LogicalSchedule:  "daily@02:00",
		PhysicalSchedule: "weekly@sun,02:00",
		RetentionDays:    5,
	}
}
