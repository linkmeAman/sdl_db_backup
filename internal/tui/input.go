package tui

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"sdl/sdl_db_backup/internal/backupapp"
)

func (m model) handleKey(key tea.KeyMsg) (model, tea.Cmd) {
	if m.helpOpen {
		switch key.String() {
		case "esc", "q", "?", "enter":
			m.helpOpen = false
			m.notifyAction("closed help")
		}
		return m, nil
	}

	if m.confirmMessage != "" {
		switch key.String() {
		case "left", "h":
			m.confirmSelected = 1
			return m, nil
		case "right", "l":
			m.confirmSelected = 0
			return m, nil
		case "y":
			action := m.confirmAction
			m.confirmMessage = ""
			m.confirmAction = nil
			m.confirmSelected = 0
			m.notifyAction("confirmed action")
			if action != nil {
				return m, action()
			}
			return m, nil
		case "enter":
			if m.confirmSelected == 1 {
				action := m.confirmAction
				m.confirmMessage = ""
				m.confirmAction = nil
				m.confirmSelected = 0
				m.notifyAction("confirmed action")
				if action != nil {
					return m, action()
				}
				return m, nil
			}
			fallthrough
		case "n", "esc":
			m.confirmMessage = ""
			m.confirmAction = nil
			m.confirmSelected = 0
			m.notifyAction("cancelled action")
			return m, nil
		}
		return m, nil
	}

	if m.commandOpen {
		return m.handleCommandPalette(key)
	}
	if m.editorActive {
		return m.handleEditor(key)
	}
	if m.scheduleChoosing {
		return m.handleScheduleChoice(key)
	}
	if m.scheduleEditing {
		return m.handleScheduleEditor(key)
	}
	if m.searchActive && m.activeScreen == screenConfig {
		return m.handleConfigSearch(key)
	}
	if m.logSearchMode {
		return m.handleLogSearch(key)
	}
	if m.scopeSearchActive {
		return m.handleScopeTableSearch(key)
	}

	if m.focus == focusContent {
		if updated, cmd, handled := m.handleContentViewportKey(key); handled {
			return updated, cmd
		}
	}

	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if m.activeScreen == screenInspect {
			m.setScreen(screenHistory)
			return m, nil
		}
		if m.dirty {
			m.confirm("Discard unsaved config changes and quit?", func() tea.Cmd { return tea.Quit })
			return m, nil
		}
		return m, tea.Quit
	case ":":
		m.commandOpen = true
		m.commandIndex = 0
		m.commandInput.SetValue("")
		m.commandInput.Focus()
		m.notifyAction("opened command palette")
		return m, nil
	case "tab":
		if m.focus == focusContent {
			m.focus = focusSidebar
		} else {
			m.focus = focusContent
		}
		m.notifyAction("focus moved to " + focusName(m.focus))
		return m, nil
	case "shift+tab":
		if m.focus == focusContent {
			m.focus = focusSidebar
		} else {
			m.focus = focusContent
		}
		m.notifyAction("focus moved to " + focusName(m.focus))
		return m, nil
	case "esc":
		if m.activeScreen == screenInspect {
			m.setScreen(screenHistory)
			return m, nil
		}
		if m.activeScreen == screenBackup && m.focus == focusContent && m.backupStep == stepScope && m.scopeLevel == "tables" {
			return m.handleBackupKey(key)
		}
		m.focus = focusSidebar
		m.notifyAction("focus moved to sidebar")
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		i, _ := strconv.Atoi(key.String())
		m.setScreen(screenID(i - 1))
		return m, m.screenRefreshCmd()
	case "g":
		if m.activeScreen == screenConfig || m.activeScreen == screenLogs {
			break
		}
		m.setScreen(screenDashboard)
		return m, nil
	case "b":
		m.setScreen(screenBackup)
		return m, nil
	case "?":
		m.helpOpen = true
		m.notifyAction("opened help")
		return m, nil
	case "r":
		m.notifyAction("refresh requested")
		return m, m.screenRefreshCmd()
	case "s", "ctrl+s":
		if m.activeScreen == screenConfig && m.dirty {
			m.notifyAction("saving .env")
			return m, saveConfig(m.envPath, m.draft)
		}
		if m.activeScreen == screenSchedule {
			m.notifyAction("applying schedule changes")
			return m.applyScheduleChanges()
		}
		if m.activeScreen == screenConfig {
			m.pushToast("info", "no config changes to save")
		}
	case "/":
		if m.activeScreen == screenConfig {
			m.searchActive = true
			m.searchInput.Focus()
			m.notifyAction("config search opened")
			return m, nil
		}
		if m.activeScreen == screenLogs {
			m.logSearchMode = true
			m.logSearch.Focus()
			m.notifyAction("log filter opened")
			return m, nil
		}
		if m.activeScreen == screenBackup && m.backupStep == stepScope && m.scopeLevel == "tables" {
			m.scopeSearchActive = true
			m.scopeTableSearch.Focus()
			m.notifyAction("table search opened")
			return m, nil
		}
	}

	if m.focus == focusSidebar {
		return m.handleSidebarKey(key)
	}

	switch m.activeScreen {
	case screenDashboard:
		return m.handleDashboardKey(key)
	case screenBackup:
		return m.handleBackupKey(key)
	case screenSchedule:
		return m.handleScheduleKey(key)
	case screenConfig:
		return m.handleConfigKey(key)
	case screenLogs:
		return m.handleLogsKey(key)
	case screenSystemd:
		return m.handleSystemdKey(key)
	case screenHistory:
		return m.handleHistoryKey(key)
	case screenInspect:
		return m.handleInspectKey(key)
	}
	return m, nil
}

func (m model) handleDashboardKey(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "j", "down":
		m.dashboardSelected++
		if m.dashboardSelected > 6 {
			m.dashboardSelected = 6
		}
	case "k", "up":
		m.dashboardSelected--
		if m.dashboardSelected < 0 {
			m.dashboardSelected = 0
		}
	case "enter":
		switch m.dashboardSelected {
		case 0:
			m.setScreen(screenBackup)
		case 1:
			m.setScreen(screenSchedule)
		case 2:
			m.setScreen(screenConfig)
		case 3:
			m.setScreen(screenLogs)
		case 4:
			m.setScreen(screenHistory)
		case 5:
			m.setScreen(screenHealth)
		case 6:
			m.setScreen(screenObservability)
		}
	}
	return m, nil
}

func (m model) handleContentViewportKey(key tea.KeyMsg) (model, tea.Cmd, bool) {
	switch m.activeScreen {
	case screenHealth:
		if handled := scrollViewportByKey(&m.healthView, key); handled {
			return m, nil, true
		}
	case screenObservability:
		if handled := scrollViewportByKey(&m.observabilityView, key); handled {
			return m, nil, true
		}
	case screenSystemd:
		if handled := scrollViewportByKey(&m.systemdView, key); handled {
			return m, nil, true
		}
	}
	return m, nil, false
}

func scrollViewportByKey(v *viewport.Model, key tea.KeyMsg) bool {
	before := v.YOffset
	switch key.String() {
	case "j", "down":
		v.ScrollDown(1)
	case "k", "up":
		v.ScrollUp(1)
	case "pgdown":
		v.PageDown()
	case "pgup":
		v.PageUp()
	case "home":
		v.GotoTop()
	case "end":
		v.GotoBottom()
	default:
		return false
	}
	return v.YOffset != before
}

func (m model) handleSidebarKey(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "j", "down":
		if m.sidebarSelected < len(screens)-1 {
			m.sidebarSelected++
		}
	case "k", "up":
		if m.sidebarSelected > 0 {
			m.sidebarSelected--
		}
	case "enter", "l", "right":
		m.setScreen(screenID(m.sidebarSelected))
		return m, m.screenRefreshCmd()
	}
	return m, nil
}

func (m model) handleConfigKey(key tea.KeyMsg) (model, tea.Cmd) {
	visible := m.visibleFields()
	if len(visible) == 0 {
		return m, nil
	}
	switch key.String() {
	case "j", "down":
		if m.configSelected < len(visible)-1 {
			m.configSelected++
		}
	case "k", "up":
		if m.configSelected > 0 {
			m.configSelected--
		}
	case "pgdown":
		m.configSelected = min(len(visible)-1, m.configSelected+max(1, m.configView.Height))
	case "pgup":
		m.configSelected = max(0, m.configSelected-max(1, m.configView.Height))
	case "g":
		m.configSelected = 0
	case "G":
		m.configSelected = len(visible) - 1
	case "enter":
		idx := visible[m.configSelected]
		m.startEditing(idx)
		m.notifyAction("editing " + m.fields[idx].Key)
	case "v":
		m.revealSecrets = !m.revealSecrets
		m.notifyAction("secret reveal set to " + strconv.FormatBool(m.revealSecrets))
	case "ctrl+s":
		m.notifyAction("applying config changes")
		return m.applyConfigChanges()
	case "ctrl+r":
		m.notifyAction("reloading .env")
		return m, reloadConfig(m.envPath)
	}
	m.syncConfigViewport(visible)
	return m, nil
}

func (m model) applyConfigChanges() (model, tea.Cmd) {
	if !m.dirty {
		m.pushToast("warning", "no changes to apply")
		return m, nil
	}
	
	err := backupapp.SaveConfig(m.envPath, m.draft)
	if err != nil {
		m.pushToast("error", "failed to save .env: "+err.Error())
		return m, nil
	}
	
	m.dirty = false
	m.pushToast("ok", "saved config to .env")
	// Reload config to ensure everything is synced
	return m, reloadConfig(m.envPath)
}

func (m model) handleEditor(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "enter":
		value := m.editorInput.Value()
		idx := m.editorIndex
		if idx >= 0 && idx < len(m.fields) {
			field := &m.fields[idx]
			if err := field.Apply(&m.draft, value); err != nil {
				field.Error = err.Error()
				m.pushToast("error", field.Key+": "+err.Error())
			} else {
				field.Value = value
				field.Error = ""
				m.dirty = true
				m.pushToast("ok", "updated "+field.Key)
			}
		}
		m.editorActive = false
		m.editorIndex = -1
		m.editorInput.Blur()
	case "esc":
		m.editorActive = false
		m.editorIndex = -1
		m.editorInput.Blur()
	}
	return m, nil
}

func (m model) handleConfigSearch(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "enter", "esc":
		m.searchActive = false
		m.searchInput.Blur()
		if m.configSelected >= len(m.visibleFields()) {
			m.configSelected = 0
		}
	case "ctrl+w":
		m.searchInput.SetValue("")
		m.configSelected = 0
	}
	return m, nil
}

func (m model) handleLogSearch(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "enter", "esc":
		m.logSearchMode = false
		m.logSearch.Blur()
	case "ctrl+w":
		m.logSearch.SetValue("")
		m.logFilter = ""
		m.updateLogViewport()
	}
	return m, nil
}

func (m model) handleScopeTableSearch(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "enter":
		m.scopeSearchActive = false
		m.scopeTableSearch.Blur()
	case "esc":
		m.scopeSearchActive = false
		m.scopeTableSearch.Blur()
	case "ctrl+w":
		m.scopeTableSearch.SetValue("")
		m.scopeTableFilter = ""
		m.scopeTableMark = -1
		m.ensureScopeTableVisible()
	}
	m.rebuildPreview()
	return m, nil
}

func (m model) handleBackupKey(key tea.KeyMsg) (model, tea.Cmd) {
	if m.running {
		return m, nil
	}
	if m.backupStep == stepScope {
		switch key.String() {
		case "n":
		default:
			return m.handleScopeKey(key)
		}
	}
	switch key.String() {
	case "j", "down":
		m.manualSelected++
		if m.manualSelected > m.maxManualSelection() {
			m.manualSelected = m.maxManualSelection()
		}
	case "k", "up":
		if m.manualSelected > 0 {
			m.manualSelected--
		}
	case "h", "left":
		if m.backupStep > stepMode {
			m.backupStep--
			m.manualSelected = 0
			m.rebuildPreview()
		}
	case "n":
		if m.backupStep < stepPreview {
			m.backupStep++
			m.manualSelected = 0
			m.rebuildPreview()
			if m.backupStep == stepScope && len(m.scopeDatabases) == 0 && !m.scopeLoading {
				m.scopeLoading = true
				return m, loadScopeDatabases(m.draft)
			}
		}
	case "enter":
		return m.applyManualSelection()
	}
	return m, nil
}

func (m model) handleScopeKey(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "/":
		if m.scopeLevel == "tables" {
			m.scopeSearchActive = true
			m.scopeTableSearch.Focus()
		}
	case "r":
		m.scopeLoading = true
		m.scopeErr = ""
		m.notifyAction("refreshing backup scope")
		if m.scopeLevel == "tables" && len(m.scopeDatabases) > 0 {
			return m, loadScopeTables(m.draft, m.scopeDatabases[m.scopeDBIndex])
		}
		return m, loadScopeDatabases(m.draft)
	case "ctrl+a", "a", "d":
		if m.scopeLevel == "tables" {
			m.selectAllTablesForCurrentDB()
			m.notifyAction("selected shown tables")
		} else {
			m.selectAllDatabases()
			m.notifyAction("selected all listed databases")
		}
	case "ctrl+x", "c", "u":
		if m.scopeLevel == "tables" {
			if m.scopeTableMark >= 0 {
				m.clearMarkedScopeRange()
				m.notifyAction("cleared marked table range")
			} else {
				m.clearSelectedTablesForCurrentDB()
				m.notifyAction("cleared explicit table checks for current database")
			}
		} else {
			if m.scopeDBMark >= 0 {
				m.clearMarkedScopeRange()
				m.notifyAction("cleared marked database range")
			} else {
				m.scopeSelectedDBs = map[string]bool{}
				m.scopeSelectedTables = map[string]map[string]bool{}
				m.notifyAction("cleared database selections")
			}
		}
	case "ctrl+r":
		m.scopeSelectedDBs = map[string]bool{}
		m.scopeSelectedTables = map[string]map[string]bool{}
		m.notifyAction("scope reset to all granted databases and tables")
	case "ctrl+s":
		return m.saveScopePermanently()
	case "v":
		if (m.scopeLevel == "tables" && m.scopeTableMark >= 0) || (m.scopeLevel == "databases" && m.scopeDBMark >= 0) {
			m.selectMarkedScopeRange()
			m.notifyAction("selected visual range")
		} else {
			m.markScopeRangeStart()
			m.notifyAction("visual range started")
		}
	case "m":
		m.markScopeRangeStart()
		m.notifyAction("range mark set")
	case "s":
		m.selectMarkedScopeRange()
		m.notifyAction("selected marked range")
	case "j", "down":
		if m.scopeLevel == "tables" {
			m.moveScopeSelection(1)
		} else if m.scopeDBIndex < len(m.scopeDatabases)-1 {
			m.scopeDBIndex++
		}
	case "k", "up":
		if m.scopeLevel == "tables" {
			m.moveScopeSelection(-1)
		} else if m.scopeDBIndex > 0 {
			m.scopeDBIndex--
		}
	case "l", "right":
		if m.scopeLevel == "tables" {
			m.moveScopeTableSelectionHorizontal(1)
		} else {
			m.backupStep = stepPreview
			m.manualSelected = 0
			m.notifyAction("continued to preview")
		}
	case "h", "left":
		if m.scopeLevel == "tables" {
			m.moveScopeTableSelectionHorizontal(-1)
		}
	case "n":
		m.backupStep = stepPreview
		m.manualSelected = 0
		m.notifyAction("continued to preview")
	case "pgdown":
		m.moveScopeSelection(10)
	case "pgup":
		m.moveScopeSelection(-10)
	case "home":
		m.moveScopeSelectionToStart()
	case "end":
		m.moveScopeSelectionToEnd()
	case " ", "x":
		if m.scopeLevel == "tables" {
			m.toggleSelectedTable()
			m.notifyAction("toggled table selection")
		} else {
			m.toggleSelectedDatabase()
			m.notifyAction("toggled database selection")
		}
	case "enter":
		if m.scopeLevel == "databases" && len(m.scopeDatabases) > 0 {
			dbName := m.scopeDatabases[m.scopeDBIndex]
			m.scopeLoading = true
			m.notifyAction("loading tables for " + dbName)
			return m, loadScopeTables(m.draft, dbName)
		}
		if m.scopeLevel == "tables" {
			m.backupStep = stepPreview
			m.manualSelected = 0
			m.notifyAction("continued to preview")
		}
	case "esc", "b", "backspace":
		if m.scopeLevel == "tables" {
			m.scopeLevel = "databases"
			m.scopeSearchActive = false
			m.scopeTableSearch.Blur()
			m.notifyAction("returned to database list")
		}
	}
	m.rebuildPreview()
	return m, nil
}

func (m model) handleScheduleKey(key tea.KeyMsg) (model, tea.Cmd) {
	maxSelection := len(scheduleRows()) - 1
	switch key.String() {
	case "j", "down":
		if m.scheduleSelected < maxSelection {
			m.scheduleSelected++
		}
	case "k", "up":
		if m.scheduleSelected > 0 {
			m.scheduleSelected--
		}
	case "enter":
		row := scheduleRows()[m.scheduleSelected]
		switch row.key {
		case "mode":
			m.scheduleModeTemp = !m.scheduleModeTemp
			m.notifyAction("schedule apply mode changed")
		case "duration":
			m.scheduleDuration = (m.scheduleDuration + 1) % len(scheduleDurations())
			m.notifyAction("temporary duration changed")
		case "logical_schedule", "physical_schedule", "retention":
			m.startScheduleChoice(row.key)
			m.notifyAction("opened " + row.label + " choices")
		case "logical_upload":
			m.draft.LogicalS3UploadEnabled = !m.draft.LogicalS3UploadEnabled
			if !m.scheduleModeTemp {
				m.dirty = true
			}
			m.notifyAction("logical upload set to " + strconv.FormatBool(m.draft.LogicalS3UploadEnabled))
		case "physical_upload":
			m.draft.PhysicalS3UploadEnabled = !m.draft.PhysicalS3UploadEnabled
			if !m.scheduleModeTemp {
				m.dirty = true
			}
			m.notifyAction("physical upload set to " + strconv.FormatBool(m.draft.PhysicalS3UploadEnabled))
		case "apply":
			m.notifyAction("applying schedule changes")
			return m.applyScheduleChanges()
		case "clear":
			m.notifyAction("clearing temporary schedule override")
			return m, clearTemporaryOverrides(m.envPath)
		default:
			m.startScheduleChoice(row.key)
			m.notifyAction("opened " + row.label + " choices")
		}
	case "c":
		m.notifyAction("clearing temporary schedule override")
		return m, clearTemporaryOverrides(m.envPath)
	case "a":
		m.notifyAction("applying schedule changes")
		return m.applyScheduleChanges()
	}
	return m, nil
}

func (m model) handleScheduleChoice(key tea.KeyMsg) (model, tea.Cmd) {
	choices := m.scheduleChoices(m.scheduleChoiceKey)
	switch key.String() {
	case "j", "down":
		if m.scheduleChoiceIdx < len(choices)-1 {
			m.scheduleChoiceIdx++
		}
	case "k", "up":
		if m.scheduleChoiceIdx > 0 {
			m.scheduleChoiceIdx--
		}
	case "e":
		m.scheduleChoosing = false
		m.startScheduleEdit(m.scheduleChoiceKey)
	case "enter":
		if len(choices) == 0 {
			return m, nil
		}
		value := choices[m.scheduleChoiceIdx].value
		if err := m.applyScheduleValue(m.scheduleChoiceKey, value); err != nil {
			m.pushToast("error", err.Error())
			return m, nil
		}
		if !m.scheduleModeTemp {
			m.dirty = true
		}
		m.scheduleChoosing = false
		m.scheduleChoiceKey = ""
	case "esc":
		m.scheduleChoosing = false
		m.scheduleChoiceKey = ""
	}
	return m, nil
}

func (m model) handleScheduleEditor(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "enter":
		value := m.scheduleInput.Value()
		if err := m.applyScheduleValue(m.scheduleEditKey, value); err != nil {
			m.pushToast("error", err.Error())
			return m, nil
		}
		if !m.scheduleModeTemp {
			m.dirty = true
		}
		m.scheduleEditing = false
		m.scheduleEditKey = ""
		m.scheduleInput.Blur()
	case "esc":
		m.scheduleEditing = false
		m.scheduleEditKey = ""
		m.scheduleInput.Blur()
	}
	return m, nil
}

func (m model) handleLogsKey(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "f":
		m.logFollow = !m.logFollow
		m.pushToast("info", "follow mode: "+strconv.FormatBool(m.logFollow))
	case "d":
		m.notifyAction("loading daily log")
		return m, loadLogs("daily", filepath.Join(m.cfg.LogDir, time.Now().Format("2006-01-02")+".log"))
	case "i":
		m.notifyAction("loading run index")
		return m, loadLogs("run index", m.cfg.RunLogPath)
	case "g":
		m.logView.GotoTop()
		m.notifyAction("logs moved to top")
	case "G":
		m.logView.GotoBottom()
		m.notifyAction("logs moved to bottom")
	}
	return m, nil
}

func (m model) handleHistoryKey(key tea.KeyMsg) (model, tea.Cmd) {
	switch key.String() {
	case "j", "down":
		if m.historySelected > 0 {
			m.historySelected--
		}
	case "k", "up":
		m.historySelected++
		if m.historySelected >= len(m.history) {
			m.historySelected = len(m.history) - 1
		}
	case "r":
		m.notifyAction("refreshing history")
		return m, loadHistory(m.cfg.RunLogPath)
	case "t":
		if len(m.history) > 0 {
			run := m.history[m.historySelected]
			if !m.cfg.RestoreTestEnabled {
				m.pushToast("error", "Restore validation is disabled in config")
				return m, nil
			}
			m.notifyAction("testing sandbox restore for run " + run.RunID + " (this may take a while)")
			return m, testRestoreRunCmd(m.cfg, run.RunID)
		}
	case "v":
		if len(m.history) > 0 {
			run := m.history[m.historySelected]
			m.notifyAction("validating logical backup for run " + run.RunID)
			return m, validateLogicalRunCmd(m.cfg, run.RunID)
		}
	case "c":
		if len(m.history) > 0 {
			run := m.history[m.historySelected]
			return m, copyToClipboard(run.RunID)
		}
	case "i":
		if len(m.history) > 0 {
			run := m.history[m.historySelected]
			m.notifyAction("inspecting backup run " + run.RunID)
			m.inspectRunID = run.RunID
			m.inspectRun = nil
			m.inspectErr = "Loading..."
			m.inspectDBIndex = 0
			m.inspectSearch = ""
			m.inspectSearchMode = false
			m.inspectSearchInput.SetValue("")
			m.setScreen(screenInspect)
			return m, inspectRunCmd(m.envPath, run.RunID)
		}
	}
	return m, nil
}

func (m model) handleSystemdKey(key tea.KeyMsg) (model, tea.Cmd) {
	actions := m.systemdActions()
	switch key.String() {
	case "j", "down":
		m.manualSelected++
		if m.manualSelected >= len(actions) {
			m.manualSelected = len(actions) - 1
		}
	case "k", "up":
		if m.manualSelected > 0 {
			m.manualSelected--
		}
	case "enter":
		action := actions[m.manualSelected]
		if action.refresh {
			m.notifyAction("refreshing systemd status")
			return m, loadSystemd(m.envPath)
		}
		m.notifyAction("confirming systemd action: " + action.label)
		m.confirm(action.confirm, func() tea.Cmd { return runSystemd(m.envPath, action.label, action.action) })
	}
	return m, nil
}

func (m model) handleCommandPalette(key tea.KeyMsg) (model, tea.Cmd) {
	commands := m.filteredCommands()
	switch key.String() {
	case "esc":
		m.commandOpen = false
		m.commandInput.Blur()
	case "tab":
		m.commandOpen = false
		m.commandInput.Blur()
		m.focus = focusSidebar
		m.notifyAction("focus moved to sidebar")
	case "shift+tab":
		m.commandOpen = false
		m.commandInput.Blur()
		m.focus = focusContent
		m.notifyAction("focus moved to content")
	case "j", "down":
		if m.commandIndex < len(commands)-1 {
			m.commandIndex++
		}
	case "k", "up":
		if m.commandIndex > 0 {
			m.commandIndex--
		}
	case "enter":
		if len(commands) == 0 {
			return m, nil
		}
		if len(commands) == 1 {
			m.commandIndex = 0
		}
		chosen := commands[clampIndex(m.commandIndex, len(commands))]
		m.commandOpen = false
		m.commandInput.Blur()
		m.notifyAction("running command: " + chosen.label)
		return m.runCommand(chosen.id)
	}
	return m, nil
}

func (m *model) setScreen(screen screenID) {
	previous := m.activeScreen
	m.activeScreen = screen
	m.sidebarSelected = int(screen)
	m.focus = focusContent
	if screen != screenSystemd {
		m.manualSelected = 0
	}
	if screen != previous && int(screen) >= 0 && int(screen) < len(screens) {
		m.notifyAction("opened " + screens[screen].name)
	}
}

func (m model) handleInspectKey(key tea.KeyMsg) (model, tea.Cmd) {
	// If search mode is active, route to text input first
	if m.inspectSearchMode {
		switch key.String() {
		case "esc", "enter":
			m.inspectSearchMode = false
			m.inspectSearchInput.Blur()
			m.inspectSearch = m.inspectSearchInput.Value()
			m.inspectDBIndex = 0
		default:
			var inputCmd tea.Cmd
			m.inspectSearchInput, inputCmd = m.inspectSearchInput.Update(key)
			m.inspectSearch = m.inspectSearchInput.Value()
			m.inspectDBIndex = 0
			return m, inputCmd
		}
		return m, nil
	}

	switch key.String() {
	case "q", "esc":
		m.setScreen(screenHistory)
	case "/":
		m.inspectSearchMode = true
		m.inspectSearchInput.SetValue("")
		m.inspectSearchInput.Focus()
	case "c":
		if m.inspectRun != nil {
			return m, copyToClipboard(m.inspectRun.RunID)
		}
	case "C":
		// Copy full file path of the selected database
		if m.inspectRun != nil {
			filtered := m.filteredInspectDBs()
			if len(filtered) > 0 && m.inspectDBIndex < len(filtered) {
				return m, copyToClipboard(filtered[m.inspectDBIndex].FilePath)
			}
		}
	case "j", "down":
		if m.inspectRun != nil {
			filtered := m.filteredInspectDBs()
			if m.inspectDBIndex < len(filtered)-1 {
				m.inspectDBIndex++
			}
		}
	case "k", "up":
		if m.inspectDBIndex > 0 {
			m.inspectDBIndex--
		}
	}
	return m, nil
}

// filteredInspectDBs returns the DB list filtered by the current search term.
func (m model) filteredInspectDBs() []backupapp.InspectDB {
	if m.inspectRun == nil {
		return nil
	}
	if m.inspectSearch == "" {
		return m.inspectRun.LogicalDBs
	}
	needle := strings.ToLower(m.inspectSearch)
	var out []backupapp.InspectDB
	for _, db := range m.inspectRun.LogicalDBs {
		if strings.Contains(strings.ToLower(db.Name), needle) {
			out = append(out, db)
		}
	}
	return out
}
