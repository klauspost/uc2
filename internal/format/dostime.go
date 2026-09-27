package format

import "time"

var (
	minDOS = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	maxDOS = time.Date(2107, 12, 31, 23, 59, 58, 0, time.UTC)
)

// DOSTime encodes the wall clock of t (in its own location) as DOS date and time,
// clamped to 1980-01-01..2107-12-31 23:59:58 with 2 second resolution.
func DOSTime(t time.Time) (date, tm uint16) {
	w := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	if w.Before(minDOS) {
		w = minDOS
	} else if w.After(maxDOS) {
		w = maxDOS
	}
	date = uint16(w.Year()-1980)<<9 | uint16(w.Month())<<5 | uint16(w.Day())
	tm = uint16(w.Hour())<<11 | uint16(w.Minute())<<5 | uint16(w.Second()/2)
	return date, tm
}

// FromDOS decodes a DOS date and time in loc. Out of range fields are normalized.
func FromDOS(date, tm uint16, loc *time.Location) time.Time {
	return time.Date(int(date>>9)+1980, time.Month(date>>5&15), int(date&31),
		int(tm>>11), int(tm>>5&63), int(tm&31)*2, 0, loc)
}
