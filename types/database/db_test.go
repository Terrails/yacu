package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

func TestOpenCreatesSchemaInExistingEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	// e.g. a file created with `touch` before being bind-mounted
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	dgst := digest.Digest("sha256:" + "1111111111111111111111111111111111111111111111111111111111111111")
	if _, err := db.SaveRemoteImage("test/app:latest", "docker.io", created, dgst); err != nil {
		t.Fatal(err)
	}

	row, err := db.GetRemoteImageFromName("test/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Created.Equal(created) || row.Digest != dgst || row.Domain != "docker.io" {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")

	for i := 0; i < 2; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}

		var version int
		if err := db.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != len(migrations) {
			t.Fatalf("schema version is %d, want %d", version, len(migrations))
		}
		db.Close()
	}
}
