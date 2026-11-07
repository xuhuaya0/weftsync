package job

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule modes (PLAN.md §10).
const (
	// ScheduleNone means the job only runs when asked.
	ScheduleNone = "none"
	// ScheduleInterval runs every Interval, optionally phase-locked to StartAt.
	ScheduleInterval = "interval"
	// ScheduleDailyTime runs once a day at a fixed local time.
	ScheduleDailyTime = "daily_time"
	// ScheduleCron takes a standard five-field cron expression.
	ScheduleCron = "cron"
)

// ScheduleSpec is the [job.schedule] table.
type ScheduleSpec struct {
	Mode string `toml:"mode"`
	// Interval is a Go duration string ("1h", "30m").
	Interval string `toml:"interval"`
	// StartAt is the time of day a job first became active, "HH:MM".
	//
	// With AlignToStart it also fixes the phase: a "2h" interval with start_at
	// "08:00" fires at 08:00, 10:00, 12:00 rather than at some arbitrary offset
	// from when the daemon happened to start.
	StartAt string `toml:"start_at"`
	// AlignToStart turns StartAt into a phase anchor. FreeFileSync calls the
	// same idea "start time"; PLAN.md §10 asks for both behaviours to be
	// distinguishable.
	AlignToStart bool `toml:"align_to_start"`
	// DailyTime is "HH:MM" for ScheduleDailyTime.
	DailyTime string `toml:"daily_time"`
	// Cron is a five-field expression for ScheduleCron.
	Cron string `toml:"cron"`
}

// ResolvedSchedule is a validated schedule that can compute its next run.
type ResolvedSchedule struct {
	Mode         string
	Interval     time.Duration
	DailyTime    hourMinute
	StartAt      hourMinute
	HasStartAt   bool
	AlignToStart bool
	Cron         string
}

type hourMinute struct {
	Hour   int
	Minute int
}

func (h hourMinute) String() string {
	return fmt.Sprintf("%02d:%02d", h.Hour, h.Minute)
}

// at returns today's occurrence in loc.
func (h hourMinute) at(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), h.Hour, h.Minute, 0, 0, now.Location())
}

// EffectiveSchedule validates the schedule, applying defaults.
func (c *Config) EffectiveSchedule() (ResolvedSchedule, error) {
	s := c.Job.Schedule
	mode := strings.TrimSpace(s.Mode)
	if mode == "" {
		mode = ScheduleNone
	}

	out := ResolvedSchedule{
		Mode:         mode,
		AlignToStart: s.AlignToStart,
		Cron:         strings.TrimSpace(s.Cron),
	}

	if start := strings.TrimSpace(s.StartAt); start != "" {
		hm, err := parseHourMinute(start)
		if err != nil {
			return out, fmt.Errorf("job %q: schedule.start_at: %w", c.Job.Name, err)
		}
		out.StartAt = hm
		out.HasStartAt = true
	}

	switch mode {
	case ScheduleNone:
		// Nothing to validate.

	case ScheduleInterval:
		raw := strings.TrimSpace(s.Interval)
		if raw == "" {
			return out, fmt.Errorf(
				"job %q: schedule.mode = \"interval\" needs schedule.interval (e.g. \"1h\")",
				c.Job.Name)
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			return out, fmt.Errorf("job %q: schedule.interval %q: %w", c.Job.Name, raw, err)
		}
		if d < time.Minute {
			return out, fmt.Errorf(
				"job %q: schedule.interval %v is too short; the minimum is 1m "+
					"(a tighter loop cannot finish a real sync and will only burn API quota)",
				c.Job.Name, d)
		}
		out.Interval = d
		if out.AlignToStart && !out.HasStartAt {
			return out, fmt.Errorf(
				"job %q: schedule.align_to_start needs schedule.start_at (\"HH:MM\")",
				c.Job.Name)
		}

	case ScheduleDailyTime:
		raw := strings.TrimSpace(s.DailyTime)
		if raw == "" {
			return out, fmt.Errorf(
				"job %q: schedule.mode = \"daily_time\" needs schedule.daily_time (e.g. \"02:00\")",
				c.Job.Name)
		}
		hm, err := parseHourMinute(raw)
		if err != nil {
			return out, fmt.Errorf("job %q: schedule.daily_time: %w", c.Job.Name, err)
		}
		out.DailyTime = hm

	case ScheduleCron:
		if out.Cron == "" {
			return out, fmt.Errorf(
				"job %q: schedule.mode = \"cron\" needs schedule.cron (e.g. \"0 2 * * *\")",
				c.Job.Name)
		}
		if err := validateCron(out.Cron); err != nil {
			return out, fmt.Errorf("job %q: schedule.cron %q: %w", c.Job.Name, out.Cron, err)
		}

	default:
		return out, fmt.Errorf(
			"job %q: unknown schedule.mode %q (expected %q, %q, %q or %q)",
			c.Job.Name, mode, ScheduleNone, ScheduleInterval, ScheduleDailyTime, ScheduleCron)
	}

	return out, nil
}

func parseHourMinute(s string) (hourMinute, error) {
	var hm hourMinute
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return hm, fmt.Errorf("%q is not HH:MM", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return hm, fmt.Errorf("%q: hour is not a number", s)
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return hm, fmt.Errorf("%q: minute is not a number", s)
	}
	if h < 0 || h > 23 {
		return hm, fmt.Errorf("%q: hour must be 0-23", s)
	}
	if m < 0 || m > 59 {
		return hm, fmt.Errorf("%q: minute must be 0-59", s)
	}
	hm.Hour, hm.Minute = h, m
	return hm, nil
}

// NextAfter computes the next run time strictly after now.
//
// A zero result means "never" (mode none).
func (r ResolvedSchedule) NextAfter(now time.Time) time.Time {
	switch r.Mode {
	case ScheduleInterval:
		base := now
		if r.AlignToStart {
			// Phase-lock to StartAt on the current day: walk forward in whole
			// intervals until the next slot is in the future. This keeps the
			// schedule stable across restarts instead of drifting from whatever
			// moment the daemon happened to start.
			anchor := r.StartAt.at(now)
			if anchor.After(now) {
				return anchor
			}
			elapsed := now.Sub(anchor)
			steps := elapsed / r.Interval
			next := anchor.Add((steps + 1) * r.Interval)
			for !next.After(now) {
				next = next.Add(r.Interval)
			}
			return next
		}
		return base.Add(r.Interval)

	case ScheduleDailyTime:
		next := r.DailyTime.at(now)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next

	default:
		return time.Time{}
	}
}

// String renders the schedule for logs.
func (r ResolvedSchedule) String() string {
	switch r.Mode {
	case ScheduleInterval:
		if r.AlignToStart {
			return fmt.Sprintf("every %v from %s", r.Interval, r.StartAt)
		}
		return "every " + r.Interval.String()
	case ScheduleDailyTime:
		return "daily at " + r.DailyTime.String()
	case ScheduleCron:
		return "cron " + r.Cron
	default:
		return "manual"
	}
}
