package utils

import (
	"fmt"
	"math"
	"time"
)

func DaysPassed(t time.Time) int {
	diff := time.Now().UTC().Sub(t)
	days := int(diff.Hours() / 24)
	return days
}

// source: https://gist.github.com/harshavardhana/327e0577c4fed9211f65
func HumanizeDuration(duration time.Duration) string {
	if duration.Seconds() < 60.0 {
		return fmt.Sprintf("%d second(s)", int64(duration.Seconds()))
	}

	if duration.Minutes() < 60.0 {
		remainingSeconds := math.Mod(duration.Seconds(), 60)
		return fmt.Sprintf("%d minute(s) and %d second(s)", int64(duration.Minutes()), int64(remainingSeconds))
	}

	if duration.Hours() < 24.0 {
		remainingSeconds := math.Mod(duration.Seconds(), 60)
		remainingMinutes := math.Mod(duration.Minutes(), 60)
		return fmt.Sprintf("%d hour(s), %d minute(s) and %d second(s)", int64(duration.Hours()), int64(remainingMinutes), int64(remainingSeconds))
	}

	remainingSeconds := math.Mod(duration.Seconds(), 60)
	remainingMinutes := math.Mod(duration.Minutes(), 60)
	remainingHours := math.Mod(duration.Hours(), 24)
	return fmt.Sprintf("%d day(s), %d hour(s), %d minute(s) and %d second(s)", int64(duration.Hours()/24), int64(remainingHours), int64(remainingMinutes), int64(remainingSeconds))
}
