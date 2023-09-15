package config

import (
	"errors"
	"os"

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

func (c *Config) ReadConfigIfFound(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		// If file does not exist, just return that all went fine
		return nil
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
