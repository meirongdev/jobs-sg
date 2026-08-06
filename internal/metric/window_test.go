package metric

import (
	"testing"
	"time"
)

func TestDayBoundsAreSGTMidnights(t *testing.T) {
	// 2026-08-03 09:00 SGT -> [2026-08-02T16:00Z, 2026-08-03T16:00Z)
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, SGT)
	w := Day(now)
	if !w.Start.Equal(time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 2026-08-02T16:00:00Z", w.Start)
	}
	if !w.End.Equal(time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2026-08-03T16:00:00Z", w.End)
	}
}

func TestISOWeekOfSnapsToMonday(t *testing.T) {
	// Thursday 2026-08-06 SGT belongs to the week starting Monday 2026-08-03
	w := ISOWeekOf(time.Date(2026, 8, 6, 23, 0, 0, 0, SGT))
	if got := w.WeekLabel(); got != "2026-W32" {
		t.Errorf("label = %s, want 2026-W32", got)
	}
	if !w.Start.Equal(time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want Monday 2026-08-03 00:00 SGT", w.Start)
	}
}

func TestLastCompletedWeekExcludesTheCurrentWeek(t *testing.T) {
	// Monday 2026-08-10 is in W33; the last completed week is W32.
	w := LastCompletedWeek(time.Date(2026, 8, 10, 0, 0, 0, 0, SGT))
	if got := w.WeekLabel(); got != "2026-W32" {
		t.Errorf("label = %s, want 2026-W32", got)
	}
	// Sunday 2026-08-09 23:59 SGT is still inside W32, so W32 is NOT complete.
	w = LastCompletedWeek(time.Date(2026, 8, 9, 23, 59, 0, 0, SGT))
	if got := w.WeekLabel(); got != "2026-W31" {
		t.Errorf("label = %s, want 2026-W31", got)
	}
}

func TestPrevWeeksReturnsOldestFirst(t *testing.T) {
	w := LastCompletedWeek(time.Date(2026, 8, 10, 0, 0, 0, 0, SGT)) // W32
	prev := PrevWeeks(w, 4)
	want := []string{"2026-W28", "2026-W29", "2026-W30", "2026-W31"}
	if len(prev) != len(want) {
		t.Fatalf("got %d weeks, want %d", len(prev), len(want))
	}
	for i, lbl := range want {
		if got := prev[i].WeekLabel(); got != lbl {
			t.Errorf("prev[%d] = %s, want %s", i, got, lbl)
		}
	}
}

func TestRollingEndsAtTodaysSGTDayEnd(t *testing.T) {
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, SGT)
	w := Rolling(now, 90)
	if !w.End.Equal(Day(now).End) {
		t.Errorf("end = %v, want today's SGT day end %v", w.End, Day(now).End)
	}
	if got := w.End.Sub(w.Start).Hours(); got != 90*24 {
		t.Errorf("span = %vh, want %vh", got, 90*24)
	}
}
