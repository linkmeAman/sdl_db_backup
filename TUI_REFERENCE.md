# SDL DB Backup TUI Reference

This document describes the current Bubble Tea TUI structure, page layout, controls, and expected UI behavior.

The TUI is now the interactive operator console for the portable backup platform. It manages the same shared config and status surface used by the runner and the built-in REST API.

## Launch

```bash
go run ./cmd/sdl-db-backup-tui
```

The TUI reads `.env` by default and edits managed backup settings through the `Config` and `Schedule` pages. It also exposes API/server settings, systemd unit names, rendered unit previews, runtime metadata, and scheduler guidance.

## Overall Layout

The TUI is terminal-size aware. It reserves vertical space for the status bar, notifications, command palette, and confirmation prompts, then gives the remaining height to the sidebar and active content panel.
The shell treats the terminal as a strict grid: the sidebar keeps a fixed width, the content pane uses the remaining width, and long pages are clipped inside viewports so they cannot expand past the available bounds.

```text
+----------------------+------------------------------------------------------+
| SDL DB Backup        | Active page content                                  |
|                      |                                                      |
| [1] 📊 Dashboard     | Page title                                           |
| [2] 💾 Backup        | Page-specific controls, tables, logs, forms, etc.   |
| [3] 📅 Schedule      |                                                      |
| [4] ⚙ Config         |                                                      |
| [5] 📜 Logs          |                                                      |
| [6] 🕘 History       |                                                      |
| [7] 🩺 Health        |                                                      |
| [8] 🛠 Systemd       |                                                      |
|                      |                                                      |
| Env: .env            |                                                      |
+----------------------+------------------------------------------------------+
| [ 🩺 Health ] [ ◩ Content ] [ 📁 .env ]      [Tab] Focus [?] Help [:] Menu |
+-----------------------------------------------------------------------------+
| Notifications                                                                |
| 12:35:12 │ [ ✔ OK ] health refreshed                                         |
+-----------------------------------------------------------------------------+
```

Main regions:
- Sidebar: page navigation, active page highlight, env path, dirty config marker, focus indicator.
- Content panel: active page body.
- Status bar: page/focus/env on the left, global key hints on the right.
- Notifications popup: last three command results or state changes.
- Command palette: opens as a centered thick-bordered panel when active.
- Confirm panel: opens at the bottom for destructive or important actions.
- Dashboard, Health, and Systemd bodies render inside bounded viewports so long text can be scrolled instead of pushing the shell layout off-screen.

## Visual Design System

Semantic colors:
- Primary accent blue: active selections, focused borders, app title, key labels.
- Muted gray: inactive borders, secondary text, hints, future workflow steps.
- Success green: successful backups, valid checks, completed workflow steps.
- Warning yellow: unsaved changes, partial/disabled status, temporary overrides.
- Error red: failed backups, invalid values, failed commands.

Focus is visual:
- Active region uses a rounded accent border.
- Inactive region uses a muted normal border.
- The sidebar no longer needs a separate focus text indicator because the border shows focus.

Icon conventions:
- Sidebar pages use compact icons for scanning.
- Success uses `✔`.
- Warning uses `⚠`.
- Error uses `✖`.
- Info notifications use `ℹ`.

Core components:
- Status bar uses segmented pills for active page, focused region, and env path.
- Backup workflow uses a horizontal stepper with completed, active, and future states.
- Notifications use timestamped badges.
- Command palette uses a thick bordered centered panel.
- History uses subtle alternating row backgrounds.

## Global Pages

| Key | Page | Purpose |
|---|---|---|
| `1` | Dashboard | Latest run, health summary, runtime/API status, quick actions |
| `2` | Backup | Manual backup workflow |
| `3` | Schedule | Permanent or temporary schedule changes |
| `4` | Config | Searchable `.env` editor |
| `5` | Logs | Daily log and run index viewer |
| `6` | History | Previous runs from `backup-runs.jsonl` |
| `7` | Health | Detailed health diagnostics, latest run, runtime metadata, scheduler guidance |
| `8` | Systemd | User service/timer status, actions, and rendered unit previews |

## Global Controls

| Key | Behavior |
|---|---|
| `1`-`8` | Jump to pages |
| `Tab` | Move focus forward between sidebar and content |
| `Shift+Tab` | Move focus backward between content and sidebar |
| `Esc` | Return focus to sidebar, close search/palette/prompt, or go back in table selection |
| `j` / `k` | Move down/up; scroll Dashboard, Health, and Systemd when content is focused |
| Arrow keys | Move through lists and selectors; scroll Dashboard, Health, and Systemd when content is focused |
| `Enter` | Activate selected row/action |
| `:` | Open command palette |
| `/` | Search/filter on supported pages |
| `r` | Refresh current page where supported |
| `?` | Open centered help modal |
| `q` | Quit, with confirmation if config has unsaved changes |
| `Ctrl+S` | Save config or permanent backup scope where supported |

Esc handling order:
1. Close floating modal or prompt: Help Modal, Command Palette, confirmation panel, active editor, or search input.
2. Exit sub-view context: table list returns to database list in Backup Scope.
3. Reset focus to Sidebar when already on a root page view.

## Context Footer

The status bar shows keybindings for the currently focused panel.

Examples:
- Sidebar focused: `[j/k] Navigate  [Enter] Select  [:] Commands  [?] Help`
- Scope table focused: `[Space] Toggle  [v] Range  [Ctrl+A] Select All  [Ctrl+X] Clear  [Esc] Back`
- Dashboard/Health/Systemd content focused: `[j/k] Scroll  [PgUp/PgDn] Page  [Home/End] Jump  [Tab] Focus`
- Config focused: `[/] Search  [Enter] Edit  [Ctrl+S] Save  [v] Reveal`

## Notifications

Every command or meaningful state change should produce a visible notification.

Notification levels:
- `info`: neutral action, page opened, command started.
- `ok`: successful save, refresh, command, or completed action.
- `error`: failed load, invalid value, failed command, failed backup.

Representation:

```text
+-----------------------------------------------------------------------------+
| Notifications                                                                |
| 12:29:45 │ [ ℹ INF ] opened Config                                           |
| 12:35:12 │ [ ✔ OK ] config saved                                             |
| 12:36:01 │ [ ✖ ERR ] log load failed: ...                                    |
+-----------------------------------------------------------------------------+
```

Only the latest three notifications are shown in the popup.

## Command Palette

Open with `:`.

```text
+-----------------------------------------------------------------------------+
| Command Palette                                                              |
| > type a command                                                             |
|                                                                             |
| Go to dashboard                                                              |
| Start manual backup workflow                                                 |
| Open schedule manager                                                        |
| Open config editor                                                           |
| Open logs                                                                    |
+-----------------------------------------------------------------------------+
```

Commands:
- Go to dashboard
- Start manual backup workflow
- Open schedule manager
- Open config editor
- Open logs
- Open run history
- Refresh health
- Open systemd
- Save config
- Toggle API auth
- Rotate API bearer token
- Load daily log
- Load run index
- Quit

Controls:
- Type to fuzzy-filter by label or command id.
- Page-specific commands appear first, such as `Save schedule to .env` on the Schedule page.
- `j` / `k` moves selection.
- `Enter` runs selected command. If only one command matches, Enter runs it immediately.
- `Esc` closes.

## Help Modal

Open with `?`.

The help modal is a centered overlay containing global keybindings and the current page's local keybindings. Close it with `Esc`, `?`, `q`, or `Enter`.

When Help, Command Palette, or Confirm is active:
- Sidebar and Content borders are forced to muted inactive styling.
- Normal page input/update handling is frozen.
- Input is trapped inside the active modal until it is closed.

## Confirmation Panel

Used before actions such as quitting with unsaved changes or running systemd actions.

```text
+-----------------------------------------------------------------------------+
| Confirm                                                                      |
| Restart the backup service?                                                  |
|                                                                             |
| Cancel  Confirm                                                              |
| Default is Cancel. Use left/right then Enter, or type y to confirm.          |
+-----------------------------------------------------------------------------+
```

## Dashboard

Purpose: high-level status and quick entry points.

```text
Dashboard

+ Latest Run -----------------------------------------------------------------+
| success at 2026-05-29 00:43 (4/4 dbs)                                       |
+----------------------------------------------------------------------------+
+ Logical --------------------------------------------------------------------+
| ok - logical backup prerequisites look valid                                |
+----------------------------------------------------------------------------+
+ Physical -------------------------------------------------------------------+
| ok - physical backup prerequisites look valid                               |
+----------------------------------------------------------------------------+
+ Logs -----------------------------------------------------------------------+
| daily: /mnt/volume_1/backup/mysql_backup/logs/YYYY-MM-DD.log                |
+----------------------------------------------------------------------------+

Quick Actions
b / 2  Manual backup workflow
3      Schedule manager
4      Search and edit config
5      View logs
7      Refresh health diagnostics
:      Command palette
```

Dashboard is a scrollable content page. When the content pane has focus, `j/k`, arrow keys, `PgUp/PgDn`, and `Home/End` move the viewport instead of changing the selected page.

## Backup Page

Purpose: manual backup workflow with mode, upload behavior, database/table scope, preview, and live run logs.

Step bar:

```text
Manual Backup

✔ Mode ── ✔ Upload ── [ ◉ Scope ] ── ○ Preview ── ○ Run
```

### Step 1: Mode

Options:
- logical + physical
- logical only
- physical only

Help panel explains:
- logical backup: readable `.sql.gz` dump files for restore/import.
- physical backup: full MySQL data-file backup using xtrabackup and direct S3 streaming.

### Step 2: Upload

Options:
- normal upload behavior
- local only - disables uploads

Notes:
- `local only` disables logical S3 upload for that manual run.
- Physical backup is skipped in `local only` mode because physical backup currently supports direct S3 streaming only.

### Step 3: Scope

Scope applies to logical backups only.

Database list representation:

```text
Backup Scope
Default: all granted DBs/tables. Space toggle Ctrl+A select all Ctrl+X clear v range Ctrl+S save

Databases
No database selected means all databases. Selecting a database lets you include all its tables or choose specific tables.
Ctrl+A selects all DBs. v starts/selects a range. Enter opens tables. Press n/right to preview.

[ ] bk_pf_TickleRight_9210          all tables
[x] pf_TickleRight_9210             42 selected tables
[ ] pf_central                      all tables
[ ] pf_messenger                    all tables

Current Scope
Scope: selected databases/tables
- pf_TickleRight_9210: 42 selected tables (...)
```

Table list representation:

```text
Tables in pf_TickleRight_9210 (42 selected)
Selecting tables means only those tables are dumped for this database. No table selection means all tables.
/ search Space toggle Ctrl+A select shown v range Ctrl+X clear Enter continue Esc back

Showing 1-60 of 548 tables. / search. PageUp/PageDown jumps. Home/End jumps to first/last.
[x] action_request              [ ] cities                    [x] contact_gallery
[x] active_member_log           [ ] class_forward_log         [x] contact_group
[ ] active_members_age_range    [ ] class_room                [x] contact_history
```

Scope controls:
- `Space`: toggle current database/table.
- `Enter` on database: open table list.
- `Enter` in table list: continue to preview.
- `n`: continue to preview.
- Right arrow or `l`: continue to preview from the database list; move right one visual column in the table list.
- `Esc`, `b`, `Backspace`: return from table list to database list.
- `/`: search table names.
- `Ctrl+W`: clear table search.
- `Ctrl+A` or `a`: select all listed databases, or all currently shown table matches.
- `Ctrl+X` or `c`: clear selected range, or clear current database/table checks.
- `v`: start a visual range; press `v` again at another row to select the range.
- `PageUp` / `PageDown`: jump in long table lists.
- `Home` / `End`: jump first/last.
- `Ctrl+S`: save selected scope permanently to `.env`.

Multi-column table navigation:
- `j` / Down moves vertically within the same visual column.
- `k` / Up moves vertically within the same visual column.
- `l` / Right moves to the neighboring visual column on the right.
- `h` / Left moves to the neighboring visual column on the left.
- `Esc`, `b`, or `Backspace` goes back to the database list.

Scope rules:
- No database selected means all granted non-system databases.
- Selected database with no selected tables means all tables in that database.
- Selected database with selected tables means only those tables are dumped.
- Permanent scope save writes `BACKUP_LOGICAL_DATABASES` and `BACKUP_LOGICAL_TABLES`.

### Step 4: Preview

Shows:
- force-run toggle.
- selected mode and upload behavior.
- logical/physical schedule effect.
- selected database/table scope.
- warnings.
- `Start backup` action.

### Step 5: Run

Shows:
- spinner.
- progress summary.
- progress bar.
- live backup log viewport.

Done state shows:
- status.
- run ID.
- run folder.
- log file.
- database success count.
- prompt to start another manual workflow.

## Schedule Page

Purpose: edit schedule and upload toggles permanently or temporarily.

```text
Schedule Manager
enter edit/toggle a apply c clear temporary override s save/apply

Mode: permanent .env change

Apply Mode                  permanent .env
Temporary Duration          not used for permanent changes
Logical Backup Times        daily@02:00,06:00,12:00,16:00,18:00
Physical/S3 Backup Time     weekly@sun,02:00
Logical S3 Upload           true
Physical S3 Stream          true
Retention Days              5
Apply Changes               write to .env
Clear Temporary Override    none active

Hint
...
```

Rows:
- Apply Mode
- Temporary Duration
- Logical Backup Times
- Physical/S3 Backup Time
- Logical S3 Upload
- Physical S3 Stream
- Retention Days
- Apply Changes
- Clear Temporary Override

Controls:
- `j` / `k`: move rows.
- `Enter`: edit/toggle selected row.
- `a` or `s`: apply changes.
- `c`: clear active temporary override.
- `Esc`: cancel picker/editor.

Apply modes:
- `permanent .env`: writes directly to `.env`.
- `temporary override`: writes `<BACKUP_LOG_DIR>/backup-temporary-overrides.json` and leaves `.env` unchanged.

Temporary durations:
- until end of today
- for 24 hours
- for 7 days
- for 30 days

Logical schedule presets:
- `always`
- `disabled`
- `daily@02:00`
- `daily@00:00,06:00,12:00,18:00`
- `interval@6h`
- `interval@12h`
- `interval@24h`
- custom value

Physical schedule presets:
- `always`
- `disabled`
- `weekly@sun,02:00`
- `daily@03:00`
- `interval@24h`
- `interval@168h`
- custom value

Retention presets:
- 3 days
- 5 days
- 7 days
- 14 days
- 30 days
- custom value

## Config Page

Purpose: searchable `.env` editor with masked secrets and inline validation.

```text
Config Editor
Search: none   j/k scroll   pgup/pgdown   g/G top/bottom   s save   / search   v reveal

Database
DB_USER                           backup_user
DB_PASS                           ************
DB_HOST                           localhost

Logical
BACKUP_LOGICAL_ENABLED            true
BACKUP_LOGICAL_SCHEDULE           daily@02:00

Hint
BACKUP_LOGICAL_SCHEDULE: When logical .sql.gz backups run...
```

Controls:
- `/`: search by group, key, label, or value.
- `Enter`: edit selected setting.
- `Enter` while editing: apply draft value.
- `Esc`: cancel editing/search.
- `j` / `k`, arrows: move selection.
- `PageUp` / `PageDown`: scroll.
- `g` / `G`: top/bottom.
- `v`: reveal or mask secret values.
- `s` or `Ctrl+S`: save `.env`.
- `Ctrl+R`: reload `.env` from disk and discard draft changes.

Field groups:
- Database: `DB_USER`, `DB_PASS`, `DB_HOST`, `DB_PORT`
- Paths: `BACKUP_DIR`, `BACKUP_LOG_DIR`, `BACKUP_LOCK_FILE`, `MYSQL_BIN`, `MYSQLDUMP_BIN`
- Logical: `BACKUP_LOGICAL_ENABLED`, `BACKUP_LOGICAL_SCHEDULE`, `BACKUP_LOGICAL_DATABASES`, `BACKUP_LOGICAL_TABLES`, `BACKUP_LOGICAL_TIMEOUT_PER_DB`, `BACKUP_LOGICAL_S3_UPLOAD_ENABLED`
- Physical: `BACKUP_PHYSICAL_ENABLED`, `BACKUP_PHYSICAL_SCHEDULE`, `BACKUP_PHYSICAL_TIMEOUT`, `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED`, xtrabackup/xbcloud settings
- S3: `BACKUP_S3_BUCKET`, `BACKUP_S3_REGION`, `BACKUP_S3_LOGICAL_PREFIX`, `BACKUP_S3_PHYSICAL_PREFIX`, `BACKUP_S3_KEY_ID`, `BACKUP_S3_KEY_SECRET`
- Upload: `BACKUP_S3_UPLOAD_MODE`, `BACKUP_S3_UPLOAD_URL`, `BACKUP_S3_UPLOAD_TIMEOUT`, `BACKUP_S3_PHP_BIN`, `BACKUP_S3_UPLOAD_SCRIPT`
- Tuning: retry, discovery, preflight, retention (daily/weekly/monthly), cleanup settings

Secret fields:
- `DB_PASS`
- `BACKUP_XTRABACKUP_PASS`
- `BACKUP_S3_KEY_ID`
- `BACKUP_S3_KEY_SECRET`
- `BACKUP_ENCRYPTION_KEY`

## Logs Page

Purpose: inspect daily aggregate logs or the structured run index.

```text
Logs: daily   follow=true   filter=none
d daily   i run index   / filter   f follow   g/G top/bottom

2026/05/29 00:43:16 logical backup: skipped by schedule=...
2026/05/29 00:43:16 physical backup: skipped by schedule=...
```

Controls:
- `d`: load current daily log.
- `i`: load run index.
- `/`: filter visible lines.
- `f`: toggle follow mode.
- `g` / `G`: top/bottom.
- `r`: reload.

Missing daily log behavior:
- Missing daily log is shown as an informational message, not a fatal error.
- The page explains the expected path and suggests running a backup or waiting for the scheduled service.

Follow plus filter behavior:
- When `follow=true` and a filter is active, the visible viewport only contains lines that include the filter substring.
- Incoming or newly loaded lines that do not match the filter are excluded from the active viewport so the view does not jump because of invisible lines.

## History Page

Purpose: show latest backup runs from `backup-runs.jsonl`.

```text
Run History
r refresh

time                status    dbs        duration   run id
2026-05-29 00:43    success   4/4        3m50s      2026-05-29_00-43-16
```

Controls:
- `r`: refresh run history.

Displayed fields:
- time.
- status.
- succeeded/total databases.
- duration.
- run ID.

## Health Page

Purpose: detailed config/log paths, prerequisite checks, and latest recorded run.

```text
Health Diagnostics
r refresh

Config: .env
Daily Log: /mnt/volume_1/backup/mysql_backup/logs/YYYY-MM-DD.log
Run Index: /mnt/volume_1/backup/mysql_backup/logs/backup-runs.jsonl

Logical:  ok - logical backup prerequisites look valid
Physical: ok - physical backup prerequisites look valid

Latest Run
Status: success
Time: 2026-05-29T00:43:16+05:30
Run ID: 2026-05-29_00-43-16
Run Folder: ...
Log File: ...
Duration: ...
Databases: total=4 success=4 failed=0
```

Controls:
- `r`: refresh health report.

Health is a scrollable content page. When the content pane has focus, `j/k`, arrow keys, `PgUp/PgDn`, and `Home/End` move the viewport through the diagnostics report.

## Systemd Page

Purpose: inspect and run user systemd service/timer actions.

```text
Systemd
enter action   r refresh   selected action explains command and effect

Service: load=loaded active=inactive sub=dead enabled=enabled
Timer:   load=loaded active=active sub=waiting enabled=enabled

Actions
Refresh status
Daemon reload
Enable timer
Disable timer
Start timer
Stop timer
Restart service
Start service
Stop service

Selected Action
Runs the backup service once immediately, outside timer timing.
Command: systemctl --user start sdl-db-backup.service
```

Controls:
- `j` / `k`: move action selection when the viewport cannot scroll.
- `Enter`: confirm and run selected action.
- `r`: refresh service/timer status.

Systemd is a scrollable content page. When the content pane has focus, `j/k`, arrow keys, `PgUp/PgDn`, and `Home/End` first try to scroll the viewport that contains the unit preview and action details. If the viewport is already at the top or bottom, the keys fall back to the action list.

The Systemd page uses a bounded viewport for the unit preview and action details. When the content pane is focused, `j/k`, arrow keys, `PgUp/PgDn`, and `Home/End` scroll the viewport.

Actions:
- Refresh status
- Daemon reload
- Enable timer
- Disable timer
- Start timer
- Stop timer
- Restart service
- Start service
- Stop service

## Data Flow

Startup:
1. Load `.env`.
2. Build editable config field list.
3. Load health report.
4. Load temporary schedule overrides.
5. Load history.
6. Load current daily log if present.

Config save:
1. User edits draft values.
2. Validation runs per field.
3. Save command writes managed keys to `.env`.
4. Config reloads into active state.
5. Notification reports success or failure.

Manual backup:
1. User selects mode.
2. User selects upload behavior.
3. User selects logical database/table scope.
4. Preview is generated using in-memory overrides.
5. Backup runs in a subprocess-like async command.
6. Live logs stream into the Backup page.
7. Final run record updates history and health.

Schedule change:
1. User edits schedule rows.
2. Permanent mode writes `.env`.
3. Temporary mode writes override JSON in the log directory.
4. Effective config loading applies active overrides until expiry.

Logical S3 upload:
1. `BACKUP_LOGICAL_S3_UPLOAD_ENABLED` controls whether logical upload runs.
2. `BACKUP_S3_UPLOAD_MODE=direct` uploads from Go using env S3 secrets.
3. `BACKUP_S3_UPLOAD_MODE=php` runs `BACKUP_S3_UPLOAD_SCRIPT`.
4. `BACKUP_S3_UPLOAD_MODE=http` posts to `BACKUP_S3_UPLOAD_URL`.
5. `BACKUP_S3_UPLOAD_MODE=auto` tries direct upload first, then PHP/HTTP fallback.

Physical S3 upload:
1. Physical backup streams `xtrabackup` output to S3 with `xbcloud`.
2. It uses `BACKUP_S3_KEY_ID`, `BACKUP_S3_KEY_SECRET`, `BACKUP_S3_BUCKET`, and `BACKUP_S3_REGION`.
3. It writes under `BACKUP_S3_PHYSICAL_PREFIX`.

## Responsive Behavior

The TUI recalculates sizes on terminal resize:
- Sidebar uses a fixed width.
- Content panel uses the remaining width after subtracting the sidebar and layout borders.
- Status, notifications, command palette, and confirm panel reserve vertical space.
- Scrollable areas resize to the remaining main panel height and are clamped at zero when the terminal becomes too small.
- Dashboard, Health, and Systemd render through viewports so their text cannot overflow the shell frame.
- Table selection uses 3, 2, or 1 columns depending on the available width.
- History row count is capped by available panel height.
- Config selection stays visible by adjusting viewport offset.

Main viewport composition:

```text
┌──────────────────────────────────────────────────────────────┐
│ Active Window                                                │
│ ┌───────────────┬──────────────────────────────────────────┐ │
│ │ Sidebar       │ Active Content Panel                     │ │
│ │ Rounded/Dim   │ Rounded Accent Border when focused       │ │
│ └───────────────┴──────────────────────────────────────────┘ │
│ ┌──────────────────────────────────────────────────────────┐ │
│ │ Status Bar Pills and Context Keybinding Footer           │ │
│ └──────────────────────────────────────────────────────────┘ │
│ ┌──────────────────────────────────────────────────────────┐ │
│ │ Notification Drawer, max 3 recent lines                  │ │
│ └──────────────────────────────────────────────────────────┘ │
│ ┌──────────────────────────────────────────────────────────┐ │
│ │ Optional modal layer: Command Palette, Help, Confirm      │ │
│ └──────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

## Files

- TUI orchestration: `internal/tui/app.go`
- TUI views and panels: `internal/tui/views.go`
- TUI keyboard routing: `internal/tui/input.go`
- TUI async commands: `internal/tui/cmds.go`
- TUI helpers and shared logic: `internal/tui/helpers.go`
- TUI entrypoint: `cmd/sdl-db-backup-tui/main.go`
- Backup engine: `internal/backupapp/app.go`
- User systemd unit: `sdl-db-backup.service`
- User systemd timer: `sdl-db-backup.timer`
- Environment config: `.env`
- Template config: `.env.example`
