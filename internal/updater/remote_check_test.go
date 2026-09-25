package updater

import (
	"context"
	"testing"
	"time"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	"github.com/terrails/yacu/internal/config"
	yacuregistry "github.com/terrails/yacu/internal/registry"
)

func TestIsRemotePullable(t *testing.T) {
	var (
		old   = time.Now().Add(-30 * 24 * time.Hour)
		young = time.Now().Add(-24 * time.Hour)
		// the container runs the image with digest sha('1')
		running = digest.Digest(sha('1'))
		newer   = digest.Digest(sha('2'))
	)
	type check struct {
		created time.Time
		digest  digest.Digest
		ago     time.Duration
	}

	tests := []struct {
		name          string
		checkInterval int
		previous      *check // what the last registry check found
		registry      check  // what the registry has now
		want          bool
		wantQueries   int
	}{
		{"first check finds an update", 24, nil, check{old, newer, 0}, true, 1},
		{"first check finds a too young image", 24, nil, check{young, newer, 0}, false, 1},
		{"first check finds the running image", 24, nil, check{old, running, 0}, false, 1},
		{"recent check found the running image", 24, &check{old, running, time.Hour}, check{old, newer, 0}, false, 0},
		{"recent check found a too young image", 24, &check{young, newer, time.Hour}, check{old, newer, 0}, false, 0},
		{"recent check found an update, still there", 24, &check{old, newer, time.Hour}, check{old, newer, 0}, true, 1},
		{"recent check found an update, tag moved to a too young image", 24, &check{old, newer, time.Hour}, check{young, digest.Digest(sha('3')), 0}, false, 1},
		{"previous check is outdated", 24, &check{old, running, 48 * time.Hour}, check{old, newer, 0}, true, 1},
		{"check interval of 0 always queries", 0, &check{old, running, time.Minute}, check{old, newer, 0}, true, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := newFakeDocker()
			api.addImage("test/app:latest", sha('a'), oldCreated, "test/app@"+string(running))
			id := api.addContainer("app", "test/app:latest", nil, true)
			app := newTestApp(t, api)
			app.Scanner.CheckInterval = test.checkInterval
			cnt := loadContainer(t, app, id)

			queries := 0
			app.RegistryLookup = func(ctx context.Context, entries *config.RegistryEntries, named reference.Named) (*yacuregistry.ImageData, error) {
				queries++
				created := test.registry.created
				return &yacuregistry.ImageData{Created: &created, Digest: test.registry.digest}, nil
			}

			if test.previous != nil {
				if _, err := app.DB.SaveRemoteImage("test/app:latest", "docker.io", test.previous.created, test.previous.digest); err != nil {
					t.Fatal(err)
				}
				lastCheck := time.Now().Add(-test.previous.ago).UTC().Format(time.RFC3339Nano)
				if _, err := app.DB.DB.Exec("UPDATE remote_images SET last_check=? WHERE name=?", lastCheck, "test/app:latest"); err != nil {
					t.Fatal(err)
				}
			}

			got, err := app.IsRemotePullable(context.Background(), cnt)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("got %v, want %v", got, test.want)
			}
			if queries != test.wantQueries {
				t.Errorf("registry queried %d times, want %d", queries, test.wantQueries)
			}

			// a query is recorded, so that the next scans can rely on it
			if queries > 0 {
				row, err := app.DB.GetRemoteImageFromName("test/app:latest")
				if err != nil {
					t.Fatal(err)
				}
				if row.Digest != test.registry.digest || time.Since(row.LastCheck) > time.Minute {
					t.Errorf("registry check was not recorded: %+v", row)
				}
			}
		})
	}
}
