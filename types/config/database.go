package config

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"
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

	createTables := false
	if _, err := os.Stat(c.Path); errors.Is(err, os.ErrNotExist) {
		file, err := os.Create(c.Path)
		if err != nil {
			logger.Err(err).Msg("creating database file failed")
			return nil, err
		}
		file.Close()

		createTables = true
	}

	db, err := sql.Open("sqlite3", c.Path)
	if err != nil {
		logger.Err(err).Msg("opening database file failed")
		return nil, err
	}

	database := &database.Database{
		DB: db,
	}

	// Create tables if file was just created
	if createTables {

		if _, err = database.Exec(
			`CREATE TABLE remote_images (
				id 				INTEGER PRIMARY KEY,
				name 			TEXT NOT NULL,
				domain	 		TEXT NOT NULL,
				created			TEXT NOT NULL,
				digest			TEXT NOT NULL,
				last_check      TEXT NOT NULL,
				unique (name, domain)
			);`,
		); err != nil {
			logger.Err(err).Msg("creating remote_images table failed")
			return nil, err
		}
	}

	return database, nil
}
