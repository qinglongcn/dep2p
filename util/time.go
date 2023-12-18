package util

import "time"

// TimeFormatDeP2P is the format dep2p uses to represent time in string form.
var TimeFormatDeP2P = time.RFC3339Nano

// ParseRFC3339 parses an RFC3339Nano-formatted time stamp and
// returns the UTC time.
func ParseRFC3339(s string) (time.Time, error) {
	t, err := time.Parse(TimeFormatDeP2P, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// FormatRFC3339 returns the string representation of the
// UTC value of the given time in RFC3339Nano format.
func FormatRFC3339(t time.Time) string {
	return t.UTC().Format(TimeFormatDeP2P)
}
