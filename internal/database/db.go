package database

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

type Database struct {
	DB *sql.DB
}

// Applied in order; the index of the next one to run is stored in PRAGMA user_version.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS remote_images (
		id 				INTEGER PRIMARY KEY,
		name 			TEXT NOT NULL,
		domain	 		TEXT NOT NULL,
		created			TEXT NOT NULL,
		digest			TEXT NOT NULL,
		last_check      TEXT NOT NULL,
		unique (name, domain)
	);`,
}

// Opens (creating if needed) the sqlite database at path and brings its schema up to date.
func Open(path string) (*Database, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	database := &Database{DB: db}
	if err := database.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return database, nil
}

func (d Database) Close() error {
	return d.DB.Close()
}

func (d Database) migrate() error {
	var version int
	if err := d.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("reading schema version failed: %w", err)
	}

	for ; version < len(migrations); version++ {
		tx, err := d.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[version]); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %d failed: %w", version+1, err)
		}
		// PRAGMA does not accept bound parameters
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("updating schema version failed: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
