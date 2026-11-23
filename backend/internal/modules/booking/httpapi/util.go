package httpapi

import "time"

var zeroTime time.Time

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }
