package main

import (
	"fmt"
	"time"

	"heroku-console/logfeed"
)

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func toRec(r *logfeed.Record) Rec {
	return Rec{
		Date: r.Date, Time: r.Time, Level: int(r.Level), Module: r.Module,
		Lines: r.Lines, Hard: r.Hard, Warn: r.Warn, Err: r.Err, Count: r.Count,
	}
}

func toRecs(rs []*logfeed.Record) []Rec {
	out := make([]Rec, len(rs))
	for i, r := range rs {
		out[i] = toRec(r)
	}
	return out
}

func humanSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%dс", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dм", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dч %dм", int(d.Hours()), int(d.Minutes())%60)
	}
}
