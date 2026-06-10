package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"sdl/sdl_db_backup/internal/backupapp"
)

type screenID int

const (
	screenDashboard screenID = iota
	screenBackup
	screenSchedule
	screenConfig
	screenLogs
	screenHistory
	screenHealth
	screenSystemd
)

type focusRegion int

const (
	focusSidebar focusRegion = iota
	focusContent
)

type backupStep int

const (
	stepMode backupStep = iota
	stepUpload
	stepScope
	stepPreview
	stepRunning
	stepDone
)

type toast struct {
	level string
	text  string
	at    time.Time
}

type configField struct {
	Group  string
	Key    string
	Label  string
	Value  string
	Secret bool
	Apply  func(*backupapp.Config, string) error
	Error  string
}

type model struct {
	envPath string
	cfg     backupapp.Config
	draft   backupapp.Config
	dirty   bool

	width  int
	height int

	activeScreen    screenID
	sidebarSelected int
	focus           focusRegion

	health       backupapp.HealthReport
	healthLoaded bool
	healthErr    string
	systemd      backupapp.UnitStatus
	systemdErr   string
	systemdLast  string
	history      []backupapp.RunResult
	historyErr   string

	fields         []configField
	configSelected int
	configView     viewport.Model
	searchActive   bool
	searchInput    textinput.Model
	editorActive   bool
	editorInput    textinput.Model
	editorIndex    int
	revealSecrets  bool

	scheduleSelected  int
	scheduleEditing   bool
	scheduleChoosing  bool
	scheduleInput     textinput.Model
	scheduleEditKey   string
	scheduleChoiceKey string
	scheduleChoiceIdx int
	scheduleModeTemp  bool
	scheduleDuration  int
	tempOverrides     *backupapp.TemporaryOverrides
	tempErr           string

	backupStep          backupStep
	manualMode          backupapp.ManualRunMode
	manualUpload        backupapp.ManualUploadMode
	manualForce         bool
	manualSelected      int
	scopeLevel          string
	scopeLoading        bool
	scopeErr            string
	scopeDatabases      []string
	scopeTables         []string
	scopeDBIndex        int
	scopeTableIndex     int
	scopeDBMark         int
	scopeTableMark      int
	scopeTableFilter    string
	scopeTableSearch    textinput.Model
	scopeSearchActive   bool
	scopeSelectedDBs    map[string]bool
	scopeSelectedTables map[string]map[string]bool
	runPreview          backupapp.Preview
	runConfig           backupapp.Config
	running             bool
	runResult           *backupapp.RunResult
	runErr              string
	runLogLines         []string
	runLogView          viewport.Model
	runLogCh            <-chan string
	runProgress         runProgressState
	spinner             spinner.Model
	progress            progress.Model

	dashboardView viewport.Model
	healthView    viewport.Model
	systemdView   viewport.Model

	logView       viewport.Model
	logLines      []string
	logFilter     string
	logFollow     bool
	logSource     string
	logSearchMode bool
	logSearch     textinput.Model

	commandOpen  bool
	commandInput textinput.Model
	commandIndex int

	helpOpen        bool
	confirmMessage  string
	confirmAction   func() tea.Cmd
	confirmSelected int

	toasts []toast
}

type healthMsg struct {
	report backupapp.HealthReport
	err    error
}

type systemdMsg struct {
	status backupapp.UnitStatus
	err    error
}

type historyMsg struct {
	runs []backupapp.RunResult
	err  error
}

type scopeDatabasesMsg struct {
	databases []string
	err       error
}

type scopeTablesMsg struct {
	dbName string
	tables []string
	err    error
}

type logsMsg struct {
	source  string
	lines   []string
	missing bool
	err     error
}

type runLogMsg struct {
	line string
}

type runLogsClosedMsg struct{}

type runFinishedMsg struct {
	result backupapp.RunResult
	err    error
}

type startBackupMsg struct {
	cfg backupapp.Config
}

type saveConfigMsg struct {
	cfg backupapp.Config
	err error
}

type reloadConfigMsg struct {
	cfg backupapp.Config
	err error
}

type temporaryOverridesMsg struct {
	overrides backupapp.TemporaryOverrides
	active    bool
	err       error
}

type temporaryOverridesSavedMsg struct {
	action string
	err    error
}

type systemdActionMsg struct {
	action string
	err    error
}

var (
	colorBG      = lipgloss.Color("#0D1117")
	colorPanel   = lipgloss.Color("#161B22")
	colorPanel2  = lipgloss.Color("#1C2128")
	colorBorder  = lipgloss.Color("#30363D")
	colorMuted   = lipgloss.Color("#8B949E")
	colorText    = lipgloss.Color("#F0F6FC")
	colorAccent  = lipgloss.Color("#58A6FF")
	colorGood    = lipgloss.Color("#3FB950")
	colorWarn    = lipgloss.Color("#D29922")
	colorBad     = lipgloss.Color("#F85149")
	colorMagenta = lipgloss.Color("#BC8CFF")

	baseStyle          = lipgloss.NewStyle().Foreground(colorText).Background(colorBG)
	panel              = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Background(colorPanel).Padding(0, 1)
	activePanel        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Background(colorPanel).Padding(0, 1)
	inactivePanel      = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorBorder).Background(colorPanel).Padding(0, 1)
	floatingPanel      = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(colorAccent).Background(colorPanel).Padding(1, 2)
	sidebarPanel       = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorBorder).Background(colorPanel).Padding(0, 1).BorderRight(false)
	sidebarPanelActive = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorAccent).Background(colorPanel).Padding(0, 1).BorderRight(false)
	contentPanel       = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorBorder).Background(colorPanel).Padding(0, 2)
	contentPanelActive = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorAccent).Background(colorPanel).Padding(0, 2)
	fileBlock          = lipgloss.NewStyle().Background(colorPanel2).Padding(0, 1)
	title              = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	sectionTitle       = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Underline(true)
	muted              = lipgloss.NewStyle().Foreground(colorMuted)
	good               = lipgloss.NewStyle().Foreground(colorGood).Bold(true)
	warn               = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	bad                = lipgloss.NewStyle().Foreground(colorBad).Bold(true)
	selected           = lipgloss.NewStyle().Foreground(colorBG).Background(colorAccent).Bold(true).Padding(0, 1)
	selectedDim        = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	statusPill         = lipgloss.NewStyle().Foreground(colorBG).Background(colorAccent).Bold(true).Padding(0, 1)
	statusDimPill      = lipgloss.NewStyle().Foreground(colorText).Background(colorBorder).Padding(0, 1)
	keyHint            = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	rowAlt             = lipgloss.NewStyle().Background(colorPanel2)
)

var screens = []struct {
	name string
	key  string
}{
	{"📊 Dashboard", "1"},
	{"💾 Backup", "2"},
	{"📅 Schedule", "3"},
	{"⚙ Config", "4"},
	{"📜 Logs", "5"},
	{"🕘 History", "6"},
	{"🩺 Health", "7"},
	{"🛠 Systemd", "8"},
}

func Run(envPath string, cfg backupapp.Config) error {
	m := newModel(envPath, cfg)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newModel(envPath string, cfg backupapp.Config) model {
	search := textinput.New()
	search.Placeholder = "search settings"
	search.Prompt = "/ "

	editor := textinput.New()
	editor.Prompt = "> "

	scheduleInput := textinput.New()
	scheduleInput.Prompt = "> "

	logSearch := textinput.New()
	logSearch.Placeholder = "filter logs"
	logSearch.Prompt = "/ "

	scopeSearch := textinput.New()
	scopeSearch.Placeholder = "search tables"
	scopeSearch.Prompt = "/ "

	cmd := textinput.New()
	cmd.Placeholder = "type a command"
	cmd.Prompt = ": "

	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = lipgloss.NewStyle().Foreground(colorAccent)

	runLog := viewport.New(80, 12)
	logs := viewport.New(80, 12)
	configView := viewport.New(80, 12)
	dashboardView := viewport.New(80, 12)
	healthView := viewport.New(80, 12)
	systemdView := viewport.New(80, 12)

	m := model{
		envPath:             envPath,
		cfg:                 cfg,
		draft:               cfg,
		activeScreen:        screenDashboard,
		focus:               focusContent,
		fields:              buildConfigFields(cfg),
		dashboardView:       dashboardView,
		healthView:          healthView,
		systemdView:         systemdView,
		configView:          configView,
		searchInput:         search,
		editorInput:         editor,
		editorIndex:         -1,
		scheduleInput:       scheduleInput,
		manualMode:          backupapp.ManualRunBoth,
		manualUpload:        backupapp.ManualUploadNormal,
		manualForce:         true,
		scopeLevel:          "databases",
		scopeDBMark:         -1,
		scopeTableMark:      -1,
		scopeTableSearch:    scopeSearch,
		scopeSelectedDBs:    map[string]bool{},
		scopeSelectedTables: map[string]map[string]bool{},
		backupStep:          stepMode,
		runLogView:          runLog,
		logView:             logs,
		logFollow:           true,
		logSource:           "daily",
		logSearch:           logSearch,
		commandInput:        cmd,
		spinner:             spin,
		progress:            progress.New(progress.WithDefaultGradient()),
		toasts:              []toast{{level: "info", text: "TUI ready", at: time.Now()}},
	}
	m.loadScopeFromConfig(cfg)
	m.rebuildPreview()
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		loadHealth(m.envPath),
		loadTemporaryOverrides(m.envPath),
		loadHistory(m.cfg.RunLogPath),
		loadLogs("daily", filepath.Join(m.cfg.LogDir, time.Now().Format("2006-01-02")+".log")),
		m.spinner.Tick,
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewports()
		if m.activeScreen == screenConfig {
			m.syncConfigViewport(m.visibleFields())
		}
	case healthMsg:
		m.healthErr = ""
		if msg.err != nil {
			m.healthErr = msg.err.Error()
			m.pushToast("error", "health refresh failed: "+m.healthErr)
		} else {
			m.health = msg.report
			m.healthLoaded = true
			m.pushToast("ok", "health refreshed")
		}
	case systemdMsg:
		m.systemdErr = ""
		if msg.err != nil {
			m.systemdErr = msg.err.Error()
			m.pushToast("error", "systemd refresh failed: "+m.systemdErr)
		} else {
			m.systemd = msg.status
			m.systemdLast = "Refreshed status successfully"
			m.pushToast("ok", "systemd status refreshed")
		}
	case historyMsg:
		m.historyErr = ""
		if msg.err != nil {
			m.historyErr = msg.err.Error()
		} else {
			m.history = msg.runs
		}
	case scopeDatabasesMsg:
		m.scopeLoading = false
		if msg.err != nil {
			m.scopeErr = msg.err.Error()
			m.pushToast("error", "database discovery failed: "+m.scopeErr)
		} else {
			m.scopeErr = ""
			m.scopeDatabases = msg.databases
			m.scopeDBIndex = 0
			m.scopeDBMark = -1
			m.pushToast("ok", fmt.Sprintf("found %d databases", len(msg.databases)))
		}
	case scopeTablesMsg:
		m.scopeLoading = false
		if msg.err != nil {
			m.scopeErr = msg.err.Error()
			m.pushToast("error", "table discovery failed: "+m.scopeErr)
		} else {
			m.scopeErr = ""
			m.scopeTables = msg.tables
			m.scopeTableIndex = 0
			m.scopeTableMark = -1
			m.scopeTableFilter = ""
			m.scopeTableSearch.SetValue("")
			m.scopeSearchActive = false
			m.scopeLevel = "tables"
			m.pushToast("ok", fmt.Sprintf("found %d tables in %s", len(msg.tables), msg.dbName))
		}
	case logsMsg:
		if msg.err != nil {
			m.pushToast("error", "log load failed: "+msg.err.Error())
		} else if msg.missing {
			m.logSource = msg.source
			m.logLines = msg.lines
			m.updateLogViewport()
			m.pushToast("info", msg.source+" log has not been created yet")
		} else {
			m.logSource = msg.source
			m.logLines = msg.lines
			m.updateLogViewport()
			m.pushToast("ok", "loaded "+msg.source+" logs")
		}
	case runLogMsg:
		m.runLogLines = append(m.runLogLines, msg.line)
		if len(m.runLogLines) > 2000 {
			m.runLogLines = m.runLogLines[len(m.runLogLines)-2000:]
		}
		m.runProgress.applyLogLine(msg.line)
		m.updateRunLogViewport()
		if m.runLogCh != nil {
			cmds = append(cmds, waitForRunLog(m.runLogCh))
		}
	case runLogsClosedMsg:
		m.runLogCh = nil
	case runFinishedMsg:
		m.running = false
		m.backupStep = stepDone
		if msg.err != nil {
			m.runErr = msg.err.Error()
			m.pushToast("error", "backup failed")
		} else {
			m.runResult = &msg.result
			m.runErr = ""
			m.pushToast("ok", "backup finished: "+msg.result.Status)
			cmds = append(cmds, loadHealth(m.envPath), loadHistory(m.cfg.RunLogPath))
		}
	case saveConfigMsg:
		if msg.err != nil {
			m.pushToast("error", "save failed: "+msg.err.Error())
		} else {
			m.cfg = msg.cfg
			m.draft = msg.cfg
			m.fields = buildConfigFields(msg.cfg)
			m.dirty = false
			m.pushToast("ok", ".env saved")
			cmds = append(cmds, loadHealth(m.envPath), loadTemporaryOverrides(m.envPath))
		}
	case reloadConfigMsg:
		if msg.err != nil {
			m.pushToast("error", "reload failed: "+msg.err.Error())
		} else {
			m.cfg = msg.cfg
			m.draft = msg.cfg
			m.fields = buildConfigFields(msg.cfg)
			m.dirty = false
			m.loadScopeFromConfig(msg.cfg)
			m.rebuildPreview()
			m.pushToast("ok", ".env reloaded")
			cmds = append(cmds, loadHealth(m.envPath), loadTemporaryOverrides(m.envPath))
		}
	case temporaryOverridesMsg:
		m.tempErr = ""
		if msg.err != nil {
			m.tempErr = msg.err.Error()
			m.pushToast("error", "temporary override check failed: "+m.tempErr)
		} else if msg.active {
			m.tempOverrides = &msg.overrides
		} else {
			m.tempOverrides = nil
		}
	case temporaryOverridesSavedMsg:
		if msg.err != nil {
			m.pushToast("error", "schedule apply failed: "+msg.err.Error())
		} else {
			if msg.action == "temporary" {
				m.pushToast("ok", "temporary schedule overrides saved")
			} else if msg.action == "clear" {
				m.tempOverrides = nil
				m.pushToast("ok", "temporary schedule overrides cleared")
			} else {
				m.cfg = m.draft
				m.fields = buildConfigFields(m.cfg)
				m.dirty = false
				m.pushToast("ok", "schedule saved permanently")
			}
			cmds = append(cmds, loadTemporaryOverrides(m.envPath), loadHealth(m.envPath))
		}
	case startBackupMsg:
		m.running = true
		m.backupStep = stepRunning
		m.activeScreen = screenBackup
		m.sidebarSelected = int(screenBackup)
		m.runResult = nil
		m.runErr = ""
		m.runLogLines = nil
		m.runProgress = runProgressState{}
		m.runLogView.SetContent("")
		ch := make(chan string, 256)
		m.runLogCh = ch
		cmds = append(cmds, waitForRunLog(ch), runBackup(msg.cfg, ch))
	case systemdActionMsg:
		if msg.err != nil {
			m.systemdLast = msg.action + " failed: " + msg.err.Error()
			m.pushToast("error", "systemd action failed: "+msg.err.Error())
		} else {
			m.systemdLast = msg.action + " completed successfully"
			m.pushToast("ok", "systemd action completed")
			cmds = append(cmds, loadSystemd(m.envPath))
		}
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		updated, keyCmd := m.handleKey(key)
		m = updated
		cmds = append(cmds, keyCmd)
	}

	if m.commandOpen {
		m.commandInput, cmd = m.commandInput.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}
	if m.helpOpen || m.confirmMessage != "" {
		return m, tea.Batch(cmds...)
	}

	if m.editorActive {
		m.editorInput, cmd = m.editorInput.Update(msg)
		cmds = append(cmds, cmd)
	}
	if m.scheduleEditing {
		m.scheduleInput, cmd = m.scheduleInput.Update(msg)
		cmds = append(cmds, cmd)
	}
	if m.searchActive {
		m.searchInput, cmd = m.searchInput.Update(msg)
		if m.activeScreen == screenConfig {
			m.syncConfigViewport(m.visibleFields())
		}
		cmds = append(cmds, cmd)
	}
	if m.logSearchMode {
		m.logSearch, cmd = m.logSearch.Update(msg)
		m.logFilter = m.logSearch.Value()
		m.updateLogViewport()
		cmds = append(cmds, cmd)
	}
	if m.scopeSearchActive {
		m.scopeTableSearch, cmd = m.scopeTableSearch.Update(msg)
		m.scopeTableFilter = m.scopeTableSearch.Value()
		m.ensureScopeTableVisible()
		cmds = append(cmds, cmd)
	}
	m.runLogView, cmd = m.runLogView.Update(msg)
	cmds = append(cmds, cmd)
	m.logView, cmd = m.logView.Update(msg)
	cmds = append(cmds, cmd)
	m.configView, cmd = m.configView.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) screenRefreshCmd() tea.Cmd {
	switch m.activeScreen {
	case screenDashboard, screenHealth:
		return loadHealth(m.envPath)
	case screenSchedule:
		return loadTemporaryOverrides(m.envPath)
	case screenHistory:
		return loadHistory(m.cfg.RunLogPath)
	case screenLogs:
		return loadLogs("daily", filepath.Join(m.cfg.LogDir, time.Now().Format("2006-01-02")+".log"))
	case screenSystemd:
		return loadSystemd(m.envPath)
	default:
		return nil
	}
}

func (m model) applyManualSelection() (model, tea.Cmd) {
	switch m.backupStep {
	case stepMode:
		switch m.manualSelected {
		case 0:
			m.manualMode = backupapp.ManualRunBoth
		case 1:
			m.manualMode = backupapp.ManualRunLogicalOnly
		case 2:
			m.manualMode = backupapp.ManualRunPhysicalOnly
		}
		m.notifyAction("backup mode set to " + string(m.manualMode))
		m.backupStep = stepUpload
		m.manualSelected = 0
	case stepUpload:
		if m.manualSelected == 0 {
			m.manualUpload = backupapp.ManualUploadNormal
		} else {
			m.manualUpload = backupapp.ManualUploadLocalOnly
		}
		m.notifyAction("upload mode set to " + string(m.manualUpload))
		m.backupStep = stepScope
		m.manualSelected = 0
		if len(m.scopeDatabases) == 0 && !m.scopeLoading {
			m.scopeLoading = true
			return m, loadScopeDatabases(m.draft)
		}
	case stepScope:
		m.backupStep = stepPreview
		m.manualSelected = 0
		m.notifyAction("continued to preview")
	case stepPreview:
		if m.manualSelected == 0 {
			m.manualForce = !m.manualForce
			m.notifyAction("force run now set to " + strconv.FormatBool(m.manualForce))
		} else {
			m.rebuildPreview()
			cfg := m.runConfig
			m.notifyAction("backup start confirmation opened")
			m.confirm("Start manual backup now?", func() tea.Cmd {
				return func() tea.Msg { return startBackupMsg{cfg: cfg} }
			})
		}
	case stepDone:
		m.backupStep = stepMode
		m.manualSelected = 0
		m.runResult = nil
		m.runErr = ""
		m.runLogLines = nil
		m.runProgress = runProgressState{}
		m.notifyAction("manual backup workflow reset")
	}
	m.rebuildPreview()
	return m, nil
}

func (m model) maxManualSelection() int {
	switch m.backupStep {
	case stepMode:
		return 2
	case stepUpload:
		return 1
	case stepScope:
		return 0
	case stepPreview:
		return 1
	default:
		return 0
	}
}

func (m *model) rebuildPreview() {
	cfg, preview, err := backupapp.BuildManualRunConfig(m.draft, backupapp.ManualRunOptions{
		Mode:       m.manualMode,
		UploadMode: m.manualUpload,
		ForceNow:   m.manualForce,
	})
	m.applyScopeToConfig(&cfg)
	if err != nil {
		preview.Warnings = append(preview.Warnings, err.Error())
	}
	preview.Lines = append(preview.Lines, m.scopePreviewLines()...)
	m.runConfig = cfg
	m.runPreview = preview
}

func (m model) saveScopePermanently() (model, tea.Cmd) {
	cfg := m.draft
	m.applyScopeToConfig(&cfg)
	m.draft = cfg
	m.fields = buildConfigFields(cfg)
	m.rebuildPreview()
	m.pushToast("info", "saving logical backup scope to .env")
	return m, saveConfig(m.envPath, cfg)
}

func (m *model) loadScopeFromConfig(cfg backupapp.Config) {
	m.scopeSelectedDBs = map[string]bool{}
	m.scopeSelectedTables = map[string]map[string]bool{}
	for _, dbName := range cfg.LogicalDatabases {
		m.scopeSelectedDBs[dbName] = true
	}
	for dbName, tables := range cfg.LogicalTables {
		m.scopeSelectedDBs[dbName] = true
		if m.scopeSelectedTables[dbName] == nil {
			m.scopeSelectedTables[dbName] = map[string]bool{}
		}
		for _, tableName := range tables {
			m.scopeSelectedTables[dbName][tableName] = true
		}
	}
}

func (m *model) toggleSelectedDatabase() {
	if len(m.scopeDatabases) == 0 {
		return
	}
	dbName := m.scopeDatabases[m.scopeDBIndex]
	if m.scopeSelectedDBs[dbName] {
		delete(m.scopeSelectedDBs, dbName)
		delete(m.scopeSelectedTables, dbName)
		return
	}
	m.scopeSelectedDBs[dbName] = true
}

func (m *model) toggleSelectedTable() {
	if len(m.scopeDatabases) == 0 || len(m.scopeTables) == 0 {
		return
	}
	dbName := m.scopeDatabases[m.scopeDBIndex]
	tableName := m.scopeTables[m.scopeTableIndex]
	if m.scopeSelectedTables[dbName] == nil {
		m.scopeSelectedTables[dbName] = map[string]bool{}
	}
	if m.scopeSelectedTables[dbName][tableName] {
		delete(m.scopeSelectedTables[dbName], tableName)
		if len(m.scopeSelectedTables[dbName]) == 0 {
			delete(m.scopeSelectedTables, dbName)
		}
		return
	}
	m.scopeSelectedDBs[dbName] = true
	m.scopeSelectedTables[dbName][tableName] = true
}

func (m *model) selectAllDatabases() {
	for _, dbName := range m.scopeDatabases {
		m.scopeSelectedDBs[dbName] = true
		delete(m.scopeSelectedTables, dbName)
	}
}

func (m *model) selectAllTablesForCurrentDB() {
	if len(m.scopeDatabases) == 0 || len(m.scopeTables) == 0 {
		return
	}
	dbName := m.scopeDatabases[m.scopeDBIndex]
	m.scopeSelectedDBs[dbName] = true
	if m.scopeSelectedTables[dbName] == nil {
		m.scopeSelectedTables[dbName] = map[string]bool{}
	}
	for _, tableIndex := range m.visibleScopeTableIndexes() {
		m.scopeSelectedTables[dbName][m.scopeTables[tableIndex]] = true
	}
}

func (m *model) clearSelectedTablesForCurrentDB() {
	if len(m.scopeDatabases) == 0 {
		return
	}
	dbName := m.scopeDatabases[m.scopeDBIndex]
	delete(m.scopeSelectedTables, dbName)
	m.scopeSelectedDBs[dbName] = true
}

func (m *model) markScopeRangeStart() {
	if m.scopeLevel == "tables" {
		if len(m.scopeTables) > 0 {
			m.scopeTableMark = m.scopeTableIndex
		}
		return
	}
	if len(m.scopeDatabases) > 0 {
		m.scopeDBMark = m.scopeDBIndex
	}
}

func (m *model) selectMarkedScopeRange() {
	if m.scopeLevel == "tables" {
		if len(m.scopeDatabases) == 0 || len(m.scopeTables) == 0 {
			return
		}
		dbName := m.scopeDatabases[m.scopeDBIndex]
		m.scopeSelectedDBs[dbName] = true
		if m.scopeSelectedTables[dbName] == nil {
			m.scopeSelectedTables[dbName] = map[string]bool{}
		}
		for _, tableIndex := range m.markedScopeTableIndexes() {
			m.scopeSelectedTables[dbName][m.scopeTables[tableIndex]] = true
		}
		return
	}
	if len(m.scopeDatabases) == 0 {
		return
	}
	start, end := m.scopeDBRange()
	for i := start; i <= end; i++ {
		dbName := m.scopeDatabases[i]
		m.scopeSelectedDBs[dbName] = true
		delete(m.scopeSelectedTables, dbName)
	}
}

func (m *model) clearMarkedScopeRange() {
	if m.scopeLevel == "tables" {
		if len(m.scopeDatabases) == 0 || len(m.scopeTables) == 0 {
			return
		}
		dbName := m.scopeDatabases[m.scopeDBIndex]
		selectedTables := m.scopeSelectedTables[dbName]
		if selectedTables == nil {
			return
		}
		for _, tableIndex := range m.markedScopeTableIndexes() {
			delete(selectedTables, m.scopeTables[tableIndex])
		}
		if len(selectedTables) == 0 {
			delete(m.scopeSelectedTables, dbName)
			m.scopeSelectedDBs[dbName] = true
		}
		return
	}
	if len(m.scopeDatabases) == 0 {
		return
	}
	start, end := m.scopeDBRange()
	for i := start; i <= end; i++ {
		dbName := m.scopeDatabases[i]
		delete(m.scopeSelectedDBs, dbName)
		delete(m.scopeSelectedTables, dbName)
	}
}

func (m model) scopeDBRange() (int, int) {
	start := m.scopeDBMark
	if start < 0 || start >= len(m.scopeDatabases) {
		start = m.scopeDBIndex
	}
	end := m.scopeDBIndex
	if start > end {
		start, end = end, start
	}
	return start, end
}

func (m model) scopeTableRange() (int, int) {
	visible := m.visibleScopeTableIndexes()
	if len(visible) == 0 {
		return 0, 0
	}
	start := m.scopeVisibleTablePosition(m.scopeTableMark)
	if start < 0 {
		start = m.scopeVisibleTablePosition(m.scopeTableIndex)
	}
	end := m.scopeVisibleTablePosition(m.scopeTableIndex)
	if end < 0 {
		end = start
	}
	if start > end {
		start, end = end, start
	}
	return start, end
}

func (m model) markedScopeTableIndexes() []int {
	visible := m.visibleScopeTableIndexes()
	if len(visible) == 0 {
		return nil
	}
	start, end := m.scopeTableRange()
	return append([]int{}, visible[start:end+1]...)
}

func (m *model) moveScopeSelection(delta int) {
	if m.scopeLevel == "tables" {
		m.moveScopeTableSelection(delta)
		return
	}
	if len(m.scopeDatabases) == 0 {
		return
	}
	m.scopeDBIndex = clampIndex(m.scopeDBIndex+delta, len(m.scopeDatabases))
}

func (m *model) moveScopeSelectionToStart() {
	if m.scopeLevel == "tables" {
		visible := m.visibleScopeTableIndexes()
		if len(visible) > 0 {
			m.scopeTableIndex = visible[0]
		}
		return
	}
	m.scopeDBIndex = 0
}

func (m *model) moveScopeSelectionToEnd() {
	if m.scopeLevel == "tables" {
		visible := m.visibleScopeTableIndexes()
		if len(visible) > 0 {
			m.scopeTableIndex = visible[len(visible)-1]
		}
		return
	}
	if len(m.scopeDatabases) > 0 {
		m.scopeDBIndex = len(m.scopeDatabases) - 1
	}
}

func clampIndex(index, length int) int {
	if length == 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

func (m model) visibleScopeTableIndexes() []int {
	query := strings.ToLower(strings.TrimSpace(m.scopeTableFilter))
	indexes := make([]int, 0, len(m.scopeTables))
	for i, tableName := range m.scopeTables {
		if query == "" || strings.Contains(strings.ToLower(tableName), query) {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func (m model) scopeVisibleTablePosition(tableIndex int) int {
	for pos, visibleIndex := range m.visibleScopeTableIndexes() {
		if visibleIndex == tableIndex {
			return pos
		}
	}
	return -1
}

func (m *model) moveScopeTableSelection(delta int) {
	visible := m.visibleScopeTableIndexes()
	if len(visible) == 0 {
		return
	}
	pos := m.scopeVisibleTablePosition(m.scopeTableIndex)
	if pos < 0 {
		if delta < 0 {
			pos = len(visible) - 1
		} else {
			pos = 0
		}
	} else {
		pos = clampIndex(pos+delta, len(visible))
	}
	m.scopeTableIndex = visible[pos]
}

func (m *model) moveScopeTableSelectionHorizontal(deltaCols int) {
	rows, _ := m.scopeTableGridShape(max(1, clampNonNegative(m.width-m.sidebarWidth()-6)))
	m.moveScopeTableSelection(deltaCols * max(1, rows))
}

func (m *model) ensureScopeTableVisible() {
	visible := m.visibleScopeTableIndexes()
	if len(visible) == 0 {
		return
	}
	if m.scopeVisibleTablePosition(m.scopeTableIndex) < 0 {
		m.scopeTableIndex = visible[0]
	}
	if m.scopeTableMark >= 0 && m.scopeVisibleTablePosition(m.scopeTableMark) < 0 {
		m.scopeTableMark = -1
	}
}

func (m model) applyScopeToConfig(cfg *backupapp.Config) {
	if len(m.scopeSelectedDBs) == 0 {
		cfg.LogicalDatabases = nil
		cfg.LogicalTables = nil
		return
	}
	dbs := make([]string, 0, len(m.scopeSelectedDBs))
	for dbName := range m.scopeSelectedDBs {
		dbs = append(dbs, dbName)
	}
	slices.Sort(dbs)
	cfg.LogicalDatabases = dbs
	cfg.LogicalTables = map[string][]string{}
	for dbName, selectedTables := range m.scopeSelectedTables {
		tables := make([]string, 0, len(selectedTables))
		for table := range selectedTables {
			tables = append(tables, table)
		}
		slices.Sort(tables)
		if len(tables) > 0 {
			cfg.LogicalTables[dbName] = tables
		}
	}
	if len(cfg.LogicalTables) == 0 {
		cfg.LogicalTables = nil
	}
}

func (m model) scopePreviewLines() []string {
	if len(m.scopeSelectedDBs) == 0 {
		return []string{"Scope: all granted databases and all validated tables/views"}
	}
	lines := []string{"Scope: selected databases/tables/views"}
	dbs := make([]string, 0, len(m.scopeSelectedDBs))
	for dbName := range m.scopeSelectedDBs {
		dbs = append(dbs, dbName)
	}
	slices.Sort(dbs)
	for _, dbName := range dbs {
		tableMap := m.scopeSelectedTables[dbName]
		if len(tableMap) == 0 {
			lines = append(lines, "- "+dbName+": all validated tables/views")
			continue
		}
		tables := make([]string, 0, len(tableMap))
		for tableName := range tableMap {
			tables = append(tables, tableName)
		}
		slices.Sort(tables)
		if len(tables) > 8 {
			lines = append(lines, fmt.Sprintf("- %s: %d selected objects (%s, ...)", dbName, len(tables), strings.Join(tables[:6], ", ")))
			continue
		}
		lines = append(lines, "- "+dbName+": "+strings.Join(tables, ", "))
	}
	return lines
}

func (m model) scopeTableGridLines(width int, dbName string) []string {
	rows, cols := m.scopeTableGridShape(width)
	visible := m.visibleScopeTableIndexes()
	if len(visible) == 0 {
		if strings.TrimSpace(m.scopeTableFilter) != "" {
			return []string{warn.Render("No tables or views match /" + m.scopeTableFilter)}
		}
		return []string{"No tables or views discovered."}
	}
	pageSize := rows * cols
	selectedPos := m.scopeVisibleTablePosition(m.scopeTableIndex)
	if selectedPos < 0 {
		selectedPos = 0
	}
	pageStart := (selectedPos / pageSize) * pageSize
	pageEnd := min(pageStart+pageSize, len(visible))
	colWidth := max(1, (width-4-(cols-1)*2)/cols)
	lines := []string{
		muted.Render(m.scopeTableCountLine(pageStart, pageEnd, len(visible))),
	}
	if m.scopeTableMark >= 0 && m.scopeTableMark < len(m.scopeTables) {
		start, end := m.scopeTableRange()
		lines = append(lines, muted.Render(fmt.Sprintf("Range mark: %s (%d selected by range)", m.scopeTables[m.scopeTableMark], end-start+1)))
	}
	for row := 0; row < rows; row++ {
		cells := []string{}
		for col := 0; col < cols; col++ {
			pos := pageStart + col*rows + row
			if pos >= pageEnd {
				continue
			}
			i := visible[pos]
			tableName := m.scopeTables[i]
			marker := "[ ]"
			if m.scopeSelectedTables[dbName] != nil && m.scopeSelectedTables[dbName][tableName] {
				marker = "[x]"
			}
			line := fitCell(marker+" "+tableName, colWidth)
			if i == m.scopeTableIndex {
				line = selected.Width(colWidth).Render(line)
			} else {
				line = fmt.Sprintf("%-*s", colWidth, line)
			}
			cells = append(cells, line)
		}
		if len(cells) > 0 {
			lines = append(lines, strings.Join(cells, "  "))
		}
	}
	return lines
}

func (m model) scopeTableCountLine(pageStart, pageEnd, visibleCount int) string {
	if strings.TrimSpace(m.scopeTableFilter) == "" {
		return fmt.Sprintf("Showing %d-%d of %d tables. / search. PageUp/PageDown jumps. Home/End jumps to first/last.", pageStart+1, pageEnd, len(m.scopeTables))
	}
	return fmt.Sprintf("Search /%s: showing %d-%d of %d matches (%d total). d selects shown matches. Ctrl+W clears search.", m.scopeTableFilter, pageStart+1, pageEnd, visibleCount, len(m.scopeTables))
}

func (m model) scopeTableGridShape(width int) (int, int) {
	cols := 1
	switch {
	case width >= 108:
		cols = 3
	case width >= 72:
		cols = 2
	}
	rows := max(4, m.mainPanelHeight()-18)
	return rows, cols
}

func clampNonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func fitCell(text string, width int) string {
	if len(text) <= width {
		return text
	}
	if width <= 3 {
		return text[:width]
	}
	return text[:width-3] + "..."
}

func (m *model) confirm(message string, action func() tea.Cmd) {
	m.confirmMessage = message
	m.confirmAction = action
}

func (m *model) pushToast(level, text string) {
	m.toasts = append(m.toasts, toast{level: level, text: text, at: time.Now()})
	if len(m.toasts) > 4 {
		m.toasts = m.toasts[len(m.toasts)-4:]
	}
}

func (m *model) notifyAction(text string) {
	m.pushToast("info", text)
}
