package config

import (
	"context"
	"strings"

	"github.com/rs/zerolog"
	"github.com/terrails/yacu/types/database"
)

type DatabaseConfig struct {
	Path string
}

func (c DatabaseConfig) LoadDatabase(ctx context.Context) (*database.Database, error) {
	if len(strings.TrimSpace(c.Path)) == 0 {
		c.Path = "yacu.db"
	}

	logger := zerolog.Ctx(ctx).With().Str("service", "database").Str("path", c.Path).Logger()

	db, err := database.Open(c.Path)
	if err != nil {
		logger.Err(err).Msg("opening database failed")
		return nil, err
	}

	return db, nil
}
