package tui

import (
	"bufio"
	"context"
	"fmt"
	"os"
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
		if m.activeScreen == screenBackup && m.focus == focusContent && m.backupStep == stepScope && m.scopeLevel == "tables" {
			return m.handleBackupKey(key)
		}
		m.focus = focusSidebar
		m.notifyAction("focus moved to sidebar")
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8":
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
	}
	return m, nil
}

func (m model) handleContentViewportKey(key tea.KeyMsg) (model, tea.Cmd, bool) {
	switch m.activeScreen {
	case screenDashboard:
		if handled := scrollViewportByKey(&m.dashboardView, key); handled {
			return m, nil, true
		}
	case screenHealth:
		if handled := scrollViewportByKey(&m.healthView, key); handled {
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
	case "ctrl+r":
		m.notifyAction("reloading .env")
		return m, reloadConfig(m.envPath)
	}
	m.syncConfigViewport(visible)
	return m, nil
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
	if key.String() == "r" {
		m.notifyAction("refreshing history")
		return m, loadHistory(m.cfg.RunLogPath)
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
		return []string{"Scope: all granted databases and all tables"}
	}
	lines := []string{"Scope: selected databases/tables"}
	dbs := make([]string, 0, len(m.scopeSelectedDBs))
	for dbName := range m.scopeSelectedDBs {
		dbs = append(dbs, dbName)
	}
	slices.Sort(dbs)
	for _, dbName := range dbs {
		tableMap := m.scopeSelectedTables[dbName]
		if len(tableMap) == 0 {
			lines = append(lines, "- "+dbName+": all tables")
			continue
		}
		tables := make([]string, 0, len(tableMap))
		for tableName := range tableMap {
			tables = append(tables, tableName)
		}
		slices.Sort(tables)
		if len(tables) > 8 {
			lines = append(lines, fmt.Sprintf("- %s: %d selected tables (%s, ...)", dbName, len(tables), strings.Join(tables[:6], ", ")))
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
			return []string{warn.Render("No tables match /" + m.scopeTableFilter)}
		}
		return []string{"No tables discovered."}
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
		lines := []string{}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 1024), 1024*1024)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return logsMsg{source: source, err: err}
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
		return strconv.Itoa(m.draft.RetentionDays)
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
			{label: "Custom value", value: strconv.Itoa(m.draft.RetentionDays), hint: "Press e to type a custom number."},
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
		m.draft.RetentionDays = days
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
				"BACKUP_RETENTION_DAYS":             strconv.Itoa(m.draft.RetentionDays),
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
	addInt("Tuning", "BACKUP_RETENTION_DAYS", "Retention Days", cfg.RetentionDays, func(c *backupapp.Config, v int) { c.RetentionDays = v })
	addBool("Tuning", "BACKUP_CLEANUP_FAIL_FATAL", "Cleanup Fatal", cfg.CleanupFailFatal, func(c *backupapp.Config, v bool) { c.CleanupFailFatal = v })

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
