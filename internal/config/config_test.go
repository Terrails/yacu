package config

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
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

func TestLoadConfigFileOverridesDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "yacu.yaml")
	configFile := []byte(`database:
  path: /data/yacu.db
logging:
  console:
    level: warn
scanner:
  interval: "@daily"
updater:
  stop_timeout: 60
  remove_images: true
`)
	if err := os.WriteFile(configPath, configFile, 0600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.Path != "/data/yacu.db" || config.Logging.Console.Level != zerolog.WarnLevel ||
		config.Scanner.Interval != "@daily" || config.Updater.StopTimeout != 60 || !config.Updater.RemoveImages {
		t.Fatalf("file values did not override defaults: %+v", config)
	}
	// unset in the file, so still the defaults
	if config.Logging.File.Level != zerolog.DebugLevel || config.Scanner.ImageAge != 7 || config.Updater.RemoveVolumes {
		t.Fatalf("defaults not kept for values missing from the file: %+v", config)
	}
}

func TestLoadConfigWebhookKindDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "yacu.yaml")
	configFile := []byte(`webhooks:
  discord:
    url: https://discord.example/webhook
    kind:
      errors: false
`)
	if err := os.WriteFile(configPath, configFile, 0600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	kind := config.Webhooks["discord"].Kind
	if kind.Errors == nil || *kind.Errors {
		t.Error("errors set to false in the file was not kept")
	}
	if kind.ImageSuccess == nil || !*kind.ImageSuccess || kind.ContainerSuccess == nil || !*kind.ContainerSuccess {
		t.Errorf("kinds missing from the file are not enabled: %+v", kind)
	}
}

func TestExampleConfigsOnlyUseKnownFields(t *testing.T) {
	examples, err := filepath.Glob("../../examples/config/*.yaml")
	if err != nil || len(examples) == 0 {
		t.Fatalf("no example configs found: %v", err)
	}
	for _, example := range examples {
		t.Run(filepath.Base(example), func(t *testing.T) {
			file, err := os.Open(example)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()

			decoder := yaml.NewDecoder(file)
			decoder.KnownFields(true)
			if err := decoder.Decode(GetDefaultConfig()); err != nil {
				t.Fatalf("example does not match the config: %v", err)
			}
		})
	}
}

func TestLoadDatabaseDefaultsEmptyPath(t *testing.T) {
	t.Chdir(t.TempDir())

	db, err := DatabaseConfig{Path: " "}.LoadDatabase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := os.Stat("data.db"); err != nil {
		t.Fatalf("expected the default data.db to be used: %v", err)
	}
}

func TestReadmeConfigExamplesOnlyUseKnownFields(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	// the code blocks that are yacu configuration, recognized by their top-level key
	sections := regexp.MustCompile(`^(database|logging|scanner|updater|registries|webhooks):`)
	blocks := regexp.MustCompile("(?s)```\\n(.*?)```").FindAllStringSubmatch(string(readme), -1)
	checked := 0
	for _, block := range blocks {
		if !sections.MatchString(block[1]) {
			continue
		}
		checked++

		decoder := yaml.NewDecoder(strings.NewReader(block[1]))
		decoder.KnownFields(true)
		if err := decoder.Decode(GetDefaultConfig()); err != nil {
			t.Errorf("README example does not match the config: %v\n%s", err, block[1])
		}
	}
	if checked == 0 {
		t.Fatal("no configuration examples found in the README")
	}
}
