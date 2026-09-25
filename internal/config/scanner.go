package config

import (
	"fmt"

	"github.com/adhocore/gronx"
)

type Scanner struct {
	Interval string `yaml:"interval"`
	ImageAge int    `yaml:"image_age"`
	// hours to rely on the last registry check of an image before querying the registry again
	CheckInterval int  `yaml:"check_interval"`
	RunOnStart    bool `yaml:"run_on_start"`
	ScanAll       bool `yaml:"scan_all"`
	ScanStopped   bool `yaml:"scan_stopped"`
	FailOnError   bool `yaml:"fail_on_error"`
}

// Checks that the interval is a cron expression that fires at least once more.
// A valid one may not, e.g. `0 0 30 2 *` (February 30th) or one with a past year.
func (s Scanner) ValidateInterval() error {
	if !gronx.New().IsValid(s.Interval) {
		return fmt.Errorf("invalid cron format for scanner.interval: %q", s.Interval)
	}
	if _, err := gronx.NextTick(s.Interval, false); err != nil {
		return fmt.Errorf("scanner.interval %q never fires: %w", s.Interval, err)
	}
	return nil
}
