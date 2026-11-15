package jobs

import (
	"time"

	"github.com/riverqueue/river"
)

// DailyAt returns a river.PeriodicSchedule firing once a day at the given UTC
// hour and minute.
//
// This is deliberately not river.PeriodicInterval(24*time.Hour). River's
// periodic scheduler keeps in-memory state only and re-anchors every job to
// process start, so an interval schedule means "24h after this process booted":
// the time of day drifts with every deploy, and — with RunOnStart false — a
// process restarting more often than the interval never fires the job at all.
// Deploys here restart the server on every push to main, so a two-deploy day
// was a day with no nightly scan, no digest, and no janitor run.
//
// Computing the next wall-clock occurrence makes restarts harmless: whenever
// the scheduler asks, it gets the next real 07:00 UTC regardless of when the
// process came up.
func DailyAt(hour, minute int) river.PeriodicSchedule {
	return dailySchedule{hour: hour, minute: minute}
}

type dailySchedule struct{ hour, minute int }

// Next returns the first occurrence strictly after current — strictly, so a
// scheduler asking exactly at the target instant gets tomorrow rather than the
// same moment it just ran.
//
// UTC throughout. A local-time daily schedule would skip an hour or run twice
// on the two DST boundaries each year, and none of these jobs care which local
// hour they land on.
func (s dailySchedule) Next(current time.Time) time.Time {
	c := current.UTC()
	next := time.Date(c.Year(), c.Month(), c.Day(), s.hour, s.minute, 0, 0, time.UTC)
	if !next.After(c) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// WeeklyAt returns a river.PeriodicSchedule firing once a week, on the given
// weekday at the given UTC hour and minute. It exists for the same reason
// DailyAt does and keeps the same guarantees: a real wall-clock occurrence,
// never river.PeriodicInterval(7*24*time.Hour). An interval re-anchors to
// process start, so with deploys restarting the server several times a week
// a weekly interval job would fire almost never.
func WeeklyAt(weekday time.Weekday, hour, minute int) river.PeriodicSchedule {
	return weeklySchedule{weekday: weekday, hour: hour, minute: minute}
}

type weeklySchedule struct {
	weekday      time.Weekday
	hour, minute int
}

// Next returns the first occurrence strictly after current, in UTC: the same
// "strictly" as dailySchedule, so a scheduler asking at the target instant
// gets next week rather than the moment it just ran.
func (s weeklySchedule) Next(current time.Time) time.Time {
	c := current.UTC()
	days := (int(s.weekday) - int(c.Weekday()) + 7) % 7
	next := time.Date(c.Year(), c.Month(), c.Day()+days, s.hour, s.minute, 0, 0, time.UTC)
	if !next.After(c) {
		next = next.AddDate(0, 0, 7)
	}
	return next
}
