package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigEnvironmentOverridesFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "yacu.yaml")
	configFile := []byte("scanner:\n  image_age: 3\n  scan_all: false\n")
	if err := os.WriteFile(configPath, configFile, 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("YACU_SCANNER_IMAGE_AGE", "14")
	t.Setenv("YACU_SCANNER_SCAN_ALL", "true")

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Scanner.ImageAge != 14 || !config.Scanner.ScanAll {
		t.Fatalf("environment values did not override file: %+v", config.Scanner)
	}
}

func TestLoadConfigEnvironmentOverridesDefaultsWithoutFile(t *testing.T) {
	t.Setenv("YACU_UPDATER_STOP_TIMEOUT", "0")
	t.Setenv("YACU_UPDATER_REMOVE_IMAGES", "true")

	config, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Updater.StopTimeout != 0 || !config.Updater.RemoveImages {
		t.Fatalf("environment values did not override defaults: %+v", config.Updater)
	}
}

func TestLoadConfigRejectsInvalidEnvironmentValue(t *testing.T) {
	t.Setenv("YACU_SCANNER_IMAGE_AGE", "not-an-integer")

	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected invalid environment value to fail")
	}
}

func TestLoadConfigRejectsInvalidInterval(t *testing.T) {
	t.Setenv("YACU_SCANNER_INTERVAL", "not-a-cron-interval")

	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected invalid interval to fail")
	}
}

func TestLoadConfigRejectsIntervalThatNeverFires(t *testing.T) {
	// valid cron expressions without a next run time
	for _, interval := range []string{"0 0 30 2 *", "0 0 1 1 * 2025"} {
		t.Run(interval, func(t *testing.T) {
			t.Setenv("YACU_SCANNER_INTERVAL", interval)

			_, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
			if err == nil || !strings.Contains(err.Error(), "never fires") {
				t.Fatalf("expected the interval to be rejected, got %v", err)
			}
		})
	}
}
