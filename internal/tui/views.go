package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"sdl/sdl_db_backup/internal/backupapp"
)

func (m *model) resizeViewports() {
	m.resizeViewportsFor(m.mainPanelHeight())
}

func (m *model) resizeViewportsFor(panelHeight int) {
	contentOuterWidth := clampNonNegative(m.width - m.sidebarWidth())
	contentInnerWidth := clampNonNegative(contentOuterWidth - 6)
	contentInnerHeight := clampNonNegative(panelHeight - 2)
	m.dashboardView.Width = contentInnerWidth
	m.dashboardView.Height = contentInnerHeight
	m.healthView.Width = contentInnerWidth
	m.healthView.Height = contentInnerHeight
	m.systemdView.Width = contentInnerWidth
	m.systemdView.Height = contentInnerHeight
	m.runLogView.Width = contentInnerWidth
	m.runLogView.Height = max(3, contentInnerHeight-8)
	m.logView.Width = contentInnerWidth
	m.logView.Height = max(3, contentInnerHeight-4)
	m.configView.Width = contentInnerWidth
	m.configView.Height = max(3, contentInnerHeight-7)
}

func (m model) sidebarWidth() int {
	return 26
}

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	status := m.viewStatusBar()
	toastView := ""
	if len(m.toasts) > 0 {
		toastView = m.viewToastPopup()
	}
	commandView := ""
	if m.commandOpen {
		commandView = m.viewCommandPalette()
	}
	helpView := ""
	if m.helpOpen {
		helpView = m.viewHelpModal()
	}
	confirmView := ""
	if m.confirmMessage != "" {
		confirmView = m.viewConfirm()
	}
	reservedHeight := lipgloss.Height(status) + lipgloss.Height(toastView) + lipgloss.Height(commandView) + lipgloss.Height(helpView) + lipgloss.Height(confirmView)
	mainHeight := clampNonNegative(m.height - reservedHeight)
	m.resizeViewportsFor(mainHeight)
	sidebar := m.viewSidebar(mainHeight)
	content := m.viewContent(mainHeight)
	main := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)
	view := lipgloss.JoinVertical(lipgloss.Left, main, status)
	if toastView != "" {
		view = lipgloss.JoinVertical(lipgloss.Left, view, toastView)
	}
	if commandView != "" {
		view = lipgloss.JoinVertical(lipgloss.Left, view, commandView)
	}
	if helpView != "" {
		view = lipgloss.JoinVertical(lipgloss.Left, view, helpView)
	}
	if confirmView != "" {
		view = lipgloss.JoinVertical(lipgloss.Left, view, confirmView)
	}
	return baseStyle.Render(view)
}

func (m model) mainPanelHeight() int {
	reservedHeight := 1
	if len(m.toasts) > 0 {
		reservedHeight += lipgloss.Height(m.viewToastPopup())
	}
	if m.commandOpen {
		reservedHeight += lipgloss.Height(m.viewCommandPalette())
	}
	if m.helpOpen {
		reservedHeight += lipgloss.Height(m.viewHelpModal())
	}
	if m.confirmMessage != "" {
		reservedHeight += lipgloss.Height(m.viewConfirm())
	}
	return clampNonNegative(m.height - reservedHeight)
}

func (m model) viewSidebar(height int) string {
	w := m.sidebarWidth()
	lines := []string{title.Render("SDL DB Backup"), ""}
	for i, s := range screens {
		label := fmt.Sprintf("[%s] %s", s.key, s.name)
		if i == m.sidebarSelected {
			if m.focus == focusSidebar && !m.modalActive() {
				lines = append(lines, selected.Width(clampNonNegative(w-4)).Render(label))
			} else {
				lines = append(lines, selectedDim.Render(label))
			}
		} else {
			lines = append(lines, "  "+label)
		}
	}
	lines = append(lines, "", muted.Render("Env: "+m.envPath))
	if m.dirty {
		lines = append(lines, warn.Render("⚠ Unsaved changes"))
	}
	pane := sidebarPanel
	if m.focus == focusSidebar && !m.modalActive() {
		pane = sidebarPanelActive
	}
	return pane.Width(w).Height(clampNonNegative(height - 2)).Render(strings.Join(lines, "\n"))
}

func (m model) viewContent(height int) string {
	width := clampNonNegative(m.width - m.sidebarWidth() - 6)
	var body string
	switch m.activeScreen {
	case screenDashboard:
		body = m.viewDashboard(width)
	case screenBackup:
		body = m.viewBackup(width)
	case screenSchedule:
		body = m.viewSchedule(width)
	case screenConfig:
		body = m.viewConfig(width)
	case screenLogs:
		body = m.viewLogs(width)
	case screenHistory:
		body = m.viewHistory(width)
	case screenHealth:
		body = m.viewHealth(width)
	case screenSystemd:
		body = m.viewSystemd(width)
	}
	pane := contentPanel
	if m.focus == focusContent && !m.modalActive() {
		pane = contentPanelActive
	}
	return pane.Width(clampNonNegative(m.width - m.sidebarWidth())).Height(clampNonNegative(height - 2)).Render(body)
}

func (m model) modalActive() bool {
	return m.commandOpen || m.helpOpen || m.confirmMessage != ""
}

func (m model) viewDashboard(width int) string {
	lines := []string{title.Render("Dashboard"), ""}
	if !m.healthLoaded && m.healthErr == "" {
		lines = append(lines, m.spinner.View()+" loading health...")
	}
	if m.healthErr != "" {
		lines = append(lines, bad.Render("Health error: ")+m.healthErr)
	}
	if m.healthLoaded {
		latest := "none"
		if m.health.LatestRun != nil {
			latest = fmt.Sprintf("%s at %s (%d/%d dbs)",
				statusStyled(m.health.LatestRun.Status),
				m.health.LatestRun.Timestamp.Format("2006-01-02 15:04"),
				m.health.LatestRun.DatabasesSucceeded,
				m.health.LatestRun.DatabasesTotal,
			)
		}
		lines = append(lines,
			card("Latest Run", latest, width),
			card("Logical", healthLine(m.health.Logical), width),
			card("Physical", healthLine(m.health.Physical), width),
			card("Runtime", fmt.Sprintf("user=%s source=%s", m.health.Runtime.CurrentUser, m.health.Runtime.ExecutionSource), width),
			card("API", fmt.Sprintf("enabled=%t auth=%t addr=%s", m.cfg.APIEnabled, m.cfg.APIAuthEnabled, m.cfg.APIListenAddr), width),
			card("Logs", "daily: "+m.health.DailyLogPath, width),
		)
		if m.health.Runtime.PotentialConflictReason != "" {
			lines = append(lines, warn.Render("Scheduler warning: ")+m.health.Runtime.PotentialConflictReason)
		}
	}
	lines = append(lines, "", title.Render("Quick Actions"))
	lines = append(lines,
		"b / 2  Manual backup workflow",
		"3      Schedule manager",
		"4      Search and edit config",
		"5      View logs",
		"7      Refresh health diagnostics",
		":      Command palette",
	)
	m.dashboardView.SetContent(strings.Join(lines, "\n"))
	return m.dashboardView.View()
}

func (m model) viewBackup(width int) string {
	lines := []string{title.Render("Manual Backup"), ""}
	steps := []string{"1 mode", "2 upload", "3 scope", "4 preview", "5 run"}
	for i, step := range steps {
		steps[i] = m.viewBackupStep(i, step)
	}
	lines = append(lines, strings.Join(steps, muted.Render(" ── ")), "")
	switch m.backupStep {
	case stepMode:
		lines = append(lines, optionList([]string{"logical + physical", "logical only", "physical only"}, m.manualSelected)...)
		lines = append(lines, "", m.viewBackupModeHelp())
	case stepUpload:
		lines = append(lines, optionList([]string{"normal upload behavior", "local only - disables uploads"}, m.manualSelected)...)
	case stepScope:
		lines = append(lines, m.viewScopeSelector(width)...)
	case stepPreview:
		force := "Force Run Now: " + strconv.FormatBool(m.manualForce)
		lines = append(lines, optionList([]string{force, "Start backup"}, m.manualSelected)...)
		lines = append(lines, "", title.Render("Preview"))
		lines = append(lines, m.runPreview.Lines...)
		if len(m.runPreview.Warnings) > 0 {
			lines = append(lines, warn.Render("Warnings"))
			for _, warning := range m.runPreview.Warnings {
				lines = append(lines, "- "+warning)
			}
		}
	case stepRunning:
		lines = append(lines, m.spinner.View()+" running backup", m.runProgress.summary(), m.progress.ViewAs(m.runProgress.percent()), "")
		lines = append(lines, m.runLogView.View())
	case stepDone:
		if m.runErr != "" {
			lines = append(lines, bad.Render("Backup failed: ")+m.runErr)
		} else if m.runResult != nil {
			lines = append(lines,
				statusStyled(m.runResult.Status),
				"Run ID: "+m.runResult.RunID,
				"Run Folder: "+m.runResult.RunFolder,
				"Log File: "+m.runResult.LogFile,
				fmt.Sprintf("Databases: %d/%d succeeded", m.runResult.DatabasesSucceeded, m.runResult.DatabasesTotal),
				"",
				"Press enter to start another manual workflow.",
			)
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) viewBackupStep(index int, label string) string {
	name := strings.TrimSpace(label[2:])
	if int(m.backupStep) > index {
		return good.Render("✔ " + strings.Title(name))
	}
	if int(m.backupStep) == index {
		return selected.Render("◉ " + strings.Title(name))
	}
	return muted.Render("○ " + strings.Title(name))
}

func (m model) viewBackupModeHelp() string {
	lines := []string{title.Render("What These Mean")}
	switch m.manualSelected {
	case 0:
		lines = append(lines,
			"Logical + physical runs both backup styles.",
			"Use this when you want the normal safest coverage: easy restore files plus a full server-level copy.",
		)
	case 1:
		lines = append(lines,
			"Logical backup creates readable .sql.gz files for each database.",
			"Layman meaning: it exports your tables and data like a database document you can import later, including into another database name.",
			"Best for: normal restores, checking data, moving one database, and local server backups.",
		)
	case 2:
		lines = append(lines,
			"Physical backup copies MySQL's raw data files using xtrabackup and streams them to S3.",
			"Layman meaning: it is closer to taking a full machine-level snapshot of MySQL, faster for very large full-server disaster recovery.",
			"Best for: full server recovery. It is less convenient for importing one database manually.",
		)
	}
	lines = append(lines,
		"",
		muted.Render("Short version: logical = easy SQL restore files; physical = full MySQL disaster-recovery copy."),
	)
	return strings.Join(lines, "\n")
}

func (m model) viewScopeSelector(width int) []string {
	lines := []string{
		title.Render("Backup Scope"),
		muted.Render("Default: all granted DBs/tables. Space toggle   Ctrl+A select all   Ctrl+X clear   v range   Ctrl+S save"),
		"",
	}
	if m.scopeLoading {
		lines = append(lines, m.spinner.View()+" discovering databases/tables...")
		return lines
	}
	if m.scopeErr != "" {
		lines = append(lines, bad.Render(m.scopeErr), "Press r to retry discovery.")
		return lines
	}
	if m.scopeLevel == "tables" {
		dbName := ""
		if len(m.scopeDatabases) > 0 {
			dbName = m.scopeDatabases[m.scopeDBIndex]
		}
		selectedCount := len(m.scopeSelectedTables[dbName])
		lines = append(lines, title.Render(fmt.Sprintf("Tables in %s (%d selected)", dbName, selectedCount)), muted.Render("Selecting tables means only those tables are dumped for this database. No table selection means all tables."))
		lines = append(lines, muted.Render("/ search   Space toggle   Ctrl+A select shown   v range   Ctrl+X clear   Enter continue   Esc back"))
		if m.scopeSearchActive {
			lines = append(lines, m.scopeTableSearch.View())
		}
		if len(m.scopeTables) == 0 {
			lines = append(lines, "No tables discovered.")
			return lines
		}
		lines = append(lines, m.scopeTableGridLines(width, dbName)...)
		lines = append(lines, "", title.Render("Current Scope"))
		lines = append(lines, m.scopePreviewLines()...)
		return lines
	}
	lines = append(lines, title.Render("Databases"), muted.Render("No database selected means all databases. Selecting a database lets you include all its tables or choose specific tables."))
	lines = append(lines, muted.Render("Ctrl+A selects all DBs. v starts/selects a range. Enter opens tables. Press n/right to preview."))
	if m.scopeDBMark >= 0 && m.scopeDBMark < len(m.scopeDatabases) {
		start, end := m.scopeDBRange()
		lines = append(lines, muted.Render(fmt.Sprintf("Range mark: %s (%d selected by range)", m.scopeDatabases[m.scopeDBMark], end-start+1)))
	}
	if len(m.scopeDatabases) == 0 {
		lines = append(lines, "No databases discovered. Press r to retry.")
		return lines
	}
	for i, dbName := range m.scopeDatabases {
		marker := "[ ]"
		if m.scopeSelectedDBs[dbName] {
			marker = "[x]"
		}
		tableText := "all tables"
		if len(m.scopeSelectedTables[dbName]) > 0 {
			tableText = fmt.Sprintf("%d selected tables", len(m.scopeSelectedTables[dbName]))
		}
		line := fmt.Sprintf("%s %-32s %s", marker, dbName, tableText)
		if i == m.scopeDBIndex {
			line = selected.Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", title.Render("Current Scope"))
	lines = append(lines, m.scopePreviewLines()...)
	return lines
}

func (m model) viewSchedule(width int) string {
	mode := "permanent .env change"
	if m.scheduleModeTemp {
		mode = "temporary override"
	}
	lines := []string{
		title.Render("Schedule Manager"),
		muted.Render("enter edit/toggle   a apply   c clear temporary override   s save/apply"),
		"",
		"Mode: " + mode,
	}
	if m.scheduleModeTemp {
		lines = append(lines, "Expires: "+scheduleDurations()[m.scheduleDuration].label)
	}
	if m.tempErr != "" {
		lines = append(lines, bad.Render("Temporary override error: ")+m.tempErr)
	}
	if m.tempOverrides != nil {
		lines = append(lines,
			warn.Render("Active temporary override until "+m.tempOverrides.ExpiresAt.Format("2006-01-02 15:04")),
			formatOverrideValues(m.tempOverrides.Values),
			"",
		)
	}
	for i, row := range scheduleRows() {
		value := m.scheduleRowValue(row.key)
		if m.scheduleEditing && m.scheduleEditKey == row.key {
			value = m.scheduleInput.View()
		}
		line := fmt.Sprintf("%-28s %s", row.label, value)
		if i == m.scheduleSelected {
			line = selected.Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", m.viewScheduleHint())
	if m.scheduleChoosing {
		lines = append(lines, "", m.viewScheduleChoices())
	}
	return strings.Join(lines, "\n")
}

func (m model) viewConfig(width int) string {
	search := m.searchInput.Value()
	if m.searchActive {
		search = m.searchInput.View()
	}
	header := []string{
		title.Render("Config Editor"),
		muted.Render("Search: " + emptyDefault(search, "none") + "   j/k scroll   pgup/pgdown   g/G top/bottom   s save   / search   v reveal"),
		"",
	}
	visible := m.visibleFields()
	if len(visible) == 0 {
		header = append(header, "No settings match your search.")
		return strings.Join(header, "\n")
	}
	lines, _ := m.configContentLines(visible)
	lines = append(lines, "", m.viewConfigHint(visible))
	m.configView.SetContent(strings.Join(lines, "\n"))
	return strings.Join(header, "\n") + m.configView.View()
}

func (m model) configContentLines(visible []int) ([]string, int) {
	lines := []string{}
	selectedLine := 0
	lastGroup := ""
	for row, idx := range visible {
		field := m.fields[idx]
		if field.Group != lastGroup {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, title.Render(field.Group))
			lastGroup = field.Group
		}
		value := field.Value
		if field.Secret && !m.revealSecrets {
			value = strings.Repeat("*", min(12, max(4, len(value))))
		}
		if m.editorActive && m.editorIndex == idx {
			value = m.editorInput.View()
		}
		line := fmt.Sprintf("%-34s %s", field.Key, value)
		if field.Error != "" {
			line += "  " + bad.Render(field.Error)
		}
		if row == m.configSelected {
			selectedLine = len(lines)
			line = selected.Render(line)
		}
		lines = append(lines, line)
	}
	return lines, selectedLine
}

func (m *model) syncConfigViewport(visible []int) {
	if len(visible) == 0 {
		m.configView.GotoTop()
		return
	}
	if m.configSelected >= len(visible) {
		m.configSelected = len(visible) - 1
	}
	if m.configSelected < 0 {
		m.configSelected = 0
	}
	lines, selectedLine := m.configContentLines(visible)
	lines = append(lines, "", m.viewConfigHint(visible))
	m.configView.SetContent(strings.Join(lines, "\n"))
	if selectedLine < m.configView.YOffset {
		m.configView.SetYOffset(selectedLine)
		return
	}
	bottom := m.configView.YOffset + max(1, m.configView.Height) - 1
	if selectedLine > bottom {
		m.configView.SetYOffset(selectedLine - max(1, m.configView.Height) + 1)
	}
}

func (m model) viewConfigHint(visible []int) string {
	if len(visible) == 0 || m.configSelected >= len(visible) {
		return ""
	}
	field := m.fields[visible[m.configSelected]]
	hints := map[string]string{
		"DB_USER":                           "Logical backup MySQL user. Use a read-only backup account.",
		"DB_PASS":                           "Password for the logical backup MySQL user.",
		"BACKUP_LOGICAL_SCHEDULE":           "When logical .sql.gz backups run. Prefer Schedule page presets for common timing.",
		"BACKUP_LOGICAL_DATABASES":          "Comma-separated database include list. Empty means all granted non-system databases.",
		"BACKUP_LOGICAL_TABLES":             "Per-database table include list like db1:users,orders;db2:events. Empty means all tables.",
		"BACKUP_PHYSICAL_SCHEDULE":          "When physical xtrabackup direct-to-S3 runs.",
		"BACKUP_LOGICAL_S3_UPLOAD_ENABLED":  "Uploads completed logical backup folders after dump completion.",
		"BACKUP_PHYSICAL_S3_UPLOAD_ENABLED": "Physical backup requires this because physical mode streams directly to S3.",
		"BACKUP_S3_UPLOAD_MODE":             "direct uploads logical backups from Go using env S3 secrets; php/http keep legacy upload paths.",
		"BACKUP_S3_LOGICAL_PREFIX":          "S3 prefix for logical backup objects, before the run ID.",
		"BACKUP_S3_PHYSICAL_PREFIX":         "S3 prefix for physical backup streams, before the run ID.",
		"BACKUP_RETENTION_DAYS":             "How many days of local logical backup folders are retained.",
		"BACKUP_DIR":                        "Local folder where logical backup run folders are created.",
		"BACKUP_LOG_DIR":                    "Folder for daily logs, per-run logs, run history, schedule state, and temporary overrides.",
		"BACKUP_SYSTEMD_SERVICE_NAME":       "User-level systemd service unit name for the backup runner.",
		"BACKUP_SYSTEMD_TIMER_NAME":         "User-level systemd timer unit name for scheduled runs.",
		"BACKUP_API_ENABLED":                "Turns on the standalone REST API server when you run the API command.",
		"BACKUP_API_LISTEN_ADDR":            "HTTP listen address for the built-in API server.",
		"BACKUP_API_BASE_PATH":              "Base route prefix for the REST API, usually /api/v1.",
		"BACKUP_API_AUTH_ENABLED":           "When true, API requests must send a bearer token.",
		"BACKUP_API_BEARER_TOKEN":           "Bearer token used when API auth is enabled. Rotate it from the command palette if needed.",
	}
	hint := hints[field.Key]
	if hint == "" {
		hint = "Editing this value changes the matching environment setting in the backup configuration."
	}
	return title.Render("Hint") + "\n" + field.Key + ": " + hint
}

func (m model) viewLogs(width int) string {
	header := fmt.Sprintf("Logs: %s   follow=%t   filter=%s", m.logSource, m.logFollow, emptyDefault(m.logFilter, "none"))
	if m.logSearchMode {
		header = "Logs: " + m.logSource + "   filter " + m.logSearch.View()
	}
	lines := []string{title.Render(header), muted.Render("d daily   i run index   / filter   f follow   g/G top/bottom"), ""}
	lines = append(lines, m.logView.View())
	return strings.Join(lines, "\n")
}

func (m model) viewHistory(width int) string {
	lines := []string{title.Render("Run History"), muted.Render("r refresh"), ""}
	if m.historyErr != "" {
		lines = append(lines, bad.Render(m.historyErr))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, fmt.Sprintf("%-19s %-9s %-10s %-10s %s", "time", "status", "dbs", "duration", "run id"))
	limit := min(len(m.history), max(5, m.mainPanelHeight()-10))
	start := max(0, len(m.history)-limit)
	row := 0
	for i := len(m.history) - 1; i >= start; i-- {
		run := m.history[i]
		line := fmt.Sprintf("%-19s %-9s %-10s %-10s %s",
			run.Timestamp.Format("2006-01-02 15:04"),
			run.Status,
			fmt.Sprintf("%d/%d", run.DatabasesSucceeded, run.DatabasesTotal),
			run.Duration,
			run.RunID,
		)
		if row%2 == 1 {
			line = rowAlt.Width(clampNonNegative(width - 4)).Render(line)
		}
		lines = append(lines, line)
		row++
	}
	if len(m.history) == 0 {
		lines = append(lines, "No runs recorded yet.")
	}
	return strings.Join(lines, "\n")
}

func (m model) viewHealth(width int) string {
	lines := []string{sectionTitle.Render("Health Diagnostics"), muted.Render("r refresh"), muted.Render("Shows effective runtime values, including temporary overrides."), ""}
	if m.healthErr != "" {
		lines = append(lines, bad.Render(m.healthErr))
		m.healthView.SetContent(strings.Join(lines, "\n"))
		return m.healthView.View()
	}
	if !m.healthLoaded {
		lines = append(lines, m.spinner.View()+" loading health...")
		m.healthView.SetContent(strings.Join(lines, "\n"))
		return m.healthView.View()
	}
	lines = append(lines,
		"Config: "+m.health.ConfigPath,
		"Daily Log: "+m.health.DailyLogPath,
		"Run Index: "+m.health.RunLogPath,
		fmt.Sprintf("Runtime User: %s (non-root=%t)", m.health.Runtime.CurrentUser, m.health.Runtime.NonRoot),
		"Execution Source: "+m.health.Runtime.ExecutionSource,
		"Service Unit: "+m.health.Runtime.ServiceUnitName,
		"Timer Unit: "+m.health.Runtime.TimerUnitName,
		fmt.Sprintf("API: enabled=%t auth=%t listen=%s", m.cfg.APIEnabled, m.cfg.APIAuthEnabled, m.cfg.APIListenAddr),
		"",
		"Logical:  "+healthLine(m.health.Logical),
		"Physical: "+healthLine(m.health.Physical),
		"",
	)
	if m.health.Runtime.PotentialConflictReason != "" {
		lines = append(lines, warn.Render("Scheduler warning: ")+m.health.Runtime.PotentialConflictReason, "")
	}
	if m.health.LatestRun == nil {
		lines = append(lines, sectionTitle.Render("Latest Run"), "none")
	} else {
		r := m.health.LatestRun
		lines = append(lines,
			sectionTitle.Render("Latest Run"),
			"Status: "+statusStyled(r.Status),
			"Time: "+r.Timestamp.Format(time.RFC3339),
			"Run ID: "+r.RunID,
			"Run Folder: "+r.RunFolder,
			"Log File: "+r.LogFile,
			fmt.Sprintf("Runtime: user=%s source=%s host=%s pid=%d", r.OSUser, r.ExecutionSource, r.Hostname, r.PID),
			"Duration: "+r.Duration,
			fmt.Sprintf("Databases: total=%d success=%d failed=%d", r.DatabasesTotal, r.DatabasesSucceeded, r.DatabasesFailed),
		)
		if r.FailureReason != "" {
			lines = append(lines, "Failure: "+r.FailureReason)
		}
	}
	m.healthView.SetContent(strings.Join(lines, "\n"))
	return m.healthView.View()
}

func (m model) viewSystemd(width int) string {
	lines := []string{sectionTitle.Render("Systemd"), muted.Render("enter action   r refresh   selected action explains command and effect"), ""}
	if m.systemdErr != "" {
		lines = append(lines, bad.Render(m.systemdErr), "")
	}
	if m.systemdLast != "" {
		lines = append(lines, sectionTitle.Render("Last Result"), m.systemdLast, "")
	}
	lines = append(lines,
		fmt.Sprintf("Service: load=%s active=%s sub=%s enabled=%s", m.systemd.Service.LoadState, m.systemd.Service.Active, m.systemd.Service.SubState, m.systemd.Service.Enabled),
		fmt.Sprintf("Timer:   load=%s active=%s sub=%s enabled=%s", m.systemd.Timer.LoadState, m.systemd.Timer.Active, m.systemd.Timer.SubState, m.systemd.Timer.Enabled),
		"Expected service unit: "+m.cfg.ServiceUnitName,
		"Expected timer unit:   "+m.cfg.TimerUnitName,
		"",
		sectionTitle.Render("Portable Guidance"),
		"Use a user-level systemd timer/service for scheduled runs.",
		"Do not schedule both the Go runner and the shell script.",
		"",
		sectionTitle.Render("Actions"),
	)
	for i, action := range m.systemdActions() {
		line := action.label
		if i == m.manualSelected {
			line = selected.Render(line)
		}
		lines = append(lines, line)
	}
	selectedActions := m.systemdActions()
	selectedAction := selectedActions[m.manualSelected]
	lines = append(lines,
		"",
		sectionTitle.Render("Selected Action"),
		selectedAction.description,
		"Command: "+selectedAction.command,
	)
	if render, err := backupapp.RenderUserSystemdUnits(m.envPath); err == nil {
		lines = append(lines,
			"",
			sectionTitle.Render("Rendered Unit Preview"),
			muted.Render("Install these under ~/.config/systemd/user on the target host."),
			fileBlock.Width(clampNonNegative(width-4)).Render(strings.TrimSpace(render.ServiceUnit)),
			"",
			fileBlock.Width(clampNonNegative(width-4)).Render(strings.TrimSpace(render.TimerUnit)),
		)
	}
	m.systemdView.SetContent(strings.Join(lines, "\n"))
	return m.systemdView.View()
}

func (m model) viewStatusBar() string {
	focusLabel := "◩ Content"
	if m.focus == focusSidebar {
		focusLabel = "◨ Sidebar"
	}
	left := lipgloss.JoinHorizontal(lipgloss.Center,
		statusPill.Render(screens[m.activeScreen].name),
		statusDimPill.Render(focusLabel),
		statusDimPill.Render("📁 "+m.envPath),
	)
	rightParts := m.contextKeyHints()
	right := muted.Render(strings.Join(rightParts, "  "))
	if m.dirty {
		right = warn.Render("⚠ Unsaved") + "  " + right
	}
	available := max(0, m.width-lipgloss.Width(left)-2)
	if available > 0 {
		right = lipgloss.NewStyle().MaxWidth(available).Render(right)
	} else {
		right = ""
	}
	spacer := clampNonNegative(m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2)
	return lipgloss.NewStyle().
		Foreground(colorText).
		Background(colorBorder).
		Width(max(20, m.width)).
		Render(left + strings.Repeat(" ", spacer) + right)
}

func (m model) contextKeyHints() []string {
	if m.focus == focusSidebar {
		return []string{
			keyHint.Render("[j/k]") + " Navigate",
			keyHint.Render("[Enter]") + " Select",
			keyHint.Render("[:]") + " Commands",
			keyHint.Render("[?]") + " Help",
		}
	}
	if m.activeScreen == screenBackup && m.backupStep == stepScope {
		if m.scopeLevel == "tables" {
			return []string{
				keyHint.Render("[Space]") + " Toggle",
				keyHint.Render("[v]") + " Range",
				keyHint.Render("[Ctrl+A]") + " Select All",
				keyHint.Render("[Ctrl+X]") + " Clear",
				keyHint.Render("[Esc]") + " Back",
			}
		}
		return []string{
			keyHint.Render("[Space]") + " Toggle",
			keyHint.Render("[Enter]") + " Tables",
			keyHint.Render("[v]") + " Range",
			keyHint.Render("[Ctrl+S]") + " Save Scope",
			keyHint.Render("[n]") + " Preview",
		}
	}
	switch m.activeScreen {
	case screenDashboard:
		return []string{keyHint.Render("[j/k]") + " Scroll", keyHint.Render("[PgUp/PgDn]") + " Page", keyHint.Render("[?]") + " Help", keyHint.Render("[Tab]") + " Focus"}
	case screenHealth:
		return []string{keyHint.Render("[j/k]") + " Scroll", keyHint.Render("[PgUp/PgDn]") + " Page", keyHint.Render("[r]") + " Refresh", keyHint.Render("[?]") + " Help"}
	case screenConfig:
		return []string{keyHint.Render("[/]") + " Search", keyHint.Render("[Enter]") + " Edit", keyHint.Render("[Ctrl+S]") + " Save", keyHint.Render("[v]") + " Reveal"}
	case screenLogs:
		return []string{keyHint.Render("[d]") + " Daily", keyHint.Render("[i]") + " Index", keyHint.Render("[/]") + " Filter", keyHint.Render("[f]") + " Follow"}
	case screenSchedule:
		return []string{keyHint.Render("[Enter]") + " Edit", keyHint.Render("[a]") + " Apply", keyHint.Render("[c]") + " Clear Temp", keyHint.Render("[?]") + " Help"}
	case screenSystemd:
		if m.focus == focusContent {
			return []string{keyHint.Render("[PgUp/PgDn]") + " Scroll", keyHint.Render("[j/k]") + " Select", keyHint.Render("[Enter]") + " Run", keyHint.Render("[r]") + " Refresh"}
		}
		return []string{keyHint.Render("[j/k]") + " Action", keyHint.Render("[Enter]") + " Run", keyHint.Render("[r]") + " Refresh", keyHint.Render("[?]") + " Help"}
	default:
		return []string{keyHint.Render("[Tab]") + " Focus", keyHint.Render("[:]") + " Commands", keyHint.Render("[?]") + " Help", keyHint.Render("[q]") + " Quit"}
	}
}

func (m model) viewToasts() string {
	if len(m.toasts) == 0 {
		return ""
	}
	lines := []string{title.Render("Notifications")}
	for _, toast := range m.toasts {
		lines = append(lines, m.viewToastLine(toast))
	}
	return strings.Join(lines, "\n")
}

func (m model) viewToastPopup() string {
	if len(m.toasts) == 0 {
		return ""
	}
	start := max(0, len(m.toasts)-3)
	lines := []string{sectionTitle.Render("Notifications")}
	for _, toast := range m.toasts[start:] {
		lines = append(lines, m.viewToastLine(toast))
	}
	return panel.Width(max(40, m.width-4)).Render(strings.Join(lines, "\n"))
}

func (m model) viewToastLine(toast toast) string {
	badge := muted.Render("[ ℹ INF ]")
	switch toast.level {
	case "ok":
		badge = good.Render("[ ✔ OK ]")
	case "error":
		badge = bad.Render("[ ✖ ERR ]")
	}
	return muted.Render(toast.at.Format("15:04:05")) + " │ " + badge + " " + toast.text
}

func (m model) viewCommandPalette() string {
	commands := m.filteredCommands()
	lines := []string{title.Render("Command Palette"), m.commandInput.View(), ""}
	if len(commands) == 0 {
		lines = append(lines, muted.Render("No commands match."))
	}
	for i, command := range commands {
		line := command.label
		if i == m.commandIndex {
			line = selected.Render(line)
		}
		lines = append(lines, line)
	}
	paletteWidth := min(max(48, m.width/2), max(48, m.width-8))
	content := floatingPanel.Width(paletteWidth).Render(strings.Join(lines, "\n"))
	left := max(0, (m.width-lipgloss.Width(content))/2)
	return lipgloss.NewStyle().MarginLeft(left).Render(content)
}

func (m model) viewConfirm() string {
	cancel := selected.Render("Cancel")
	confirm := "Confirm"
	if m.confirmSelected == 1 {
		cancel = "Cancel"
		confirm = lipgloss.NewStyle().Background(colorBad).Foreground(colorBG).Bold(true).Padding(0, 1).Render("Confirm")
	}
	return panel.Width(max(40, m.width-4)).Render(
		title.Render("Confirm") + "\n" + m.confirmMessage + "\n\n" + cancel + "  " + confirm + "\n" + muted.Render("Default is Cancel. Use left/right then Enter, or type y to confirm."),
	)
}

func (m model) viewHelpModal() string {
	rows := []string{
		fmt.Sprintf("%-18s %s", "Key", "Action"),
		fmt.Sprintf("%-18s %s", "1-8", "Jump to a page"),
		fmt.Sprintf("%-18s %s", "Tab / Shift+Tab", "Move focus forward/back"),
		fmt.Sprintf("%-18s %s", "j/k or arrows", "Navigate lists"),
		fmt.Sprintf("%-18s %s", "Enter", "Activate selected item"),
		fmt.Sprintf("%-18s %s", "Esc", "Cancel, close, or go back"),
		fmt.Sprintf("%-18s %s", ":", "Open command palette"),
		fmt.Sprintf("%-18s %s", "/", "Search or filter current page"),
		fmt.Sprintf("%-18s %s", "Ctrl+S", "Save current config/scope"),
		fmt.Sprintf("%-18s %s", "q", "Quit"),
		"",
		title.Render("Current Context"),
	}
	for _, hint := range m.contextHelpHints() {
		rows = append(rows, hint)
	}
	rows = append(rows, "", muted.Render("Press Esc, ?, q, or Enter to close."))
	modalWidth := min(max(62, m.width/2), max(62, m.width-8))
	content := floatingPanel.Width(modalWidth).Render(title.Render("Help") + "\n\n" + strings.Join(rows, "\n"))
	left := max(0, (m.width-lipgloss.Width(content))/2)
	return lipgloss.NewStyle().MarginLeft(left).Render(content)
}

func (m model) contextHelpHints() []string {
	if m.focus == focusSidebar {
		return []string{"j/k: Navigate sidebar", "Enter: Select highlighted page", ":: Open command palette"}
	}
	if m.activeScreen == screenBackup && m.backupStep == stepScope {
		return []string{"Space: Toggle selection", "v: Start/select visual range", "Ctrl+A: Select all visible", "Ctrl+X: Clear selection", "Ctrl+S: Save scope", "Esc: Back/cancel"}
	}
	switch m.activeScreen {
	case screenDashboard:
		return []string{"j/k: Scroll dashboard", "PgUp/PgDn: Page dashboard", "Tab: Move focus"}
	case screenHealth:
		return []string{"j/k: Scroll health diagnostics", "PgUp/PgDn: Page health diagnostics", "r: Refresh health"}
	case screenConfig:
		return []string{"/: Search settings", "Enter: Edit setting", "Ctrl+S: Save .env", "v: Reveal or mask secrets"}
	case screenLogs:
		return []string{"d: Daily log", "i: Run index", "/: Filter", "f: Follow"}
	case screenSchedule:
		return []string{"Enter: Edit selected row", "a: Apply changes", "c: Clear temporary override"}
	case screenSystemd:
		if m.focus == focusContent {
			return []string{"PgUp/PgDn: Scroll systemd text", "j/k: Select action", "Enter: Run selected action", "r: Refresh status"}
		}
		return []string{"j/k: Select action", "Enter: Confirm action", "r: Refresh status"}
	default:
		return []string{"Tab: Move focus", ":: Open command palette", "?: Help", "q: Quit"}
	}
}

func (m model) visibleFields() []int {
	query := strings.ToLower(strings.TrimSpace(m.searchInput.Value()))
	out := []int{}
	for i, field := range m.fields {
		haystack := strings.ToLower(field.Group + " " + field.Key + " " + field.Label + " " + field.Value)
		if query == "" || strings.Contains(haystack, query) {
			out = append(out, i)
		}
	}
	return out
}

func (m *model) startEditing(idx int) {
	field := m.fields[idx]
	m.editorIndex = idx
	m.editorInput.SetValue(field.Value)
	m.editorInput.Focus()
	m.editorActive = true
}

func (m *model) updateRunLogViewport() {
	m.runLogView.SetContent(strings.Join(m.runLogLines, "\n"))
	m.runLogView.GotoBottom()
}

func (m *model) updateLogViewport() {
	lines := m.logLines
	if m.logFilter != "" {
		filtered := []string{}
		needle := strings.ToLower(m.logFilter)
		for _, line := range lines {
			if strings.Contains(strings.ToLower(line), needle) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}
	m.logView.SetContent(strings.Join(lines, "\n"))
	if m.logFollow {
		m.logView.GotoBottom()
	}
}
