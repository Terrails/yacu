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

func TestLoadConfigReadsRegistryPasswords(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "yacu.yaml")
	configFile := []byte("registries:\n  - domain: ghcr.io\n    username: owner\n    password_env: YACU_TEST_GHCR_TOKEN\n")
	if err := os.WriteFile(configPath, configFile, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YACU_TEST_GHCR_TOKEN", "token")

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Registries[0].Password != "token" {
		t.Fatalf("password was not read from the environment: %+v", config.Registries[0])
	}
}

func TestLoadConfigSchedulingOptions(t *testing.T) {
	config, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Scanner.CheckInterval != 24 || config.Scanner.RunOnStart {
		t.Fatalf("unexpected defaults: %+v", config.Scanner)
	}

	t.Setenv("YACU_SCANNER_CHECK_INTERVAL", "6")
	t.Setenv("YACU_SCANNER_RUN_ON_START", "true")
	if config, err = LoadConfig(filepath.Join(t.TempDir(), "missing.yaml")); err != nil {
		t.Fatal(err)
	}
	if config.Scanner.CheckInterval != 6 || !config.Scanner.RunOnStart {
		t.Fatalf("environment values did not apply: %+v", config.Scanner)
	}

	t.Setenv("YACU_SCANNER_CHECK_INTERVAL", "-1")
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected a negative check interval to be rejected")
	}
}
