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
	contentInnerWidth := clampNonNegative(contentOuterWidth - 4)
	contentInnerHeight := clampNonNegative(panelHeight - 2)
	m.dashboardView.Width = contentInnerWidth
	m.dashboardView.Height = contentInnerHeight
	m.healthView.Width = contentInnerWidth
	m.healthView.Height = contentInnerHeight
	m.observabilityView.Width = contentInnerWidth
	m.observabilityView.Height = contentInnerHeight
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
	return 28
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
	reservedHeight := lipgloss.Height(status) + lipgloss.Height(commandView) + lipgloss.Height(helpView) + lipgloss.Height(confirmView)
	mainHeight := clampNonNegative(m.height - reservedHeight)

	viewportAvailableHeight := mainHeight
	if toastView != "" {
		viewportAvailableHeight = clampNonNegative(mainHeight - lipgloss.Height(toastView))
	}

	m.resizeViewportsFor(viewportAvailableHeight)
	sidebar := m.viewSidebar(mainHeight)
	content := m.viewContent(mainHeight, toastView)

	main := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)
	view := lipgloss.JoinVertical(lipgloss.Left, main, status)
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
		label := fmt.Sprintf(" %s  %s", s.key, s.name)
		if i == m.sidebarSelected {
			if m.focus == focusSidebar && !m.modalActive() {
				lines = append(lines, selected.Width(clampNonNegative(w-5)).Render(label))
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
	return pane.Width(clampNonNegative(w - 2)).Height(clampNonNegative(height - 2)).Render(strings.Join(lines, "\n"))
}

func (m model) viewContent(height int, toastView string) string {
	outerWidth := clampNonNegative(m.width - m.sidebarWidth())
	innerWidth := clampNonNegative(outerWidth - 4)
	body := ""
	switch m.activeScreen {
	case screenDashboard:
		body = m.viewDashboard(innerWidth)
	case screenBackup:
		body = m.viewBackup(innerWidth)
	case screenSchedule:
		body = m.viewSchedule(innerWidth)
	case screenConfig:
		body = m.viewConfig(innerWidth)
	case screenLogs:
		body = m.viewLogs(innerWidth)
	case screenHistory:
		body = m.viewHistory(innerWidth)
	case screenHealth:
		body = m.viewHealth(innerWidth)
	case screenObservability:
		body = m.viewObservability(innerWidth)
	case screenSystemd:
		body = m.viewSystemd(innerWidth)
	}

	targetInnerHeight := clampNonNegative(height - 2)

	if toastView != "" {
		targetBodyHeight := clampNonNegative(targetInnerHeight - lipgloss.Height(toastView))
		body = lipgloss.NewStyle().
			Width(innerWidth).MaxWidth(innerWidth).
			Height(targetBodyHeight).MaxHeight(targetBodyHeight).
			Render(body)
		body = lipgloss.JoinVertical(lipgloss.Right, body, toastView)
	} else {
		body = lipgloss.NewStyle().
			Width(innerWidth).MaxWidth(innerWidth).
			Height(targetInnerHeight).MaxHeight(targetInnerHeight).
			Render(body)
	}

	pane := contentPanel
	if m.focus == focusContent && !m.modalActive() {
		pane = contentPanelActive
	}
	return pane.Render(body)
}

func (m model) modalActive() bool {
	return m.commandOpen || m.helpOpen || m.confirmMessage != ""
}

func (m model) viewDashboard(width int) string {
	lines := []string{}

	// ── LIVE BACKUP ACTIVITY (shown when a backup is running) ────────────
	if m.running {
		elapsed := time.Since(m.runStartedAt)
		elapsedStr := fmt.Sprintf("%02d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)

		lines = append(lines,
			warn.Render("⚡ BACKUP IN PROGRESS"),
			fmt.Sprintf("  %s  Elapsed: %s    Stage: %s",
				m.spinner.View(),
				title.Render(elapsedStr),
				muted.Render(emptyDefault(m.runProgress.currentStage, "starting...")),
			),
		)
		if m.runProgress.total > 0 {
			lines = append(lines,
				fmt.Sprintf("  Databases: %s / %s    Current: %s",
					good.Render(fmt.Sprintf("%d done", m.runProgress.completed)),
					title.Render(fmt.Sprintf("%d total", m.runProgress.total)),
					muted.Render(emptyDefault(m.runProgress.currentDB, "-")),
				),
				"  "+m.progress.ViewAs(m.runProgress.percent()),
			)
		} else {
			lines = append(lines, "  "+m.progress.ViewAs(0))
		}

		lines = append(lines, "", muted.Render("  ── Live Log ─────────────────────────────────────────────"))
		tail := m.runLogLines
		if len(tail) > 12 {
			tail = tail[len(tail)-12:]
		}
		for _, l := range tail {
			lines = append(lines, "  "+muted.Render(l))
		}
		if len(m.runLogLines) == 0 {
			lines = append(lines, "  "+muted.Render("waiting for log output..."))
		}
		lines = append(lines,
			muted.Render("  ─────────────────────────────────────────────────────────"),
			"",
			muted.Render("  Stay here to watch live, or press 2 to go to the Backup screen for full control."),
			"",
		)
	}

	// ── LAST COMPLETED RUN ────────────────────────────────────────────────
	lines = append(lines, title.Render("Last Completed Run"))
	if !m.healthLoaded && m.healthErr == "" {
		lines = append(lines, "  "+m.spinner.View()+" loading...")
	} else if m.healthErr != "" {
		lines = append(lines, "  "+bad.Render("Health error: ")+m.healthErr)
	} else if m.health.LatestRun == nil {
		lines = append(lines, "  "+muted.Render("No run recorded yet — start a backup from the Backup screen (2)."))
	} else {
		r := m.health.LatestRun
		age := time.Since(r.Timestamp)
		var ageStr string
		switch {
		case age < time.Minute:
			ageStr = fmt.Sprintf("%ds ago", int(age.Seconds()))
		case age < time.Hour:
			ageStr = fmt.Sprintf("%dm ago", int(age.Minutes()))
		default:
			ageStr = fmt.Sprintf("%.1fh ago", age.Hours())
		}
		lines = append(lines,
			fmt.Sprintf("  %s  %s  %d/%d dbs  took %s",
				statusStyled(r.Status), muted.Render(ageStr),
				r.DatabasesSucceeded, r.DatabasesTotal, r.Duration,
			),
		)
		if r.RunID != "" {
			lines = append(lines, "  "+muted.Render("Run ID: ")+r.RunID)
		}
		if r.LogFile != "" {
			lines = append(lines, "  "+muted.Render("Log:    ")+r.LogFile)
		}
		if r.ValidationStatus != "" {
			vs := good
			if r.ValidationStatus != "success" {
				vs = bad
			}
			lines = append(lines, "  "+muted.Render("Validation: ")+vs.Render(r.ValidationStatus)+" via "+emptyDefault(r.ValidationMode, "validation"))
		} else {
			lines = append(lines, "  "+muted.Render("Validation: not run — press 6 History to validate"))
		}
		if r.FailureReason != "" {
			lines = append(lines, "  "+bad.Render("Failure: ")+r.FailureReason)
		}
		if r.LogicalUploadError != "" {
			lines = append(lines, "  "+bad.Render("Upload error: ")+r.LogicalUploadError)
		}
		if r.AdaptiveLogicalParallel > 0 {
			lines = append(lines,
				"  "+muted.Render(fmt.Sprintf("Parallelism: logical=%d  physical=%d  xbcloud=%d  load/cpu=%.2f",
					r.AdaptiveLogicalParallel, r.AdaptivePhysicalParallel, r.AdaptiveXbcloudParallel, r.AdaptiveLoadPerCPU)),
			)
		}
	}

	// ── THIS SESSION'S MANUAL RUN (only if it differs from the persisted latest) ──
	if m.runResult != nil {
		latestRunID := ""
		if m.health.LatestRun != nil {
			latestRunID = m.health.LatestRun.RunID
		}
		if m.runResult.RunID != latestRunID {
			lines = append(lines, "",
				title.Render("This Session's Manual Run"),
				fmt.Sprintf("  %s  %s  %d/%d dbs  took %s",
					statusStyled(m.runResult.Status),
					muted.Render(m.runResult.Timestamp.Format("15:04:05")),
					m.runResult.DatabasesSucceeded, m.runResult.DatabasesTotal,
					m.runResult.Duration,
				),
			)
		}
	}

	// ── SYSTEM HEALTH ─────────────────────────────────────────────────────
	lines = append(lines, "", title.Render("System Health"))
	if m.healthLoaded {
		icon := func(c backupapp.HealthCheck) string {
			switch c.Status {
			case "ok":
				return good.Render("✔") + " " + c.Message
			case "warn", "warning":
				return warn.Render("⚠") + " " + c.Message
			default:
				return bad.Render("✖") + " " + c.Message
			}
		}
		lines = append(lines,
			fmt.Sprintf("  %-12s %s", "Logical:", icon(m.health.Logical)),
			fmt.Sprintf("  %-12s %s", "Physical:", icon(m.health.Physical)),
			fmt.Sprintf("  %-12s %s", "Metrics:", icon(m.health.Metrics)),
		)
		if m.health.Runtime.PotentialConflictReason != "" {
			lines = append(lines, "  "+warn.Render("⚠ Scheduler conflict: ")+m.health.Runtime.PotentialConflictReason)
		}
		lines = append(lines,
			"  "+muted.Render(fmt.Sprintf("User: %s   Source: %s   Scheduler: %s",
				m.health.Runtime.CurrentUser,
				m.health.Runtime.ExecutionSource,
				emptyDefault(m.health.Runtime.SchedulerContext, "unknown"),
			)),
		)
		if m.cfg.APIEnabled {
			lines = append(lines, "  "+muted.Render(fmt.Sprintf("API: %s (auth=%t)", m.cfg.APIListenAddr, m.cfg.APIAuthEnabled)))
		}
	} else if m.healthErr == "" {
		lines = append(lines, "  "+muted.Render("loading..."))
	}

	// ── QUICK ACTIONS ──────────────────────────────────────────────────────
	lines = append(lines, "", title.Render("Quick Actions"))
	actions := []string{
		"2  Start manual backup workflow",
		"3  Open schedule manager",
		"4  Search and edit configuration",
		"5  View execution logs",
		"6  View run history & validate",
		"7  Refresh health diagnostics",
		"8  Open observability metrics",
	}
	lines = append(lines, optionList(actions, m.dashboardSelected)...)
	lines = append(lines, "", muted.Render("r refresh   ? help   q quit"))

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
		preflight := "Preflight Only: " + strconv.FormatBool(m.manualPreflight)
		actionLabel := "Start backup"
		if m.manualPreflight {
			actionLabel = "Start preflight"
		}
		lines = append(lines, optionList([]string{force, preflight, actionLabel}, m.manualSelected)...)
		lines = append(lines, "", title.Render("Preview"))
		lines = append(lines, m.runPreview.Lines...)
		if len(m.runPreview.Warnings) > 0 {
			lines = append(lines, warn.Render("Warnings"))
			for _, warning := range m.runPreview.Warnings {
				lines = append(lines, "- "+warning)
			}
		}
	case stepRunning:
		runningLabel := "running backup"
		if m.runConfig.PreflightOnly {
			runningLabel = "running preflight"
		}
		lines = append(lines, m.spinner.View()+" "+runningLabel, m.runProgress.summary(), m.progress.ViewAs(m.runProgress.percent()), "")
		lines = append(lines, m.runLogView.View())
	case stepDone:
		if m.runErr != "" {
			lines = append(lines, bad.Render("Backup failed: ")+m.runErr)
		} else if m.runResult != nil {
			runKind := statusStyled(m.runResult.Status)
			if m.runConfig.PreflightOnly {
				runKind = title.Render("Preflight Result") + "\n" + statusStyled(m.runResult.Status)
			}
			outcome := finalRunOutcome(*m.runResult)
			lines = append(lines,
				runKind,
				"Run ID: "+m.runResult.RunID,
				"Run Folder: "+m.runResult.RunFolder,
				"Log File: "+m.runResult.LogFile,
				fmt.Sprintf("Databases: %d/%d succeeded", m.runResult.DatabasesSucceeded, m.runResult.DatabasesTotal),
			)
			if outcome != "" {
				lines = append(lines, "Final Outcome: "+outcome)
			}
			if m.runResult.FailureReason != "" {
				lines = append(lines, "Failure Reason: "+m.runResult.FailureReason)
			}
			if m.runResult.LogicalUploadStatus != "" {
				lines = append(lines, "Logical Upload: "+m.runResult.LogicalUploadStatus)
			}
			if m.runResult.LogicalUploadError != "" {
				lines = append(lines, "Upload Error: "+m.runResult.LogicalUploadError)
			} else if m.runResult.LogicalUploadNote != "" {
				lines = append(lines, "Upload Note: "+m.runResult.LogicalUploadNote)
			}
			if m.runResult.CleanupError != "" {
				lines = append(lines, "Cleanup: "+m.runResult.CleanupError)
			}
			lines = append(lines, "", "Press enter to start another manual workflow.")
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
		lines = append(lines, title.Render(fmt.Sprintf("Tables and Views in %s (%d selected)", dbName, selectedCount)), muted.Render("Selecting objects means only those tables or views that pass validation are dumped for this database. No selection means all validated tables and views."))
		lines = append(lines, muted.Render("/ search   Space toggle   Ctrl+A select shown   v range   Ctrl+X clear   Enter continue   Esc back"))
		if m.scopeSearchActive {
			lines = append(lines, m.scopeTableSearch.View())
		}
		if len(m.scopeTables) == 0 {
			lines = append(lines, "No tables or views discovered.")
			return lines
		}
		lines = append(lines, m.scopeTableGridLines(width, dbName)...)
		lines = append(lines, "", title.Render("Current Scope"))
		lines = append(lines, m.scopePreviewLines()...)
		return lines
	}
	lines = append(lines, title.Render("Databases"), muted.Render("No database selected means all databases. Selecting a database lets you include all its tables or choose specific tables."))
	lines = append(lines, muted.Render("Ctrl+A selects all DBs. v starts/selects a range. Enter opens tables and views. Press n/right to preview."))
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
		tableText := "all tables and views"
		if len(m.scopeSelectedTables[dbName]) > 0 {
			tableText = fmt.Sprintf("%d selected objects", len(m.scopeSelectedTables[dbName]))
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
	dataRow := 0
	for row, idx := range visible {
		field := m.fields[idx]
		if field.Group != lastGroup {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, title.Render(field.Group))
			lastGroup = field.Group
			dataRow = 0
		}
		value := field.Value
		if field.Secret && !m.revealSecrets {
			value = strings.Repeat("*", min(12, max(4, len(value))))
		}
		if m.editorActive && m.editorIndex == idx {
			value = m.editorInput.View()
		}
		line := fmt.Sprintf(" %-34s │ %s", field.Key, value)
		if field.Error != "" {
			line += "  " + bad.Render(field.Error)
		}

		// Determine full width for row styling
		width := clampNonNegative(m.width - m.sidebarWidth() - 6)
		if lipgloss.Width(line) < width {
			line += strings.Repeat(" ", width-lipgloss.Width(line))
		}

		if row == m.configSelected {
			selectedLine = len(lines)
			line = rowSelected.Render(line)
		} else if dataRow%2 == 1 {
			line = rowAlt.Render(line)
		}
		lines = append(lines, line)
		dataRow++
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
		"BACKUP_LOGICAL_TABLES":             "Per-database table/view include list like db1:users,orders;db2:events. Empty means all tables and views.",
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
	lines := []string{title.Render("Run History"), muted.Render("j/k up/down   v validate logical   t test sandbox restore   r refresh"), ""}
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
		if i == m.historySelected {
			line = selected.Width(clampNonNegative(width - 4)).Render(line)
		} else if row%2 == 1 {
			line = rowAlt.Width(clampNonNegative(width - 4)).Render(line)
		}
		lines = append(lines, line)
		row++
	}
	if len(m.history) == 0 {
		lines = append(lines, "No runs recorded yet.")
		return strings.Join(lines, "\n")
	}
	selectedRun := m.history[m.historySelected]
	lines = append(lines, "", sectionTitle.Render("Selected Run"))
	lines = append(lines,
		"Run ID: "+selectedRun.RunID,
		"Status: "+statusStyled(selectedRun.Status),
		"Folder: "+emptyDefault(selectedRun.RunFolder, "none"),
		"Log: "+emptyDefault(selectedRun.LogFile, "none"),
	)
	if selectedRun.LogicalUploadStatus != "" {
		lines = append(lines, "Logical Upload: "+selectedRun.LogicalUploadStatus)
	}
	if selectedRun.LogicalUploadError != "" {
		lines = append(lines, "Upload Error: "+selectedRun.LogicalUploadError)
	} else if selectedRun.LogicalUploadNote != "" {
		lines = append(lines, "Upload Note: "+selectedRun.LogicalUploadNote)
	}
	if outcome := finalRunOutcome(selectedRun); outcome != "" {
		lines = append(lines, "Final Outcome: "+outcome)
	}
	if selectedRun.FailureReason != "" {
		lines = append(lines, "Failure Reason: "+selectedRun.FailureReason)
	}
	if selectedRun.CleanupError != "" {
		lines = append(lines, "Cleanup: "+selectedRun.CleanupError)
	}
	if state, ok := m.validations[selectedRun.RunID]; ok {
		lines = append(lines, "", sectionTitle.Render("Last Validation"))
		lines = append(lines,
			"Mode: "+emptyDefault(state.Mode, "validation"),
			"Checked: "+state.CheckedAt.Format(time.RFC3339),
		)
		switch {
		case state.Err != "":
			lines = append(lines, "Result: "+bad.Render("failed"), "Error: "+state.Err)
		case state.Result.Valid:
			lines = append(lines, "Result: "+good.Render("success"))
		default:
			lines = append(lines, "Result: "+bad.Render("failed"))
			if state.Result.Error != "" {
				lines = append(lines, "Error: "+state.Result.Error)
			}
		}
		if len(state.Result.Databases) > 0 {
			lines = append(lines, "Databases:")
			for _, db := range state.Result.Databases {
				dbLine := fmt.Sprintf("- %s: %s", db.Database, map[bool]string{true: "ok", false: "failed"}[db.Valid])
				if db.Error != "" {
					dbLine += " (" + db.Error + ")"
				}
				lines = append(lines, dbLine)
			}
		}
	} else if selectedRun.ValidationStatus != "" {
		lines = append(lines, "", sectionTitle.Render("Last Validation"))
		lines = append(lines,
			"Mode: "+emptyDefault(selectedRun.ValidationMode, "validation"),
			"Checked: "+selectedRun.ValidationCheckedAt.Format(time.RFC3339),
		)
		if selectedRun.ValidationStatus == "success" {
			lines = append(lines, "Result: "+good.Render("success"))
		} else {
			lines = append(lines, "Result: "+bad.Render("failed"))
			if selectedRun.ValidationError != "" {
				lines = append(lines, "Error: "+selectedRun.ValidationError)
			}
		}
		if len(selectedRun.ValidationDatabases) > 0 {
			lines = append(lines, "Databases:")
			for _, db := range selectedRun.ValidationDatabases {
				dbLine := fmt.Sprintf("- %s: %s", db.Database, map[bool]string{true: "ok", false: "failed"}[db.Valid])
				if db.Error != "" {
					dbLine += " (" + db.Error + ")"
				}
				lines = append(lines, dbLine)
			}
		}
	} else {
		lines = append(lines, "", sectionTitle.Render("Validation"), muted.Render("Use v to validate backup files or t to run a sandbox restore test for the selected run."))
	}
	return strings.Join(lines, "\n")
}

func (m model) viewHealth(width int) string {
	lines := []string{sectionTitle.Render("Health Diagnostics"), muted.Render("r refresh"), muted.Render("Shows effective runtime values, including temporary overrides."), muted.Render("Grafana guide: GRAFANA_BACKUP_MONITORING.md"), ""}
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
		"Scheduler Context: "+emptyDefault(m.health.Runtime.SchedulerContext, "unknown"),
		"Service Unit: "+m.health.Runtime.ServiceUnitName,
		"Timer Unit: "+m.health.Runtime.TimerUnitName,
		fmt.Sprintf("API: enabled=%t auth=%t listen=%s", m.cfg.APIEnabled, m.cfg.APIAuthEnabled, m.cfg.APIListenAddr),
		fmt.Sprintf("Restore Verification: restore_test=%t exact_rows=%t sample_checks=%t sample_rows=%d",
			m.health.RestoreVerification.RestoreTestEnabled,
			m.health.RestoreVerification.ExactRowCounts,
			m.health.RestoreVerification.SampleDataChecks,
			m.health.RestoreVerification.SampleDataRows,
		),
		"",
		"Logical:  "+healthLine(m.health.Logical),
		"Physical: "+healthLine(m.health.Physical),
		"Metrics:  "+healthLine(m.health.Metrics),
		"",
	)
	if len(m.health.Directories) > 0 {
		lines = append(lines, sectionTitle.Render("Directories"))
		for _, check := range m.health.Directories {
			lines = append(lines, strings.ReplaceAll(check.Name, "_", " ")+": "+healthLine(check))
		}
		lines = append(lines, "")
	}
	if m.health.Runtime.PotentialConflictReason != "" {
		lines = append(lines, warn.Render("Scheduler warning: ")+m.health.Runtime.PotentialConflictReason, "")
	}
	if len(m.health.Runtime.AuditChecklist) > 0 {
		lines = append(lines, sectionTitle.Render("Runtime Audit"))
		for _, item := range m.health.Runtime.AuditChecklist {
			lines = append(lines, "- "+item)
		}
		lines = append(lines, "")
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
		if r.LogicalUploadStatus != "" {
			lines = append(lines, "Logical Upload: "+r.LogicalUploadStatus)
		}
		if r.LogicalUploadError != "" {
			lines = append(lines, "Upload Error: "+r.LogicalUploadError)
		} else if r.LogicalUploadNote != "" {
			lines = append(lines, "Upload Note: "+r.LogicalUploadNote)
		}
		if r.ValidationStatus != "" {
			lines = append(lines,
				"Validation: "+r.ValidationStatus,
				"Validation Mode: "+emptyDefault(r.ValidationMode, "validation"),
				"Validation Checked: "+r.ValidationCheckedAt.Format(time.RFC3339),
			)
			if r.ValidationError != "" {
				lines = append(lines, "Validation Error: "+r.ValidationError)
			}
		}
		if r.AdaptiveLogicalParallel > 0 || r.AdaptivePhysicalParallel > 0 || r.AdaptiveXbcloudParallel > 0 {
			lines = append(lines,
				fmt.Sprintf("Adaptive Tuning: load/cpu=%.3f logical=%d physical=%d xbcloud=%d reason=%s", r.AdaptiveLoadPerCPU, r.AdaptiveLogicalParallel, r.AdaptivePhysicalParallel, r.AdaptiveXbcloudParallel, emptyDefault(r.AdaptiveTuningReason, "unknown")),
			)
		}
		if outcome := finalRunOutcome(backupapp.RunResult{
			Status:              r.Status,
			DatabasesTotal:      r.DatabasesTotal,
			DatabasesFailed:     r.DatabasesFailed,
			FailureReason:       r.FailureReason,
			CleanupError:        r.CleanupError,
			LogicalUploadStatus: r.LogicalUploadStatus,
			LogicalUploadError:  r.LogicalUploadError,
		}); outcome != "" {
			lines = append(lines, "Final Outcome: "+outcome)
		}
		if r.FailureReason != "" {
			lines = append(lines, "Failure Reason: "+r.FailureReason)
		}
		if r.CleanupError != "" {
			lines = append(lines, "Cleanup: "+r.CleanupError)
		}
	}
	m.healthView.SetContent(strings.Join(lines, "\n"))
	return m.healthView.View()
}

func (m model) viewObservability(width int) string {
	_ = width
	lines := []string{sectionTitle.Render("Observability"), muted.Render("r refresh"), muted.Render("Shows metrics collector state and parsed .prom values."), ""}
	if m.healthErr != "" {
		lines = append(lines, bad.Render(m.healthErr))
		m.observabilityView.SetContent(strings.Join(lines, "\n"))
		return m.observabilityView.View()
	}
	if !m.healthLoaded {
		lines = append(lines, m.spinner.View()+" loading observability...")
		m.observabilityView.SetContent(strings.Join(lines, "\n"))
		return m.observabilityView.View()
	}
	obs := m.health.Observability
	lines = append(lines,
		"Metrics Path: "+obs.MetricsPath,
		fmt.Sprintf("Path Writable: %t", obs.MetricsWritable),
		"Path Status: "+emptyDefault(obs.MetricsStatus, "unknown"),
		fmt.Sprintf("File Exists: %t", obs.MetricsFileExists),
	)
	if obs.MetricsFileExists {
		lines = append(lines,
			fmt.Sprintf("File Size: %d bytes", obs.MetricsFileSize),
			"File Mode: "+obs.MetricsFileMode,
			"File Modified: "+obs.MetricsFileTime.Format(time.RFC3339),
		)
	}
	lines = append(lines, "Last Metrics Write Result: "+emptyDefault(obs.LastWriteResult, "unknown"))
	if !obs.LastUpdateTime.IsZero() {
		lines = append(lines, "Last Metrics Update Time: "+obs.LastUpdateTime.Format(time.RFC3339))
	}
	// Show the active Prometheus label set so operators can verify the configuration.
	job := m.cfg.MetricsJob
	if job == "" {
		job = "sdl_db_backup"
	}
	service := m.cfg.MetricsService
	if service == "" {
		service = "mysql"
	}
	activeLabels := fmt.Sprintf(`job="%s",service="%s"`, job, service)
	if env := strings.TrimSpace(m.cfg.MetricsEnv); env != "" {
		activeLabels += fmt.Sprintf(`,env="%s"`, env)
	} else {
		activeLabels += `,env="pilot"`
	}
	if region := strings.TrimSpace(m.cfg.MetricsRegion); region != "" {
		activeLabels += fmt.Sprintf(`,region="%s"`, region)
	}
	lines = append(lines, "Prometheus Labels: {"+activeLabels+"}")
	lines = append(lines, "", sectionTitle.Render("Parsed Metrics"))
	metricOrder := []string{
		"backup_last_status",
		"backup_upload_success",
		"backup_metrics_write_success",
		"backup_cleanup_success",
		"backup_last_cleanup_timestamp",
		"backup_logical_last_status",
		"backup_logical_last_total_databases",
		"backup_logical_last_succeeded_databases",
		"backup_logical_last_failed_databases",
		"backup_physical_last_status",
		"backup_physical_last_duration_seconds",
		"backup_adaptive_load_per_cpu",
		"backup_adaptive_logical_parallel",
		"backup_adaptive_xtrabackup_parallel",
		"backup_adaptive_xbcloud_parallel",
		"backup_physical_retry_count",
		"backup_physical_rate_limit_retry_count",
		"backup_logical_validation_last_status",
		"backup_run_in_progress",
		"backup_last_run_timestamp",
		"backup_last_success_timestamp",
		"backup_last_duration_seconds",
		"backup_last_size_bytes",
		"backup_metrics_last_update_timestamp",
	}
	for _, key := range metricOrder {
		lines = append(lines, fmt.Sprintf("%s = %s", key, emptyDefault(obs.Snapshot[key], "0")))
	}
	m.observabilityView.SetContent(strings.Join(lines, "\n"))
	return m.observabilityView.View()
}

func (m model) viewSystemd(width int) string {
	lines := []string{sectionTitle.Render("Systemd"), muted.Render("enter action   r refresh   selected action explains command and effect"), ""}

	// ── Invocation Context ────────────────────────────────────────────────
	lines = append(lines, sectionTitle.Render("Invocation Context"))
	if m.healthLoaded {
		rt := m.health.Runtime
		userTag := good.Render("user-level")
		sysctlMode := "systemctl --user ..."
		if !rt.NonRoot {
			userTag = bad.Render("ROOT ⚠")
			sysctlMode = "systemctl (system-level) — scheduled runs as root are NOT supported"
		}
		lines = append(lines,
			fmt.Sprintf("Running as:      %s (%s)", rt.CurrentUser, userTag),
			fmt.Sprintf("systemctl mode:  %s", sysctlMode),
			fmt.Sprintf("Execution source: %s", rt.ExecutionSource),
		)
		if !rt.NonRoot {
			lines = append(lines,
				"",
				bad.Render("⚠  Scheduled backups must run as a non-root user via a user-level systemd"),
				bad.Render("   timer. Running as root causes ownership drift in backup/log/metrics dirs."),
			)
		}
	} else if m.healthErr != "" {
		lines = append(lines, bad.Render("could not determine invocation context: "+m.healthErr))
	} else {
		lines = append(lines, muted.Render("loading context..."))
	}
	lines = append(lines, "")

	// ── Unit Status ───────────────────────────────────────────────────────
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
		"Grafana guide: GRAFANA_BACKUP_MONITORING.md",
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
	case screenObservability:
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
	lines := []string{}
	for _, toast := range m.toasts[start:] {
		lines = append(lines, m.viewToastLine(toast))
	}
	return floatingPanel.Render(strings.Join(lines, "\n"))
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
			line = rowSelected.Render(line)
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
		fmt.Sprintf("%-18s %s", "1-9", "Jump to a page"),
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
		return []string{"j/k: Select action", "Enter: Run action", "Tab: Move focus"}
	case screenHealth:
		return []string{"j/k: Scroll health diagnostics", "PgUp/PgDn: Page health diagnostics", "r: Refresh health", "Grafana guide: GRAFANA_BACKUP_MONITORING.md"}
	case screenObservability:
		return []string{"j/k: Scroll observability", "PgUp/PgDn: Page observability", "r: Refresh metrics state", "Grafana guide: GRAFANA_BACKUP_MONITORING.md"}
	case screenConfig:
		return []string{"/: Search settings", "Enter: Edit setting", "Ctrl+S: Save .env", "v: Reveal or mask secrets"}
	case screenLogs:
		return []string{"d: Daily log", "i: Run index", "/: Filter", "f: Follow"}
	case screenSchedule:
		return []string{"Enter: Edit selected row", "a: Apply changes", "c: Clear temporary override"}
	case screenSystemd:
		if m.focus == focusContent {
			return []string{"PgUp/PgDn: Scroll systemd text", "j/k: Select action", "Enter: Run selected action", "r: Refresh status", "Grafana guide: GRAFANA_BACKUP_MONITORING.md"}
		}
		return []string{"j/k: Select action", "Enter: Confirm action", "r: Refresh status", "Grafana guide: GRAFANA_BACKUP_MONITORING.md"}
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
