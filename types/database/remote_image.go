package database

import (
	"time"

	"github.com/opencontainers/go-digest"
)

type RemoteImageRow struct {
	RowId     int64         // rowid
	Name      string        // name including tag, used for unique row
	Domain    string        // registry domain
	Created   time.Time     // created time
	Digest    digest.Digest // remote digest
	LastCheck time.Time     // last update check time
}

func (d Database) GetRemoteImageFromId(imageId int64) (*RemoteImageRow, error) {
	return d.getRemoteImage("SELECT * FROM remote_images WHERE id=?", imageId)
}

func (d Database) GetRemoteImageFromName(imageName string) (*RemoteImageRow, error) {
	return d.getRemoteImage("SELECT * FROM remote_images WHERE name=?", imageName)
}

func (d Database) getRemoteImage(stmt string, args ...any) (*RemoteImageRow, error) {
	row, err := d.QueryRow(stmt, args...)
	if err != nil {
		return nil, err
	}

	var id int64
	var name, domain, rcreated, rdigest, rlastcheck string

	err = row.Scan(&id, &name, &domain, &rcreated, &rdigest, &rlastcheck)
	if err != nil {
		return nil, err
	}

	created, err := time.Parse(time.RFC3339Nano, rcreated)
	if err != nil {
		return nil, err
	}

	digest, err := digest.Parse(rdigest)
	if err != nil {
		return nil, err
	}

	lastcheck, err := time.Parse(time.RFC3339Nano, rlastcheck)
	if err != nil {
		return nil, err
	}

	return &RemoteImageRow{
		RowId:     id,
		Name:      name,
		Domain:    domain,
		Created:   created,
		Digest:    digest,
		LastCheck: lastcheck,
	}, nil
}

func (d Database) SaveRemoteImage(name string, domain string, created time.Time, digest digest.Digest) (*int64, error) {
	rlastcheck := time.Now().UTC().Format(time.RFC3339Nano)
	rcreated := created.UTC().Format(time.RFC3339Nano)
	rdigest := digest.String()

	result, err := d.Exec(
		`INSERT OR REPLACE 
			INTO remote_images (name, domain, created, digest, last_check) 
			VALUES (?, ?, ?, ?, ?)`,
		name, domain, rcreated, rdigest, rlastcheck,
	)

	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &id, nil
}

func (d Database) UpdateRemoteImage(rowid int64, created *time.Time, digest *digest.Digest) error {
	rcreated := created.UTC().Format(time.RFC3339Nano)
	rdigest := digest.String()

	_, err := d.Exec("UPDATE remote_images SET created=?, digest=? WHERE id=?", rcreated, rdigest, rowid)
	return err
}

func (d Database) UpdateRemoteImageCheck(rowid int64) error {
	lastCheck := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := d.Exec("UPDATE remote_images SET last_check=? WHERE id=?", lastCheck, rowid)
	return err
}
