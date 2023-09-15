package config

import (
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"
)

type ConsoleLogging struct {
	Level zerolog.Level `yaml:"level"`
}

type FileLogging struct {
	Directory string        `yaml:"directory"`
	Level     zerolog.Level `yaml:"level"`
}

type LoggingConfig struct {
	Console ConsoleLogging `yaml:"console"`
	File    FileLogging    `yaml:"file"`
}

func (c LoggingConfig) CreateLogger() *zerolog.Logger {
	consoleWriter := &zerolog.FilteredLevelWriter{
		Writer: zerolog.LevelWriterAdapter{
			Writer: zerolog.ConsoleWriter{
				Out:        os.Stdout,
				TimeFormat: time.RFC3339,
			},
		},
		Level: c.Console.Level,
	}

	if len(strings.TrimSpace(c.File.Directory)) == 0 {
		logger := zerolog.New(consoleWriter).
			With().Timestamp().Caller().
			Logger()
		return &logger
	}

	if err := os.MkdirAll(c.File.Directory, 0744); err != nil {
		logger := zerolog.New(consoleWriter).
			With().Timestamp().Caller().
			Logger()
		return &logger
	}

	writers := []io.Writer{
		consoleWriter,
		&zerolog.FilteredLevelWriter{
			Writer: zerolog.LevelWriterAdapter{
				// Want to avoid json output so just using console writer without color
				Writer: zerolog.ConsoleWriter{
					NoColor: true,
					Out: &lumberjack.Logger{
						Filename:   path.Join(c.File.Directory, "yacu.log"),
						MaxSize:    10,
						MaxBackups: 3,
						MaxAge:     28,
						Compress:   true,
					},
					TimeFormat: time.RFC3339,
				},
			},
			Level: c.File.Level,
		},
	}

	writer := zerolog.MultiLevelWriter(writers...)
	logger := zerolog.New(writer).With().Timestamp().Caller().Logger()
	return &logger
}
