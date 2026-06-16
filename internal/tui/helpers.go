package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"sdl/sdl_db_backup/internal/backupapp"
)

type systemdActionDef struct {
	label       string
	action      backupapp.SystemdAction
	confirm     string
	refresh     bool
	command     string
	description string
}

type runProgressState struct {
	total        int
	current      int
	completed    int
	currentDB    string
	currentStage string
}

type scheduleRow struct {
	key   string
	label string
}

type scheduleDuration struct {
	label string
	until func() time.Time
}

type scheduleChoice struct {
	label string
	value string
	hint  string
}

func scheduleRows() []scheduleRow {
	return []scheduleRow{
		{key: "mode", label: "Apply Mode"},
		{key: "duration", label: "Temporary Duration"},
		{key: "logical_schedule", label: "Logical Backup Times"},
		{key: "physical_schedule", label: "Physical/S3 Backup Time"},
		{key: "logical_upload", label: "Logical S3 Upload"},
		{key: "physical_upload", label: "Physical S3 Stream"},
		{key: "retention", label: "Retention Days"},
		{key: "apply", label: "Apply Changes"},
		{key: "clear", label: "Clear Temporary Override"},
	}
}

func scheduleDurations() []scheduleDuration {
	return []scheduleDuration{
		{label: "until end of today", until: func() time.Time {
			now := time.Now()
			return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
		}},
		{label: "for 24 hours", until: func() time.Time { return time.Now().Add(24 * time.Hour) }},
		{label: "for 7 days", until: func() time.Time { return time.Now().Add(7 * 24 * time.Hour) }},
		{label: "for 30 days", until: func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }},
	}
}

func (m model) scheduleRowValue(key string) string {
	switch key {
	case "mode":
		if m.scheduleModeTemp {
			return "temporary override"
		}
		return "permanent .env"
	case "duration":
		if !m.scheduleModeTemp {
			return "not used for permanent changes"
		}
		return scheduleDurations()[m.scheduleDuration].label
	case "logical_schedule":
		return m.draft.LogicalSchedule
	case "physical_schedule":
		return m.draft.PhysicalSchedule
	case "logical_upload":
		return strconv.FormatBool(m.draft.LogicalS3UploadEnabled)
	case "physical_upload":
		return strconv.FormatBool(m.draft.PhysicalS3UploadEnabled)
	case "retention":
		return strconv.Itoa(m.draft.RetentionDaily)
	case "apply":
		if m.scheduleModeTemp {
			return "save temporary override"
		}
		return "write to .env"
	case "clear":
		if m.tempOverrides == nil {
			return "none active"
		}
		return "remove active temporary override"
	default:
		return ""
	}
}

func (m *model) startScheduleEdit(key string) {
	m.scheduleEditKey = key
	m.scheduleInput.SetValue(m.scheduleRowValue(key))
	m.scheduleInput.Focus()
	m.scheduleEditing = true
}

func (m *model) startScheduleChoice(key string) {
	choices := m.scheduleChoices(key)
	if len(choices) == 0 {
		m.startScheduleEdit(key)
		return
	}
	current := m.scheduleRowValue(key)
	m.scheduleChoiceKey = key
	m.scheduleChoiceIdx = 0
	for i, choice := range choices {
		if choice.value == current {
			m.scheduleChoiceIdx = i
			break
		}
	}
	m.scheduleChoosing = true
}

func (m model) scheduleChoices(key string) []scheduleChoice {
	switch key {
	case "logical_schedule":
		return []scheduleChoice{
			{label: "Always", value: "always", hint: "Runs logical backup whenever the runner is invoked."},
			{label: "Disabled", value: "disabled", hint: "Skips logical backups entirely."},
			{label: "Daily once at 02:00", value: "daily@02:00", hint: "One logical backup per day."},
			{label: "Daily four times", value: "daily@00:00,06:00,12:00,18:00", hint: "Four logical backups per day at fixed clock slots."},
			{label: "Every 6 hours", value: "interval@6h", hint: "Runs six hours after the last successful logical backup."},
			{label: "Every 12 hours", value: "interval@12h", hint: "Runs twice a day based on last success time."},
			{label: "Every 24 hours", value: "interval@24h", hint: "Runs once per day based on last success time."},
			{label: "Custom value", value: m.draft.LogicalSchedule, hint: "Press e to type a custom schedule."},
		}
	case "physical_schedule":
		return []scheduleChoice{
			{label: "Always", value: "always", hint: "Runs physical/S3 backup whenever the runner is invoked."},
			{label: "Disabled", value: "disabled", hint: "Skips physical backups entirely."},
			{label: "Weekly Sunday 02:00", value: "weekly@sun,02:00", hint: "Typical low-frequency physical backup schedule."},
			{label: "Daily at 03:00", value: "daily@03:00", hint: "One physical/S3 backup per day."},
			{label: "Every 24 hours", value: "interval@24h", hint: "Runs after 24 hours have passed since last successful physical backup."},
			{label: "Every 7 days", value: "interval@168h", hint: "Weekly interval based on last success time."},
			{label: "Custom value", value: m.draft.PhysicalSchedule, hint: "Press e to type a custom schedule."},
		}
	case "retention":
		return []scheduleChoice{
			{label: "Keep 3 days", value: "3", hint: "Lower disk usage, shorter rollback window."},
			{label: "Keep 5 days", value: "5", hint: "Current default balance."},
			{label: "Keep 7 days", value: "7", hint: "One week of local logical backups."},
			{label: "Keep 14 days", value: "14", hint: "More rollback history, higher disk usage."},
			{label: "Keep 30 days", value: "30", hint: "Long local retention, requires enough disk."},
			{label: "Custom value", value: strconv.Itoa(m.draft.RetentionDaily), hint: "Press e to type a custom number."},
		}
	default:
		return nil
	}
}

func (m model) viewScheduleChoices() string {
	choices := m.scheduleChoices(m.scheduleChoiceKey)
	lines := []string{
		title.Render("Choose Option"),
		muted.Render("j/k move   enter choose   e custom type   esc cancel"),
	}
	for i, choice := range choices {
		line := fmt.Sprintf("%-24s %s", choice.label, choice.value)
		if i == m.scheduleChoiceIdx {
			line = selected.Render(line)
		}
		lines = append(lines, line)
	}
	if len(choices) > 0 {
		lines = append(lines, "", title.Render("Hint"), choices[m.scheduleChoiceIdx].hint)
	}
	return strings.Join(lines, "\n")
}

func (m model) viewScheduleHint() string {
	row := scheduleRows()[m.scheduleSelected]
	hints := map[string]string{
		"mode":              "Permanent writes to .env. Temporary creates an expiry-based override file and leaves .env unchanged.",
		"duration":          "Controls how long a temporary override stays active. Ignored for permanent changes.",
		"logical_schedule":  "Controls local logical .sql.gz backup timing. Use presets or press e in the option picker for custom syntax.",
		"physical_schedule": "Controls physical xtrabackup direct-to-S3 timing.",
		"logical_upload":    "Controls whether completed logical backup folders are uploaded to S3.",
		"physical_upload":   "Physical backups only work when direct S3 streaming is enabled.",
		"retention":         "Controls how many days of local logical backup folders are kept.",
		"apply":             "Applies the current schedule values using the selected apply mode.",
		"clear":             "Removes active temporary overrides and falls back to .env.",
	}
	return title.Render("Hint") + "\n" + hints[row.key]
}

func (m *model) applyScheduleValue(key, value string) error {
	switch key {
	case "logical_schedule":
		if err := validateSchedule(value); err != nil {
			return err
		}
		m.draft.LogicalSchedule = value
	case "physical_schedule":
		if err := validateSchedule(value); err != nil {
			return err
		}
		m.draft.PhysicalSchedule = value
	case "retention":
		days, err := strconv.Atoi(value)
		if err != nil || days < 0 {
			return fmt.Errorf("retention days must be a non-negative integer")
		}
		m.draft.RetentionDaily = days
	default:
		return fmt.Errorf("field cannot be edited directly")
	}
	m.rebuildPreview()
	return nil
}

func (m model) applyScheduleChanges() (model, tea.Cmd) {
	if m.scheduleModeTemp {
		expiresAt := scheduleDurations()[m.scheduleDuration].until()
		overrides := backupapp.TemporaryOverrides{
			CreatedAt: time.Now(),
			ExpiresAt: expiresAt,
			Note:      "created from TUI schedule manager",
			Values: map[string]string{
				"BACKUP_LOGICAL_SCHEDULE":           m.draft.LogicalSchedule,
				"BACKUP_PHYSICAL_SCHEDULE":          m.draft.PhysicalSchedule,
				"BACKUP_LOGICAL_S3_UPLOAD_ENABLED":  strconv.FormatBool(m.draft.LogicalS3UploadEnabled),
				"BACKUP_PHYSICAL_S3_UPLOAD_ENABLED": strconv.FormatBool(m.draft.PhysicalS3UploadEnabled),
				"BACKUP_RETENTION_DAILY":            strconv.Itoa(m.draft.RetentionDaily),
			},
		}
		m.draft = m.cfg
		m.fields = buildConfigFields(m.cfg)
		m.dirty = false
		return m, saveTemporaryOverrides(m.envPath, overrides)
	}
	return m, saveSchedulePermanently(m.envPath, m.draft)
}

func formatOverrideValues(values map[string]string) string {
	keys := []string{
		"BACKUP_LOGICAL_SCHEDULE",
		"BACKUP_PHYSICAL_SCHEDULE",
		"BACKUP_LOGICAL_S3_UPLOAD_ENABLED",
		"BACKUP_PHYSICAL_S3_UPLOAD_ENABLED",
		"BACKUP_RETENTION_DAYS",
	}
	lines := []string{}
	for _, key := range keys {
		if value, ok := values[key]; ok {
			lines = append(lines, "  "+key+"="+value)
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) systemdActions() []systemdActionDef {
	return []systemdActionDef{
		{label: "Refresh status", refresh: true, command: "systemctl --user show ...", description: "Reloads displayed service and timer state without changing anything."},
		{label: "Daemon reload", action: backupapp.SystemdDaemonReload, confirm: "Run systemctl --user daemon-reload?", command: "systemctl --user daemon-reload", description: "Reloads user systemd unit files after changing or copying service/timer files."},
		{label: "Enable timer", action: backupapp.SystemdEnableTimer, confirm: "Enable and start " + m.cfg.TimerUnitName + "?", command: "systemctl --user enable --now " + m.cfg.TimerUnitName, description: "Starts the timer now and enables it for future user sessions."},
		{label: "Disable timer", action: backupapp.SystemdDisableTimer, confirm: "Disable and stop " + m.cfg.TimerUnitName + "?", command: "systemctl --user disable --now " + m.cfg.TimerUnitName, description: "Stops scheduled backups and disables automatic timer activation."},
		{label: "Start timer", action: backupapp.SystemdStartTimer, confirm: "Start the backup timer?", command: "systemctl --user start " + m.cfg.TimerUnitName, description: "Starts the timer for this session without changing enabled state."},
		{label: "Stop timer", action: backupapp.SystemdStopTimer, confirm: "Stop the backup timer?", command: "systemctl --user stop " + m.cfg.TimerUnitName, description: "Stops the timer for this session without changing enabled state."},
		{label: "Restart service", action: backupapp.SystemdRestartSvc, confirm: "Restart the backup service?", command: "systemctl --user restart " + m.cfg.ServiceUnitName, description: "Stops and starts the backup runner service immediately."},
		{label: "Start service", action: backupapp.SystemdStartSvc, confirm: "Start the backup service?", command: "systemctl --user start " + m.cfg.ServiceUnitName, description: "Runs the backup service once immediately, outside timer timing."},
		{label: "Stop service", action: backupapp.SystemdStopSvc, confirm: "Stop the backup service?", command: "systemctl --user stop " + m.cfg.ServiceUnitName, description: "Stops a currently running backup service."},
	}
}

func (p *runProgressState) applyLogLine(line string) {
	if total, ok := parseFoundDatabases(line); ok {
		p.total = total
		p.currentStage = "discovered databases"
		return
	}
	if current, total, db, ok := parseProcessingDatabase(line); ok {
		p.current = current
		if total > 0 {
			p.total = total
		}
		p.currentDB = db
		p.currentStage = "dumping " + db
		return
	}
	if db, ok := parseCompletedDump(line); ok {
		if p.currentDB == "" || p.currentDB == db {
			p.completed++
			if p.current < p.completed {
				p.current = p.completed
			}
		}
		p.currentStage = "completed " + db
		return
	}
	if strings.Contains(line, "s3 upload:") {
		p.currentStage = "uploading to S3"
		return
	}
	if strings.Contains(line, "cleanup:") {
		p.currentStage = "cleanup"
	}
}

func (p runProgressState) percent() float64 {
	if p.total <= 0 {
		return 0
	}
	percent := float64(p.completed) / float64(p.total)
	if p.current > p.completed {
		inProgressFloor := (float64(p.current-1) / float64(p.total)) + (0.35 / float64(p.total))
		if inProgressFloor > percent {
			percent = inProgressFloor
		}
	}
	if percent > 0.99 && p.completed < p.total {
		return 0.99
	}
	return percent
}

func (p runProgressState) summary() string {
	if p.total <= 0 {
		if p.currentStage != "" {
			return muted.Render(p.currentStage)
		}
		return muted.Render("waiting for database discovery")
	}
	stage := p.currentStage
	if stage == "" {
		stage = "running"
	}
	return fmt.Sprintf("%s  databases=%d/%d  current=%s", muted.Render(stage), p.completed, p.total, emptyDefault(p.currentDB, "-"))
}

func parseFoundDatabases(line string) (int, bool) {
	idx := strings.Index(line, "found ")
	if idx < 0 || !strings.Contains(line[idx:], " databases") {
		return 0, false
	}
	rest := strings.TrimPrefix(line[idx:], "found ")
	fields := strings.Fields(rest)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "databases") {
		return 0, false
	}
	total, err := strconv.Atoi(fields[0])
	return total, err == nil
}

func parseProcessingDatabase(line string) (int, int, string, bool) {
	idx := strings.Index(line, "] processing ")
	if idx < 0 {
		return 0, 0, "", false
	}
	prefixStart := strings.LastIndex(line[:idx], "[")
	if prefixStart < 0 {
		return 0, 0, "", false
	}
	parts := strings.Split(strings.Trim(line[prefixStart+1:idx], "[]"), "/")
	if len(parts) != 2 {
		return 0, 0, "", false
	}
	current, errCurrent := strconv.Atoi(parts[0])
	total, errTotal := strconv.Atoi(parts[1])
	db := strings.TrimSpace(line[idx+len("] processing "):])
	return current, total, db, errCurrent == nil && errTotal == nil && db != ""
}

func parseCompletedDump(line string) (string, bool) {
	idx := strings.Index(line, "completed dump for database=")
	if idx < 0 {
		return "", false
	}
	rest := line[idx+len("completed dump for database="):]
	db, _, _ := strings.Cut(rest, " ")
	db = strings.TrimSpace(db)
	return db, db != ""
}

func buildConfigFields(cfg backupapp.Config) []configField {
	fields := []configField{}
	addString := func(group, key, label, value string, secret bool, apply func(*backupapp.Config, string)) {
		fields = append(fields, configField{
			Group: group, Key: key, Label: label, Value: value, Secret: secret,
			Apply: func(cfg *backupapp.Config, value string) error {
				apply(cfg, value)
				return nil
			},
		})
	}
	addBool := func(group, key, label string, value bool, apply func(*backupapp.Config, bool)) {
		fields = append(fields, configField{
			Group: group, Key: key, Label: label, Value: strconv.FormatBool(value),
			Apply: func(cfg *backupapp.Config, value string) error {
				parsed, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("expected true or false")
				}
				apply(cfg, parsed)
				return nil
			},
		})
	}
	addInt := func(group, key, label string, value int, apply func(*backupapp.Config, int)) {
		fields = append(fields, configField{
			Group: group, Key: key, Label: label, Value: strconv.Itoa(value),
			Apply: func(cfg *backupapp.Config, value string) error {
				parsed, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("expected integer")
				}
				apply(cfg, parsed)
				return nil
			},
		})
	}
	addDuration := func(group, key, label string, value time.Duration, apply func(*backupapp.Config, time.Duration)) {
		fields = append(fields, configField{
			Group: group, Key: key, Label: label, Value: value.String(),
			Apply: func(cfg *backupapp.Config, value string) error {
				parsed, err := time.ParseDuration(value)
				if err != nil {
					return fmt.Errorf("expected duration like 30m or 2h")
				}
				apply(cfg, parsed)
				return nil
			},
		})
	}
	addSchedule := func(group, key, label, value string, apply func(*backupapp.Config, string)) {
		fields = append(fields, configField{
			Group: group, Key: key, Label: label, Value: value,
			Apply: func(cfg *backupapp.Config, value string) error {
				if err := validateSchedule(value); err != nil {
					return err
				}
				apply(cfg, value)
				return nil
			},
		})
	}

	addString("Database", "DB_USER", "DB User", cfg.DBUser, false, func(c *backupapp.Config, v string) { c.DBUser = v })
	addString("Database", "DB_PASS", "DB Password", cfg.DBPass, true, func(c *backupapp.Config, v string) { c.DBPass = v })
	addString("Database", "DB_HOST", "DB Host", cfg.DBHost, false, func(c *backupapp.Config, v string) { c.DBHost = v })
	addString("Database", "DB_PORT", "DB Port", cfg.DBPort, false, func(c *backupapp.Config, v string) { c.DBPort = v })

	addString("Paths", "BACKUP_DIR", "Backup Dir", cfg.BackupDir, false, func(c *backupapp.Config, v string) { c.BackupDir = v })
	addString("Paths", "BACKUP_LOG_DIR", "Log Dir", cfg.LogDir, false, func(c *backupapp.Config, v string) { c.LogDir = v })
	addString("Paths", "BACKUP_LOCK_FILE", "Lock File", cfg.LockFile, false, func(c *backupapp.Config, v string) { c.LockFile = v })
	addString("Paths", "BACKUP_SYSTEMD_SERVICE_NAME", "Service Unit", cfg.ServiceUnitName, false, func(c *backupapp.Config, v string) { c.ServiceUnitName = v })
	addString("Paths", "BACKUP_SYSTEMD_TIMER_NAME", "Timer Unit", cfg.TimerUnitName, false, func(c *backupapp.Config, v string) { c.TimerUnitName = v })
	addString("Paths", "MYSQL_BIN", "MySQL Binary", cfg.MySQLBin, false, func(c *backupapp.Config, v string) { c.MySQLBin = v })
	addString("Paths", "MYSQLDUMP_BIN", "mysqldump Binary", cfg.MySQLDumpBin, false, func(c *backupapp.Config, v string) { c.MySQLDumpBin = v })

	addBool("Logical", "BACKUP_LOGICAL_ENABLED", "Logical Enabled", cfg.LogicalEnabled, func(c *backupapp.Config, v bool) { c.LogicalEnabled = v })
	addSchedule("Logical", "BACKUP_LOGICAL_SCHEDULE", "Logical Schedule", cfg.LogicalSchedule, func(c *backupapp.Config, v string) { c.LogicalSchedule = v })
	addString("Logical", "BACKUP_LOGICAL_DATABASES", "Logical Databases", strings.Join(cfg.LogicalDatabases, ","), false, func(c *backupapp.Config, v string) {
		c.LogicalDatabases = parseTUIList(v)
	})
	addString("Logical", "BACKUP_LOGICAL_TABLES", "Logical Tables", formatTUITableScope(cfg.LogicalTables), false, func(c *backupapp.Config, v string) {
		c.LogicalTables = parseTUITableScope(v)
	})
	addDuration("Logical", "BACKUP_LOGICAL_TIMEOUT_PER_DB", "Logical Timeout", cfg.LogicalTimeoutPerDB, func(c *backupapp.Config, v time.Duration) { c.LogicalTimeoutPerDB = v })
	addBool("Logical", "BACKUP_LOGICAL_S3_UPLOAD_ENABLED", "Logical Upload", cfg.LogicalS3UploadEnabled, func(c *backupapp.Config, v bool) { c.LogicalS3UploadEnabled = v })

	addBool("Physical", "BACKUP_PHYSICAL_ENABLED", "Physical Enabled", cfg.PhysicalEnabled, func(c *backupapp.Config, v bool) { c.PhysicalEnabled = v })
	addSchedule("Physical", "BACKUP_PHYSICAL_SCHEDULE", "Physical Schedule", cfg.PhysicalSchedule, func(c *backupapp.Config, v string) { c.PhysicalSchedule = v })
	addDuration("Physical", "BACKUP_PHYSICAL_TIMEOUT", "Physical Timeout", cfg.PhysicalTimeout, func(c *backupapp.Config, v time.Duration) { c.PhysicalTimeout = v })
	addBool("Physical", "BACKUP_PHYSICAL_S3_UPLOAD_ENABLED", "Physical Upload", cfg.PhysicalS3UploadEnabled, func(c *backupapp.Config, v bool) { c.PhysicalS3UploadEnabled = v })
	addString("Physical", "BACKUP_XTRABACKUP_BIN", "xtrabackup Bin", cfg.XtrabackupBin, false, func(c *backupapp.Config, v string) { c.XtrabackupBin = v })
	addString("Physical", "BACKUP_XBCLOUD_BIN", "xbcloud Bin", cfg.XbcloudBin, false, func(c *backupapp.Config, v string) { c.XbcloudBin = v })
	addInt("Physical", "BACKUP_XTRABACKUP_PARALLEL", "xtrabackup Parallel", cfg.XtrabackupParallel, func(c *backupapp.Config, v int) { c.XtrabackupParallel = v })
	addString("Physical", "BACKUP_XTRABACKUP_USER", "xtrabackup User", cfg.XtrabackupUser, false, func(c *backupapp.Config, v string) { c.XtrabackupUser = v })
	addString("Physical", "BACKUP_XTRABACKUP_PASS", "xtrabackup Pass", cfg.XtrabackupPass, true, func(c *backupapp.Config, v string) { c.XtrabackupPass = v })
	addString("Physical", "BACKUP_XTRABACKUP_SOCKET", "xtrabackup Socket", cfg.XtrabackupSocket, false, func(c *backupapp.Config, v string) { c.XtrabackupSocket = v })
	addString("Physical", "BACKUP_XTRABACKUP_RUN_AS_USER", "Run As User", cfg.XtrabackupRunAsUser, false, func(c *backupapp.Config, v string) { c.XtrabackupRunAsUser = v })
	addString("Physical", "BACKUP_XTRABACKUP_WORK_DIR", "Work Dir", cfg.XtrabackupWorkDir, false, func(c *backupapp.Config, v string) { c.XtrabackupWorkDir = v })

	addString("S3", "BACKUP_S3_BUCKET", "S3 Bucket", cfg.S3Bucket, false, func(c *backupapp.Config, v string) { c.S3Bucket = v })
	addString("S3", "BACKUP_S3_REGION", "S3 Region", cfg.S3Region, false, func(c *backupapp.Config, v string) { c.S3Region = v })
	addString("S3", "BACKUP_S3_LOGICAL_PREFIX", "Logical Prefix", cfg.S3LogicalPrefix, false, func(c *backupapp.Config, v string) { c.S3LogicalPrefix = v })
	addString("S3", "BACKUP_S3_PHYSICAL_PREFIX", "Physical Prefix", cfg.S3PhysicalPrefix, false, func(c *backupapp.Config, v string) { c.S3PhysicalPrefix = v })
	addString("S3", "BACKUP_S3_KEY_ID", "S3 Key ID", cfg.S3KeyID, true, func(c *backupapp.Config, v string) { c.S3KeyID = v })
	addString("S3", "BACKUP_S3_KEY_SECRET", "S3 Key Secret", cfg.S3KeySecret, true, func(c *backupapp.Config, v string) { c.S3KeySecret = v })

	addString("Upload", "BACKUP_S3_UPLOAD_MODE", "Upload Mode", cfg.S3UploadMode, false, func(c *backupapp.Config, v string) { c.S3UploadMode = strings.ToLower(strings.TrimSpace(v)) })
	addString("Upload", "BACKUP_S3_UPLOAD_URL", "Upload URL", cfg.S3UploadURL, false, func(c *backupapp.Config, v string) { c.S3UploadURL = v })
	addDuration("Upload", "BACKUP_S3_UPLOAD_TIMEOUT", "Upload Timeout", cfg.S3UploadTimeout, func(c *backupapp.Config, v time.Duration) { c.S3UploadTimeout = v })
	addString("Upload", "BACKUP_S3_PHP_BIN", "PHP Bin", cfg.S3PHPBin, false, func(c *backupapp.Config, v string) { c.S3PHPBin = v })
	addString("Upload", "BACKUP_S3_UPLOAD_SCRIPT", "Upload Script", cfg.S3UploadScript, false, func(c *backupapp.Config, v string) { c.S3UploadScript = v })

	addBool("API", "BACKUP_API_ENABLED", "API Enabled", cfg.APIEnabled, func(c *backupapp.Config, v bool) { c.APIEnabled = v })
	addString("API", "BACKUP_API_LISTEN_ADDR", "Listen Addr", cfg.APIListenAddr, false, func(c *backupapp.Config, v string) { c.APIListenAddr = v })
	addString("API", "BACKUP_API_BASE_PATH", "Base Path", cfg.APIBasePath, false, func(c *backupapp.Config, v string) { c.APIBasePath = v })
	addBool("API", "BACKUP_API_AUTH_ENABLED", "Auth Enabled", cfg.APIAuthEnabled, func(c *backupapp.Config, v bool) { c.APIAuthEnabled = v })
	addString("API", "BACKUP_API_BEARER_TOKEN", "Bearer Token", cfg.APIBearerToken, true, func(c *backupapp.Config, v string) { c.APIBearerToken = v })

	addInt("Tuning", "BACKUP_RETRY_COUNT", "Retry Count", cfg.RetryCount, func(c *backupapp.Config, v int) { c.RetryCount = v })
	addDuration("Tuning", "BACKUP_RETRY_BASE_DELAY", "Retry Base Delay", cfg.RetryBaseDelay, func(c *backupapp.Config, v time.Duration) { c.RetryBaseDelay = v })
	addDuration("Tuning", "BACKUP_RETRY_MAX_DELAY", "Retry Max Delay", cfg.RetryMaxDelay, func(c *backupapp.Config, v time.Duration) { c.RetryMaxDelay = v })
	addDuration("Tuning", "BACKUP_DISCOVERY_TIMEOUT", "Discovery Timeout", cfg.DiscoveryTimeout, func(c *backupapp.Config, v time.Duration) { c.DiscoveryTimeout = v })
	addDuration("Tuning", "BACKUP_PREFLIGHT_TIMEOUT", "Preflight Timeout", cfg.PreflightTimeout, func(c *backupapp.Config, v time.Duration) { c.PreflightTimeout = v })
	addInt("Retention", "BACKUP_RETENTION_DAILY", "Daily Backups", cfg.RetentionDaily, func(c *backupapp.Config, v int) { c.RetentionDaily = v })
	addInt("Retention", "BACKUP_RETENTION_WEEKLY", "Weekly Backups", cfg.RetentionWeekly, func(c *backupapp.Config, v int) { c.RetentionWeekly = v })
	addInt("Retention", "BACKUP_RETENTION_MONTHLY", "Monthly Backups", cfg.RetentionMonthly, func(c *backupapp.Config, v int) { c.RetentionMonthly = v })
	addBool("Tuning", "BACKUP_CLEANUP_FAIL_FATAL", "Cleanup Fatal", cfg.CleanupFailFatal, func(c *backupapp.Config, v bool) { c.CleanupFailFatal = v })

	addString("Metrics", "BACKUP_METRICS_JOB", "Prometheus Job", cfg.MetricsJob, false, func(c *backupapp.Config, v string) { c.MetricsJob = v })
	addString("Metrics", "BACKUP_METRICS_SERVICE", "Prometheus Service", cfg.MetricsService, false, func(c *backupapp.Config, v string) { c.MetricsService = v })
	addString("Metrics", "BACKUP_METRICS_ENV", "Prometheus Env", cfg.MetricsEnv, false, func(c *backupapp.Config, v string) { c.MetricsEnv = v })

	return fields
}

func validateSchedule(value string) error {
	value = strings.TrimSpace(value)
	switch {
	case value == "always" || value == "disabled":
		return nil
	case strings.HasPrefix(value, "daily@"):
		return nil
	case strings.HasPrefix(value, "weekly@"):
		return nil
	case strings.HasPrefix(value, "interval@"):
		_, err := time.ParseDuration(strings.TrimPrefix(value, "interval@"))
		if err != nil {
			return fmt.Errorf("invalid interval duration")
		}
		return nil
	default:
		return fmt.Errorf("expected always, disabled, daily@HH:MM, weekly@day,HH:MM, or interval@24h")
	}
}

func parseTUIList(raw string) []string {
	items := []string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			items = append(items, part)
		}
	}
	return items
}

func parseTUITableScope(raw string) map[string][]string {
	scope := map[string][]string{}
	for _, dbSpec := range strings.Split(raw, ";") {
		dbSpec = strings.TrimSpace(dbSpec)
		if dbSpec == "" {
			continue
		}
		dbName, tableSpec, ok := strings.Cut(dbSpec, ":")
		if !ok {
			continue
		}
		dbName = strings.TrimSpace(dbName)
		if dbName == "" {
			continue
		}
		scope[dbName] = parseTUIList(tableSpec)
	}
	return scope
}

func formatTUITableScope(scope map[string][]string) string {
	if len(scope) == 0 {
		return ""
	}
	dbs := make([]string, 0, len(scope))
	for dbName := range scope {
		dbs = append(dbs, dbName)
	}
	slices.Sort(dbs)
	parts := []string{}
	for _, dbName := range dbs {
		tables := append([]string{}, scope[dbName]...)
		slices.Sort(tables)
		parts = append(parts, dbName+":"+strings.Join(tables, ","))
	}
	return strings.Join(parts, ";")
}

func optionList(options []string, selectedIndex int) []string {
	lines := []string{}
	for i, option := range options {
		if i == selectedIndex {
			lines = append(lines, selected.Render(option))
		} else {
			lines = append(lines, "  "+option)
		}
	}
	return lines
}

func card(label, value string, width int) string {
	return panel.Width(clampNonNegative(width - 6)).Render(title.Render(label) + "\n" + value)
}

func healthLine(check backupapp.HealthCheck) string {
	return statusStyled(check.Status) + " - " + check.Message
}

func statusStyled(status string) string {
	switch status {
	case "ok", "success":
		return good.Render("✔ " + status)
	case "disabled", "partial":
		return warn.Render("⚠ " + status)
	default:
		return bad.Render("✖ " + emptyDefault(status, "unknown"))
	}
}

func focusName(f focusRegion) string {
	if f == focusSidebar {
		return "sidebar"
	}
	return "content"
}

func emptyDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
