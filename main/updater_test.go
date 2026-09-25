package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/terrails/yacu/types/config"
	yacucontainer "github.com/terrails/yacu/types/container"
	"github.com/terrails/yacu/types/database"
	yacuimage "github.com/terrails/yacu/types/image"
	"github.com/terrails/yacu/types/webhook"
)

const (
	oldCreated = "2026-01-01T00:00:00Z"
	newCreated = "2026-06-01T00:00:00Z"
)

func sha(c byte) string {
	return "sha256:" + strings.Repeat(string(c), 64)
}

func newTestApp(t *testing.T, api *fakeDocker) Yacu {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "yacu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	return Yacu{
		Client:   api,
		Webhooks: webhook.NewWebhookHandler(),
		DB:       *db,
		Scanner:  config.Scanner{ImageAge: 7},
		Updater:  config.Updater{StopTimeout: 1},
	}
}

// loadContainer wraps a fake container the same way the scanner does
func loadContainer(t *testing.T, app Yacu, id string) *yacucontainer.Container {
	t.Helper()
	data, err := app.Client.ContainerInspect(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	cnt, err := yacucontainer.New(context.Background(), app.Client, &data, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	return cnt
}

func failWhen(method string, match func(id string) bool) func(string, string) error {
	return func(m, id string) error {
		if m == method && match(id) {
			return errors.New("injected " + method + " failure")
		}
		return nil
	}
}

func assertRunning(t *testing.T, api *fakeDocker, name, wantID string) {
	t.Helper()
	c := api.byName(name)
	if c == nil {
		t.Fatalf("container %s does not exist", name)
	}
	if wantID != "" && c.ID != wantID {
		t.Fatalf("container %s has ID %s, want %s", name, c.ID, wantID)
	}
	if c.State.Status != container.StateRunning {
		t.Fatalf("container %s is %s, want running", name, c.State.Status)
	}
}

func assertUpdateError(t *testing.T, err error, wantContext string) {
	t.Helper()
	var updateErr *updateError
	if !errors.As(err, &updateErr) {
		t.Fatalf("expected an updateError, got %v", err)
	}
	if updateErr.Context != wantContext {
		t.Fatalf("error context is %q, want %q", updateErr.Context, wantContext)
	}
	if strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rollback reported failures: %v", err)
	}
}

// setupApp creates a running "app" container whose tag has since moved to a new image
func setupApp(t *testing.T) (*fakeDocker, Yacu, *yacucontainer.Container) {
	api := newFakeDocker()
	api.addImage("test/app:latest", sha('a'), oldCreated, "test/app@"+sha('1'))
	id := api.addContainer("app", "test/app:latest", nil, true)

	app := newTestApp(t, api)
	cnt := loadContainer(t, app, id)
	api.addImage("test/app:latest", sha('b'), newCreated, "test/app@"+sha('2'))
	return api, app, cnt
}

func TestUpdateContainerReplacesContainer(t *testing.T) {
	api, app, cnt := setupApp(t)

	newCnt, warnings, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	assertRunning(t, api, "app", newCnt.ID)
	if newCnt.ID == cnt.ID {
		t.Fatal("container was not recreated")
	}
	if got := api.byName("app").Image; got != sha('b') {
		t.Fatalf("recreated container uses image %s, want %s", got, sha('b'))
	}
	if len(api.containers) != 1 {
		t.Fatalf("expected the previous container to be removed, have %d containers", len(api.containers))
	}
}

func TestUpdateContainerLetsNewImageDefaultsApply(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/app:latest", sha('a'), oldCreated, "test/app@"+sha('1'))
	oldImage := api.images[sha('a')]
	oldImage.Config = &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{
		Env:    []string{"APP_VERSION=1.0"},
		Labels: map[string]string{"org.opencontainers.image.version": "1.0"},
	}}
	api.images[sha('a')] = oldImage

	id := api.addContainer("app", "test/app:latest", map[string]string{"org.opencontainers.image.version": "1.0", "custom": "yes"}, true)
	// what the daemon made of the user's settings and the old image's defaults
	old := api.containers[id]
	old.Config.Hostname = id[:12]
	old.Config.Env = []string{"TZ=UTC", "APP_VERSION=1.0"}
	old.NetworkSettings.Networks["bridge"] = &network.EndpointSettings{EndpointID: "old-endpoint", IPAddress: "172.17.0.2", Aliases: []string{id[:12]}}

	app := newTestApp(t, api)
	cnt := loadContainer(t, app, id)
	api.addImage("test/app:latest", sha('b'), newCreated, "test/app@"+sha('2'))

	newCnt, _, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}

	// the fake daemon stores what it was asked to create as-is
	created := api.containers[newCnt.ID]
	if !slices.Equal(created.Config.Env, []string{"TZ=UTC"}) {
		t.Errorf("created with env %v, want only the user's", created.Config.Env)
	}
	if _, ok := created.Config.Labels["org.opencontainers.image.version"]; ok || created.Config.Labels["custom"] != "yes" {
		t.Errorf("created with labels %v, want only the user's", created.Config.Labels)
	}
	if created.Config.Hostname != "" {
		t.Errorf("created with the previous container's hostname %q", created.Config.Hostname)
	}
	if endpoint := created.NetworkSettings.Networks["bridge"]; endpoint.EndpointID != "" || endpoint.IPAddress != "" || len(endpoint.Aliases) != 0 {
		t.Errorf("created with the previous container's endpoint data %+v", endpoint)
	}
}

func TestUpdateContainerKeepsAnonymousVolumes(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/db:latest", sha('a'), oldCreated, "test/db@"+sha('1'))
	id := api.addContainer("db", "test/db:latest", nil, true)
	api.containers[id].Config.Volumes = map[string]struct{}{"/var/lib/db": {}}
	api.containers[id].Mounts = []container.MountPoint{{Type: mount.TypeVolume, Name: "anonymous-db-data", Destination: "/var/lib/db", RW: true}}

	app := newTestApp(t, api)
	cnt := loadContainer(t, app, id)
	api.addImage("test/db:latest", sha('b'), newCreated, "test/db@"+sha('2'))
	newImage := api.images[sha('b')]
	newImage.Config = &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Volumes: map[string]struct{}{"/var/lib/db": {}}}}
	api.images[sha('b')] = newImage

	newCnt, _, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}

	want := []mount.Mount{{Type: mount.TypeVolume, Source: "anonymous-db-data", Target: "/var/lib/db"}}
	if got := api.containers[newCnt.ID].HostConfig.Mounts; !reflect.DeepEqual(got, want) {
		t.Fatalf("created with mounts %+v, want the previous anonymous volume %+v", got, want)
	}
}

func TestUpdateContainerKeepsStoppedContainerStopped(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/app:latest", sha('a'), oldCreated, "test/app@"+sha('1'))
	id := api.addContainer("app", "test/app:latest", nil, false)
	app := newTestApp(t, api)
	cnt := loadContainer(t, app, id)

	newCnt, _, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}
	if api.called("ContainerStart", newCnt.ID) != 0 {
		t.Fatal("stopped container was started")
	}
	if len(api.containers) != 1 {
		t.Fatalf("expected one container, have %d", len(api.containers))
	}
}

func TestUpdateContainerRollsBackWhenCreateFails(t *testing.T) {
	api, app, cnt := setupApp(t)
	api.fail = failWhen("ContainerCreate", func(string) bool { return true })

	_, _, err := app.UpdateContainer(context.Background(), cnt)

	assertUpdateError(t, err, "Unable to create container")
	assertRunning(t, api, "app", cnt.ID)
	if len(api.containers) != 1 {
		t.Fatalf("expected only the original container, have %d", len(api.containers))
	}
}

func TestUpdateContainerRollsBackWhenStartFails(t *testing.T) {
	api, app, cnt := setupApp(t)
	api.fail = failWhen("ContainerStart", func(id string) bool { return id != cnt.ID })

	_, _, err := app.UpdateContainer(context.Background(), cnt)

	assertUpdateError(t, err, "Unable to start container")
	assertRunning(t, api, "app", cnt.ID)
	if len(api.containers) != 1 {
		t.Fatalf("expected the replacement to be removed, have %d containers", len(api.containers))
	}
}

func TestUpdateContainerRollsBackWhenNameIsTaken(t *testing.T) {
	api, app, cnt := setupApp(t)
	// leftover from an interrupted update
	api.addContainer("app"+oldContainerSuffix, "test/app:latest", nil, false)

	_, _, err := app.UpdateContainer(context.Background(), cnt)

	assertUpdateError(t, err, "Unable to rename container")
	assertRunning(t, api, "app", cnt.ID)
}

// setupCompose creates a compose project "p" with a "db" service and services depending on it
func setupCompose(t *testing.T) (*fakeDocker, Yacu, *yacucontainer.Container, map[string]string) {
	api := newFakeDocker()
	api.addImage("test/db:latest", sha('a'), oldCreated, "test/db@"+sha('1'))

	compose := func(project, service, dependsOn string) map[string]string {
		labels := map[string]string{
			yacucontainer.LABEL_PROJECT: project,
			yacucontainer.LABEL_SERVICE: service,
		}
		if len(dependsOn) > 0 {
			labels[yacucontainer.LABEL_DEPENDS_ON] = dependsOn
		}
		return labels
	}

	ids := map[string]string{
		"db":    api.addContainer("p-db-1", "test/db:latest", compose("p", "db", ""), true),
		"web":   api.addContainer("p-web-1", "test/web:latest", compose("p", "web", "cache:service_started:true,db:service_started:true"), true),
		"lazy":  api.addContainer("p-lazy-1", "test/web:latest", compose("p", "lazy", "db:service_started:false"), true),
		"other": api.addContainer("q-web-1", "test/web:latest", compose("q", "web", "db:service_started:true"), true),
	}

	app := newTestApp(t, api)
	cnt := loadContainer(t, app, ids["db"])
	api.addImage("test/db:latest", sha('b'), newCreated, "test/db@"+sha('2'))
	return api, app, cnt, ids
}

func TestUpdateContainerRestartsDependants(t *testing.T) {
	api, app, cnt, ids := setupCompose(t)

	newCnt, warnings, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	assertRunning(t, api, "p-db-1", newCnt.ID)
	if api.called("ContainerStop", ids["web"]) != 1 || api.called("ContainerStart", ids["web"]) != 1 {
		t.Fatal("dependant was not stopped and started again")
	}
	assertRunning(t, api, "p-web-1", ids["web"])

	if api.called("ContainerStop", ids["lazy"]) != 0 {
		t.Fatal("dependant with restart: false was stopped")
	}
	if api.called("ContainerStop", ids["other"]) != 0 {
		t.Fatal("service from another compose project was stopped")
	}
}

func TestUpdateContainerRestartsDependantsWhenStopFails(t *testing.T) {
	api, app, cnt, ids := setupCompose(t)
	api.fail = failWhen("ContainerStop", func(id string) bool { return id == ids["db"] })

	_, _, err := app.UpdateContainer(context.Background(), cnt)

	assertUpdateError(t, err, "Unable to stop container")
	assertRunning(t, api, "p-db-1", ids["db"])
	assertRunning(t, api, "p-web-1", ids["web"])
}

func TestUpdateContainerRestartsDependantsWhenCreateFails(t *testing.T) {
	api, app, cnt, ids := setupCompose(t)
	api.fail = failWhen("ContainerCreate", func(string) bool { return true })

	_, _, err := app.UpdateContainer(context.Background(), cnt)

	assertUpdateError(t, err, "Unable to create container")
	assertRunning(t, api, "p-db-1", ids["db"])
	assertRunning(t, api, "p-web-1", ids["web"])
}

func TestGetDependingContainersMatchesContainerNameOutsideCompose(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/db:latest", sha('a'), oldCreated, "test/db@"+sha('1'))
	dbID := api.addContainer("db", "test/db:latest", nil, true)
	webID := api.addContainer("web", "test/web:latest", map[string]string{yacucontainer.LABEL_DEPENDS_ON: "db"}, true)
	app := newTestApp(t, api)

	data, _ := api.ContainerInspect(context.Background(), dbID)
	dependants, err := app.GetDependingContainers(context.Background(), &data)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependants) != 1 || dependants[0].Data.ID != webID {
		t.Fatalf("expected web to depend on db, got %d dependants", len(dependants))
	}
	if dependants[0].DependencyType != yacucontainer.DEPENDENCY_HEALTHY {
		t.Fatalf("expected default condition %s, got %s", yacucontainer.DEPENDENCY_HEALTHY, dependants[0].DependencyType)
	}
}

func TestPullImageReportsErrorsFromStream(t *testing.T) {
	api := newFakeDocker()
	app := newTestApp(t, api)
	named, _ := reference.ParseNormalizedNamed("test/app:latest")
	tagged := named.(reference.NamedTagged)

	if err := app.PullImage(context.Background(), tagged); err != nil {
		t.Fatalf("successful pull returned %v", err)
	}

	api.pullError("test/app:latest", "manifest unknown")
	err := app.PullImage(context.Background(), tagged)
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("expected the stream error to be returned, got %v", err)
	}
}

func TestApplyUpdatesSkipsOnlyContainersWhoseImageFailed(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/a:latest", sha('a'), oldCreated, "test/a@"+sha('1'))
	api.addImage("test/b:latest", sha('b'), oldCreated, "test/b@"+sha('2'))
	aID := api.addContainer("a", "test/a:latest", nil, true)
	bID := api.addContainer("b", "test/b:latest", nil, true)
	app := newTestApp(t, api)

	// what the scanner recorded about the newer remote images
	created, _ := time.Parse(time.RFC3339, newCreated)
	for _, name := range []string{"test/a:latest", "test/b:latest"} {
		if _, err := app.DB.SaveRemoteImage(name, "docker.io", created, digest.Digest(sha('9'))); err != nil {
			t.Fatal(err)
		}
	}

	api.pullError("test/a:latest", "unauthorized")
	api.addUntaggedImage(sha('c'), newCreated, "test/b@"+sha('9'))
	api.pullResult("test/b:latest", sha('c'))

	containers := yacucontainer.Containers{loadContainer(t, app, aID), loadContainer(t, app, bID)}
	if failures := app.ApplyUpdates(context.Background(), containers); failures != 1 {
		t.Fatalf("%d failures reported, want 1 for container a", failures)
	}

	assertRunning(t, api, "a", aID)
	b := api.byName("b")
	if b == nil || b.ID == bID || b.Image != sha('c') {
		t.Fatal("container b was not updated after container a's image failed to pull")
	}
}

func TestFetchUpdatesFiltersBeforeInspecting(t *testing.T) {
	api := newFakeDocker()
	enabled := map[string]string{yacucontainer.LABEL_ENABLE: "true"}
	api.addImage("test/plain:latest", sha('a'), oldCreated, "test/plain@"+sha('1'))
	api.addImage("local/build:latest", sha('b'), oldCreated, "")
	api.addImage("test/pinned:1@"+sha('3'), sha('c'), oldCreated, "test/pinned@"+sha('3'))
	api.addImage("ghcr.io/terrails/yacu:latest", sha('d'), oldCreated, "ghcr.io/terrails/yacu@"+sha('4'))

	ids := map[string]string{
		"plain":   api.addContainer("plain", "test/plain:latest", nil, true),
		"stopped": api.addContainer("stopped", "local/build:latest", enabled, false),
		"local":   api.addContainer("local", "local/build:latest", enabled, true),
		"pinned":  api.addContainer("pinned", "test/pinned:1@"+sha('3'), enabled, true),
		"self":    api.addContainer("yacu", "ghcr.io/terrails/yacu:latest", enabled, true),
	}
	app := newTestApp(t, api)

	containers, errs := app.FetchUpdates(context.Background())
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(containers) > 0 {
		t.Fatalf("expected no updatable containers, got %d", len(containers))
	}

	for name, want := range map[string]int{"plain": 0, "stopped": 0, "local": 1, "pinned": 1, "self": 1} {
		if got := api.called("ContainerInspect", ids[name]); got != want {
			t.Errorf("container %s inspected %d times, want %d", name, got, want)
		}
	}

	app.Scanner.ScanStopped = true
	if _, errs := app.FetchUpdates(context.Background()); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if api.called("ContainerInspect", ids["stopped"]) != 1 {
		t.Error("stopped container was not scanned with scan_stopped enabled")
	}
}

func TestFetchUpdatesRetriesFailedChecks(t *testing.T) {
	previousDelay := scanRetryDelay
	scanRetryDelay = 0
	t.Cleanup(func() { scanRetryDelay = previousDelay })

	api := newFakeDocker()
	api.addImage("local/build:latest", sha('b'), oldCreated, "")
	id := api.addContainer("local", "local/build:latest", map[string]string{yacucontainer.LABEL_ENABLE: "true"}, true)
	app := newTestApp(t, api)

	failures := 2
	api.fail = func(method, _ string) error {
		if method == "ContainerInspect" && failures > 0 {
			failures--
			return errors.New("daemon busy")
		}
		return nil
	}

	if _, errs := app.FetchUpdates(context.Background()); len(errs) > 0 {
		t.Fatalf("expected the third attempt to succeed, got %v", errs)
	}
	if got := api.called("ContainerInspect", id); got != 3 {
		t.Fatalf("container inspected %d times, want 3", got)
	}

	api.calls = nil
	api.fail = failWhen("ContainerInspect", func(string) bool { return true })
	if _, errs := app.FetchUpdates(context.Background()); len(errs) != 1 {
		t.Fatalf("expected one error after exhausting retries, got %v", errs)
	}
	api.calls = nil
	if failures := app.Run(context.Background()); failures != 1 {
		t.Fatalf("%d failures reported, want 1 for the container that could not be checked", failures)
	}
	if got := api.called("ContainerInspect", id); got != scanAttempts {
		t.Fatalf("container inspected %d times, want %d", got, scanAttempts)
	}
}

func TestRemoveUnusedImagesKeepsImagesOfStoppedContainers(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/a:latest", sha('a'), oldCreated, "test/a@"+sha('1'))
	api.addUntaggedImage(sha('b'), oldCreated, "test/a@"+sha('2'))
	api.addContainer("idle", "test/a:latest", nil, false)
	app := newTestApp(t, api)

	count := app.RemoveUnusedImages(context.Background(),
		&yacuimage.ImageData{ID: sha('a')},
		&yacuimage.ImageData{ID: sha('b')},
	)

	if count != 1 {
		t.Fatalf("removed %d images, want 1", count)
	}
	if _, ok := api.images[sha('a')]; !ok {
		t.Fatal("image used by a stopped container was removed")
	}
	if _, ok := api.images[sha('b')]; ok {
		t.Fatal("unused image was not removed")
	}
}

func TestApplyUpdatesPullsWhenTagIsMissing(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/app:latest", sha('a'), oldCreated, "test/app@"+sha('1'))
	id := api.addContainer("app", "test/app:latest", nil, true)
	app := newTestApp(t, api)
	hook := withRecordingHook(app)
	cnt := loadContainer(t, app, id)

	// the container still runs its image, but the tag no longer exists locally
	delete(api.tags, normalize("test/app:latest"))

	created, _ := time.Parse(time.RFC3339, newCreated)
	if _, err := app.DB.SaveRemoteImage("test/app:latest", "docker.io", created, digest.Digest(sha('9'))); err != nil {
		t.Fatal(err)
	}
	api.addUntaggedImage(sha('c'), newCreated, "test/app@"+sha('9'))
	api.pullResult("test/app:latest", sha('c'))

	app.ApplyUpdates(context.Background(), yacucontainer.Containers{cnt})

	if hook.errors != 0 {
		t.Fatalf("%d error notifications sent", hook.errors)
	}
	if got := api.byName("app"); got == nil || got.ID == id || got.Image != sha('c') {
		t.Fatal("container was not updated after pulling its missing tag")
	}
}

func TestPullImagesFailsOnInspectErrors(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/app:latest", sha('a'), oldCreated, "test/app@"+sha('1'))
	id := api.addContainer("app", "test/app:latest", nil, true)
	app := newTestApp(t, api)
	hook := withRecordingHook(app)
	cnt := loadContainer(t, app, id)

	api.fail = failWhen("ImageInspect", func(ref string) bool { return ref == normalize("test/app:latest") })

	failed := app.PullImages(context.Background(), yacucontainer.Containers{cnt})

	if _, ok := failed[cnt.Repository.String()]; !ok {
		t.Fatal("image was not reported as failed")
	}
	if api.called("ImagePull", normalize("test/app:latest")) != 0 {
		t.Fatal("image was pulled although checking it failed")
	}
	if hook.errors != 1 {
		t.Fatalf("%d error notifications sent, want 1", hook.errors)
	}
}

func TestPullImageSendsRegistryCredentials(t *testing.T) {
	// no credentials from the host
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("DOCKER_CONFIG", home)
	// stored by `docker login ghcr.io`
	stored := `{"auths": {"ghcr.io": {"auth": "` + base64.StdEncoding.EncodeToString([]byte("stored-user:stored-token")) + `"}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(stored), 0600); err != nil {
		t.Fatal(err)
	}

	api := newFakeDocker()
	app := newTestApp(t, api)
	app.Registries = config.RegistryEntries{{Domain: "https://index.docker.io/v1/", Username: "hub-user", Password: "hub-token"}}

	tests := []struct{ ref, wantUser string }{
		{"test/app:latest", "hub-user"},
		{"ghcr.io/owner/app:latest", "stored-user"},
		{"quay.io/owner/app:latest", ""},
	}
	for _, test := range tests {
		named, _ := reference.ParseNormalizedNamed(test.ref)
		if err := app.PullImage(context.Background(), named.(reference.NamedTagged)); err != nil {
			t.Fatal(err)
		}

		auth := api.pullAuth[normalize(test.ref)]
		if len(test.wantUser) == 0 {
			if len(auth) > 0 {
				t.Errorf("%s was pulled with credentials although there are none", test.ref)
			}
			continue
		}
		decoded, err := registry.DecodeAuthConfig(auth)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Username != test.wantUser {
			t.Errorf("%s was pulled as %q, want %q", test.ref, decoded.Username, test.wantUser)
		}
	}
}
