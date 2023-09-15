package database

import (
	"database/sql"
)

type Database struct {
	DB *sql.DB
}

func (d Database) Exec(stmt string, args ...any) (sql.Result, error) {
	query, err := d.DB.Prepare(stmt)
	if err != nil {
		return nil, err
	}

	return query.Exec(args...)
}

func (d Database) QueryRow(stmt string, args ...any) (*sql.Row, error) {
	query, err := d.DB.Prepare(stmt)
	if err != nil {
		return nil, err
	}

	row := query.QueryRow(args...)
	return row, row.Err()
}
