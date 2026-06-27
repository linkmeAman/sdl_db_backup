package backupapp

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateScheduleDailyCatchesMissedPreviousDay(t *testing.T) {
	now := time.Date(2026, 6, 25, 1, 0, 0, 0, time.UTC)
	lastSuccess := time.Date(2026, 6, 23, 2, 0, 0, 0, time.UTC)

	due, reason, err := evaluateSchedule(now, "daily@02:00", lastSuccess)
	if err != nil {
		t.Fatalf("evaluateSchedule returned error: %v", err)
	}
	if !due {
		t.Fatalf("expected missed prior-day daily schedule to be due, got due=%t reason=%q", due, reason)
	}
	if !strings.Contains(reason, "2026-06-24T02:00:00Z") {
		t.Fatalf("expected due reason to reference the missed target, got %q", reason)
	}
}

func TestEvaluateScheduleDailyReportsNextFutureSlot(t *testing.T) {
	now := time.Date(2026, 6, 25, 11, 0, 0, 0, time.UTC)
	lastSuccess := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	due, reason, err := evaluateSchedule(now, "daily@02:00,10:00,18:00", lastSuccess)
	if err != nil {
		t.Fatalf("evaluateSchedule returned error: %v", err)
	}
	if due {
		t.Fatalf("expected schedule to wait for later same-day slot, got due=%t reason=%q", due, reason)
	}
	if !strings.Contains(reason, "2026-06-25T18:00:00Z") {
		t.Fatalf("expected next due reason for later slot, got %q", reason)
	}
}

func TestEvaluateScheduleWeeklyCatchesMissedPreviousWeek(t *testing.T) {
	now := nextWeekday(time.Date(2026, 6, 25, 1, 0, 0, 0, time.UTC), time.Sunday)
	now = time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, time.UTC)
	lastSuccessDay := now.AddDate(0, 0, -14)
	lastSuccess := time.Date(lastSuccessDay.Year(), lastSuccessDay.Month(), lastSuccessDay.Day(), 2, 0, 0, 0, time.UTC)
	missedTargetDay := now.AddDate(0, 0, -7)

	due, reason, err := evaluateSchedule(now, "weekly@sun,02:00", lastSuccess)
	if err != nil {
		t.Fatalf("evaluateSchedule returned error: %v", err)
	}
	if !due {
		t.Fatalf("expected missed prior-week schedule to be due, got due=%t reason=%q", due, reason)
	}
	expectedTarget := time.Date(missedTargetDay.Year(), missedTargetDay.Month(), missedTargetDay.Day(), 2, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if !strings.Contains(reason, expectedTarget) {
		t.Fatalf("expected weekly due reason to reference missed target %s, got %q", expectedTarget, reason)
	}
}

func nextWeekday(start time.Time, weekday time.Weekday) time.Time {
	for start.Weekday() != weekday {
		start = start.AddDate(0, 0, 1)
	}
	return start
}
