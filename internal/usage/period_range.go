package usage

import (
	"strings"
	"time"
)

// A period can also be days the reader picked (#1492): from one date to
// another, both whole days and both included, in the local time zone,
// written "2026-10-01..2026-10-07". It begins at the first day's midnight and
// ends at the midnight after the last, where the presets end at now.
const rangeSep = ".."

// RangeOf is the period of the days from one date (YYYY-MM-DD) to another,
// in either order; ok is false when either is not a date.
func RangeOf(from, to string) (Period, bool) {
	a, err1 := time.Parse(time.DateOnly, from)
	b, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil {
		return "", false
	}
	if b.Before(a) {
		a, b = b, a
	}
	return Period(a.Format(time.DateOnly) + rangeSep + b.Format(time.DateOnly)), true
}

// days is the first and last day of a picked range, at midnight in loc.
func (p Period) days(loc *time.Location) (first, last time.Time, ok bool) {
	from, to, found := strings.Cut(string(p), rangeSep)
	if !found {
		return
	}
	a, err1 := time.ParseInLocation(time.DateOnly, from, loc)
	b, err2 := time.ParseInLocation(time.DateOnly, to, loc)
	if err1 != nil || err2 != nil || b.Before(a) {
		return
	}
	return a, b, true
}

// IsRange is whether p is days the reader picked rather than a preset.
func (p Period) IsRange() bool {
	_, _, ok := p.days(time.UTC)
	return ok
}

// Known is whether p is a period the usage pages answer for: a preset or a
// picked range.
func (p Period) Known() bool {
	switch p {
	case Today, Week, Month, All:
		return true
	}
	return p.IsRange()
}

// Until is when the period ends, as of now: the midnight after a picked
// range's last day, zero for a preset, which runs up to now.
func (p Period) Until(now time.Time) time.Time {
	if _, last, ok := p.days(now.Location()); ok {
		return last.AddDate(0, 0, 1)
	}
	return time.Time{}
}

// after is whether t falls past the period's end.
func after(until, t time.Time) bool { return !until.IsZero() && !t.Before(until) }

// shown is the period a summary says it is of: the preset or range asked
// for, and All for anything else.
func (p Period) shown() Period {
	if p == Today || p == Week || p == Month || p.IsRange() {
		return p
	}
	return All
}
