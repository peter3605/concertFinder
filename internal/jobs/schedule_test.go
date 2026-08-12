package jobs

import (
	"testing"
	"time"
)

func TestDailyAtNextIsTodayWhenStillAhead(t *testing.T) {
	s := DailyAt(7, 0)
	got := s.Next(time.Date(2026, 8, 11, 3, 30, 0, 0, time.UTC))
	want := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDailyAtNextRollsToTomorrowOncePast(t *testing.T) {
	s := DailyAt(7, 0)
	got := s.Next(time.Date(2026, 8, 11, 7, 0, 1, 0, time.UTC))
	want := time.Date(2026, 8, 12, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Strictly after: a scheduler asking at exactly the target instant must get
// tomorrow, or the job it just ran would be scheduled again immediately.
func TestDailyAtExactlyOnTheHourRollsForward(t *testing.T) {
	s := DailyAt(7, 0)
	at := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	if got := s.Next(at); !got.After(at) {
		t.Errorf("Next(%v) = %v, must be strictly after", at, got)
	}
}

func TestDailyAtCrossesMonthAndYearBoundaries(t *testing.T) {
	s := DailyAt(9, 30)
	got := s.Next(time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC))
	want := time.Date(2027, 1, 1, 9, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The whole point of the type: the answer depends only on the clock, not on
// when the process started. A restart at any hour must land on the same
// instant that an already-running scheduler would have picked.
func TestDailyAtIsIndependentOfProcessStart(t *testing.T) {
	s := DailyAt(7, 0)
	day := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	for h := 0; h < 7; h++ {
		if got := s.Next(day.Add(time.Duration(h) * time.Hour)); !got.Equal(want) {
			t.Errorf("restart at %02d:00 scheduled %v, want %v", h, got, want)
		}
	}
}

// Callers hand it whatever zone time.Now() carries; the schedule is UTC.
func TestDailyAtNormalizesNonUTCInput(t *testing.T) {
	s := DailyAt(7, 0)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 2026-08-11 01:00 EDT == 05:00 UTC, so the next 07:00 UTC is same-day.
	got := s.Next(time.Date(2026, 8, 11, 1, 0, 0, 0, ny))
	want := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
