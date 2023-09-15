package config

import (
	"github.com/adhocore/gronx"
)

type Scanner struct {
	Interval    string `yaml:"interval"`
	ImageAge    int    `yaml:"image_age"`
	ScanAll     bool   `yaml:"scan_all"`
	ScanStopped bool   `yaml:"scan_stopped"`
}

func (s Scanner) IsIntervalValid() bool {
	gron := gronx.New()
	return gron.IsValid(s.Interval)
}
