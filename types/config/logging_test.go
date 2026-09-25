package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/rs/zerolog"
)

func TestCreateLoggerWarnsWhenLogDirectoryCannotBeCreated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}

	var console bytes.Buffer
	config := LoggingConfig{
		Console: ConsoleLogging{Level: zerolog.InfoLevel},
		// a directory cannot be created inside a file
		File: FileLogging{Directory: filepath.Join(file, "logs"), Level: zerolog.DebugLevel},
	}
	config.createLogger(&console)

	if !strings.Contains(console.String(), "creating log directory failed") {
		t.Fatalf("expected a warning on the console, got %q", console.String())
	}
}

func TestCreateLoggerCreatesAccessibleLogDirectory(t *testing.T) {
	previous := syscall.Umask(0o022)
	defer syscall.Umask(previous)

	directory := filepath.Join(t.TempDir(), "logs")
	config := LoggingConfig{
		Console: ConsoleLogging{Level: zerolog.InfoLevel},
		File:    FileLogging{Directory: directory, Level: zerolog.DebugLevel},
	}
	config.createLogger(&bytes.Buffer{})

	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	// without execute permission, only the owner could enter it to read the logs
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("log directory has mode %o, want 755", perm)
	}
}
