package access

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Limit is a gateway key's budget for a window: a calendar one in local
// time (#585), a day from midnight, a week from Monday's midnight, a
// month from the 1st's; or a cycle of Days days (#1509, period "days"),
// the first from when the limit was set, each next one Days×24h after
// the last. Tokens counts each call's uncached input, output (reasoning
// included) and cache writes, and its cache reads too when CacheReads is
// set; Cost is an estimate in US dollars at the prices magpie shows on
// the Usage page, not a vendor's bill. A cap of 0 is no cap; a limit with
// neither is no limit.
type Limit struct {
	Period string `json:"period"`
	// Days is a "days" limit's cycle, 1 to MaxDays; 0 for the others.
	Days       int     `json:"days,omitempty"`
	Tokens     int64   `json:"tokens,omitempty"`
	Cost       float64 `json:"cost,omitempty"`
	CacheReads bool    `json:"cacheReads,omitempty"`
	// Since is magpie's, never the caller's (Update sets it): for "days",
	// when the first cycle began, the limit set or last reset; for a
	// calendar period, when it was last reset, so a call before it in
	// the same window doesn't count. Zero for a calendar limit never
	// reset, as every limit from before #1509 is.
	Since time.Time `json:"since,omitzero"`
}

// Periods are the windows a limit can be set for.
var Periods = []string{"day", "week", "month", "days"}

// MaxDays is the longest "days" cycle, ten years.
const MaxDays = 3650

// Limited: l caps something.
func (l *Limit) Limited() bool { return l != nil && (l.Tokens > 0 || l.Cost > 0) }

// Valid is l as it is kept: nil for no limit, an error for one that
// can't be. Since is left as it came; Update decides it.
func (l *Limit) Valid() (*Limit, error) {
	if l == nil {
		return nil, nil
	}
	if l.Tokens < 0 || l.Cost < 0 || math.IsNaN(l.Cost) || math.IsInf(l.Cost, 0) {
		return nil, errors.New("A limit can't be negative")
	}
	if !l.Limited() {
		return nil, nil
	}
	v := *l
	if v.Period == "" {
		v.Period = "day"
	}
	switch v.Period {
	case "day", "week", "month":
		v.Days = 0
	case "days":
		if v.Days < 1 || v.Days > MaxDays {
			return nil, fmt.Errorf("A limit's cycle is a whole number of days from 1 to %d", MaxDays)
		}
	default:
		return nil, errors.New("A limit's period is day, week, month or every N days")
	}
	v.Cost = math.Round(v.Cost*1e4) / 1e4
	return &v, nil
}

// Per is the window as a sentence ends it: "day", "week", "month" or
// "10-day cycle".
func (l *Limit) Per() string {
	if l.Period == "days" {
		return fmt.Sprintf("%d-day cycle", l.Days)
	}
	return l.Period
}

// sinceOf is the Since a limit set as next keeps, with prev the key's
// limit before: its own while the period and cycle stay the same (a new
// cap doesn't restart the window), else a new cycle from now for "days"
// and none for a calendar period.
func sinceOf(prev, next *Limit, now time.Time) time.Time {
	if prev != nil && prev.Period == next.Period && prev.Days == next.Days && (next.Period != "days" || !prev.Since.IsZero()) {
		return prev.Since
	}
	if next.Period == "days" {
		return now
	}
	return time.Time{}
}

// epoch is where a "days" cycle written by hand without a Since runs from.
var epoch = time.Unix(0, 0)

// Window is the window l counts in at now, in now's location: when it
// began and when the next begins. A "days" cycle is counted in absolute
// time, 24h a day whatever the clocks do, so a restart or a change of
// time zone or summer time keeps it; one with no Since runs from the Unix
// epoch. A calendar window starts at Since instead when the key was reset
// in it.
func (l *Limit) Window(now time.Time) (start, reset time.Time) {
	loc := now.Location()
	if l.Period == "days" {
		n := time.Duration(min(max(l.Days, 1), MaxDays)) * 24 * time.Hour
		anchor := l.Since
		if anchor.Before(epoch) {
			anchor = epoch
		}
		if now.Before(anchor) { // the clock was set back past the start
			return anchor.In(loc), anchor.Add(n).In(loc)
		}
		start = anchor.Add(now.Sub(anchor) / n * n)
		return start.In(loc), start.Add(n).In(loc)
	}
	start, reset = Window(l.Period, now)
	if l.Since.After(start) && l.Since.Before(reset) {
		start = l.Since.In(loc)
	}
	return start, reset
}

// Window is the calendar window of period that now is in, in now's
// location: its start and when the next begins.
func Window(period string, now time.Time) (start, reset time.Time) {
	y, m, d := now.Date()
	loc := now.Location()
	switch period {
	case "week":
		back := (int(now.Weekday()) + 6) % 7 // days since Monday
		start = time.Date(y, m, d-back, 0, 0, 0, 0, loc)
		return start, time.Date(y, m, d-back+7, 0, 0, 0, 0, loc)
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, loc), time.Date(y, m+1, 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(y, m, d, 0, 0, 0, 0, loc), time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	}
}
