package backupapp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func parseScheduleTimestamp(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func loadScheduleState(path string) scheduleState {
	var state scheduleState
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("warning: could not parse schedule state %s: %v", path, err)
		return scheduleState{}
	}
	return state
}

func saveScheduleState(path string, state scheduleState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o640)
}

func parseClock(raw string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return hour, minute, nil
}

func parseWeekday(raw string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return time.Sunday, fmt.Errorf("invalid weekday %q", raw)
	}
}

func parseDailyTimes(raw string) ([]time.Duration, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return nil, fmt.Errorf("expected at least one HH:MM time")
	}

	seen := make(map[time.Duration]struct{}, len(parts))
	times := make([]time.Duration, 0, len(parts))
	for _, part := range parts {
		hour, minute, err := parseClock(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		tod := time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute
		if _, ok := seen[tod]; ok {
			continue
		}
		seen[tod] = struct{}{}
		times = append(times, tod)
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("expected at least one HH:MM time")
	}
	slices.Sort(times)
	return times, nil
}

func latestDueDailyTarget(now time.Time, times []time.Duration) (time.Time, bool) {
	if len(times) == 0 {
		return time.Time{}, false
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for i := len(times) - 1; i >= 0; i-- {
		target := today.Add(times[i])
		if !now.Before(target) {
			return target, true
		}
	}

	return time.Time{}, false
}

func parseCSVList(raw string) []string {
	items := []string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		items = append(items, part)
	}
	return items
}

func parseTableScope(raw string) map[string][]string {
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
		scope[dbName] = parseCSVList(tableSpec)
	}
	return scope
}

func formatTableScope(scope map[string][]string) string {
	if len(scope) == 0 {
		return ""
	}
	dbs := make([]string, 0, len(scope))
	for db := range scope {
		dbs = append(dbs, db)
	}
	slices.Sort(dbs)
	parts := []string{}
	for _, db := range dbs {
		tables := append([]string{}, scope[db]...)
		slices.Sort(tables)
		parts = append(parts, db+":"+strings.Join(tables, ","))
	}
	return strings.Join(parts, ";")
}

func evaluateSchedule(now time.Time, raw string, lastSuccess time.Time) (bool, string, error) {
	schedule := strings.ToLower(strings.TrimSpace(raw))
	if schedule == "" || schedule == "always" {
		return true, "schedule=always", nil
	}
	if schedule == "disabled" || schedule == "off" || schedule == "never" {
		return false, fmt.Sprintf("schedule=%s", raw), nil
	}

	if strings.HasPrefix(schedule, "interval@") {
		interval, err := time.ParseDuration(strings.TrimSpace(schedule[len("interval@"):]))
		if err != nil || interval <= 0 {
			return false, "", fmt.Errorf("invalid schedule %q: expected interval@<duration>", raw)
		}
		if lastSuccess.IsZero() {
			return true, fmt.Sprintf("schedule=%s first run", raw), nil
		}
		nextRun := lastSuccess.Add(interval)
		if !now.Before(nextRun) {
			return true, fmt.Sprintf("schedule=%s due since %s", raw, nextRun.Format(time.RFC3339)), nil
		}
		return false, fmt.Sprintf("schedule=%s next due at %s", raw, nextRun.Format(time.RFC3339)), nil
	}

	if strings.HasPrefix(schedule, "daily") {
		timeSpec := "00:00"
		if strings.Contains(schedule, "@") {
			timeSpec = strings.TrimSpace(strings.SplitN(schedule, "@", 2)[1])
		}
		times, err := parseDailyTimes(timeSpec)
		if err != nil {
			return false, "", fmt.Errorf("invalid schedule %q: %v", raw, err)
		}
		target, ok := latestDueDailyTarget(now, times)
		if !ok {
			nextTarget := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(times[0])
			return false, fmt.Sprintf("schedule=%s waiting until %s", raw, nextTarget.Format(time.RFC3339)), nil
		}
		if lastSuccess.IsZero() || lastSuccess.Before(target) {
			return true, fmt.Sprintf("schedule=%s due at %s", raw, target.Format(time.RFC3339)), nil
		}
		return false, fmt.Sprintf("schedule=%s already satisfied for %s", raw, target.Format(time.RFC3339)), nil
	}

	if strings.HasPrefix(schedule, "weekly") {
		spec := "sun,00:00"
		if strings.Contains(schedule, "@") {
			spec = strings.TrimSpace(strings.SplitN(schedule, "@", 2)[1])
		}
		parts := strings.SplitN(spec, ",", 2)
		if len(parts) != 2 {
			return false, "", fmt.Errorf("invalid schedule %q: expected weekly@<weekday>,HH:MM", raw)
		}
		weekday, err := parseWeekday(parts[0])
		if err != nil {
			return false, "", fmt.Errorf("invalid schedule %q: %v", raw, err)
		}
		hour, minute, err := parseClock(parts[1])
		if err != nil {
			return false, "", fmt.Errorf("invalid schedule %q: %v", raw, err)
		}
		daysBack := (7 + int(now.Weekday()) - int(weekday)) % 7
		targetDate := now.AddDate(0, 0, -daysBack)
		target := time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), hour, minute, 0, 0, now.Location())
		if now.Before(target) {
			nextTarget := target
			return false, fmt.Sprintf("schedule=%s waiting until %s", raw, nextTarget.Format(time.RFC3339)), nil
		}
		if lastSuccess.IsZero() || lastSuccess.Before(target) {
			return true, fmt.Sprintf("schedule=%s due at %s", raw, target.Format(time.RFC3339)), nil
		}
		return false, fmt.Sprintf("schedule=%s already satisfied for %s", raw, target.Format(time.RFC3339)), nil
	}

	return false, "", fmt.Errorf("invalid schedule %q: supported values are always, disabled, daily@HH:MM[,HH:MM...], weekly@day,HH:MM, interval@24h", raw)
}
