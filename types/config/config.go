package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Database   DatabaseConfig  `yaml:"database"`
	Logging    LoggingConfig   `yaml:"logging"`
	Scanner    Scanner         `yaml:"scanner"`
	Updater    Updater         `yaml:"updater"`
	Registries RegistryEntries `yaml:"registries"`
	Webhooks   Webhooks        `yaml:"webhooks"`
}

func GetDefaultConfig() *Config {
	return &Config{
		Database: DatabaseConfig{
			Path: "data.db",
		},
		Logging: LoggingConfig{
			Console: ConsoleLogging{
				Level: zerolog.InfoLevel,
			},
			File: FileLogging{
				Directory: "logs",
				Level:     zerolog.DebugLevel,
			},
		},
		Scanner: Scanner{
			Interval:    "@weekly",
			ImageAge:    7,
			ScanAll:     false,
			ScanStopped: false,
			FailOnError: true,
		},
		Updater: Updater{
			StopTimeout:   30,
			RemoveVolumes: false,
			RemoveImages:  false,
		},
		Registries: RegistryEntries{},
		Webhooks:   Webhooks{},
	}
}

func LoadConfig(path string) (*Config, error) {
	config := GetDefaultConfig()

	if err := config.ReadConfigIfFound(path); err != nil {
		return nil, err
	}
	if err := config.ApplyEnvironment(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}

	return config, nil
}

func (c *Config) ReadConfigIfFound(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	if info.IsDir() {
		// path points to a directory
		return errors.New("given path is a directory, expected a file")
	}

	file, err := os.ReadFile(path)
	if err != nil {
		// file cannot be read
		return err
	}

	if err := yaml.Unmarshal(file, c); err != nil {
		// yaml parser failed
		return err
	}

	return nil
}

func (c *Config) ApplyEnvironment() error {
	if value, ok := os.LookupEnv("YACU_DATABASE_PATH"); ok {
		c.Database.Path = value
	}
	if value, ok := os.LookupEnv("YACU_LOGGING_CONSOLE_LEVEL"); ok {
		level, err := zerolog.ParseLevel(value)
		if err != nil {
			return fmt.Errorf("YACU_LOGGING_CONSOLE_LEVEL: %w", err)
		}
		c.Logging.Console.Level = level
	}
	if value, ok := os.LookupEnv("YACU_LOGGING_FILE_DIRECTORY"); ok {
		c.Logging.File.Directory = value
	}
	if value, ok := os.LookupEnv("YACU_LOGGING_FILE_LEVEL"); ok {
		level, err := zerolog.ParseLevel(value)
		if err != nil {
			return fmt.Errorf("YACU_LOGGING_FILE_LEVEL: %w", err)
		}
		c.Logging.File.Level = level
	}
	if value, ok := os.LookupEnv("YACU_SCANNER_INTERVAL"); ok {
		c.Scanner.Interval = value
	}
	if value, ok := os.LookupEnv("YACU_SCANNER_IMAGE_AGE"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("YACU_SCANNER_IMAGE_AGE must be an integer: %w", err)
		}
		c.Scanner.ImageAge = parsed
	}
	if value, ok := os.LookupEnv("YACU_SCANNER_SCAN_ALL"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("YACU_SCANNER_SCAN_ALL must be a boolean: %w", err)
		}
		c.Scanner.ScanAll = parsed
	}
	if value, ok := os.LookupEnv("YACU_SCANNER_SCAN_STOPPED"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("YACU_SCANNER_SCAN_STOPPED must be a boolean: %w", err)
		}
		c.Scanner.ScanStopped = parsed
	}
	if value, ok := os.LookupEnv("YACU_SCANNER_FAIL_ON_ERROR"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("YACU_SCANNER_FAIL_ON_ERROR must be a boolean: %w", err)
		}
		c.Scanner.FailOnError = parsed
	}
	if value, ok := os.LookupEnv("YACU_UPDATER_STOP_TIMEOUT"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("YACU_UPDATER_STOP_TIMEOUT must be an integer: %w", err)
		}
		c.Updater.StopTimeout = parsed
	}
	if value, ok := os.LookupEnv("YACU_UPDATER_REMOVE_VOLUMES"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("YACU_UPDATER_REMOVE_VOLUMES must be a boolean: %w", err)
		}
		c.Updater.RemoveVolumes = parsed
	}
	if value, ok := os.LookupEnv("YACU_UPDATER_REMOVE_IMAGES"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("YACU_UPDATER_REMOVE_IMAGES must be a boolean: %w", err)
		}
		c.Updater.RemoveImages = parsed
	}

	return nil
}

func (c Config) Validate() error {
	if !c.Scanner.IsIntervalValid() {
		return fmt.Errorf("invalid cron format for scanner.interval: %q", c.Scanner.Interval)
	}
	return nil
}
