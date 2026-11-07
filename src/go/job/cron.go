package job

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser accepts the standard five fields (minute hour day month weekday).
//
// Descriptors like "@daily" are deliberately not accepted: they make a schedule
// ambiguous to read back, and this project's config is meant to be obvious at a
// glance. A five-field expression is unambiguous.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// validateCron reports whether expr is a usable standard cron expression.
func validateCron(expr string) error {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return err
	}
	// A schedule that never fires is almost always a mistake, so prove it can
	// produce at least one future time.
	if sched.Next(time.Now()).IsZero() {
		return fmt.Errorf("the expression never fires")
	}
	return nil
}

// CronNext returns the next fire time after now for a validated expression.
func CronNext(expr string, now time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(now), nil
}
