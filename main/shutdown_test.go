package main

import (
	"context"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/terrails/yacu/types/config"
	yacucontainer "github.com/terrails/yacu/types/container"
	"github.com/terrails/yacu/types/image"
)

// counts the notifications that would have been sent
type recordingHook struct {
	errors, updates int
}

func (h *recordingHook) Error(ctx context.Context, context string, err error) { h.errors++ }
func (h *recordingHook) ImageUpdated(ctx context.Context, prevImage, newImage *image.ImageData) {
	h.updates++
}
func (h *recordingHook) ImageError(ctx context.Context, image *image.ImageData, context string, err error) {
	h.errors++
}
func (h *recordingHook) ImageRemovalFailed(ctx context.Context, image *image.ImageData, err error) {
	h.errors++
}
func (h *recordingHook) ContainerUpdated(ctx context.Context, prevContainer, newContainer *yacucontainer.Container, warnings ...string) {
	h.updates++
}
func (h *recordingHook) ContainerError(ctx context.Context, container *yacucontainer.Container, context string, err error) {
	h.errors++
}

func withRecordingHook(app Yacu) *recordingHook {
	enabled := true
	hook := &recordingHook{}
	app.Webhooks.Append(hook, &config.WebhookKind{Errors: &enabled, ImageSuccess: &enabled, ContainerSuccess: &enabled})
	return hook
}

// setupTwoUpdates creates running containers "a" and "b" whose newer images are ready to be pulled
func setupTwoUpdates(t *testing.T) (*fakeDocker, Yacu, yacucontainer.Containers, map[string]string) {
	api := newFakeDocker()
	api.addImage("test/a:latest", sha('a'), oldCreated, "test/a@"+sha('1'))
	api.addImage("test/b:latest", sha('b'), oldCreated, "test/b@"+sha('2'))
	ids := map[string]string{
		"a": api.addContainer("a", "test/a:latest", nil, true),
		"b": api.addContainer("b", "test/b:latest", nil, true),
	}
	app := newTestApp(t, api)

	created, _ := time.Parse(time.RFC3339, newCreated)
	for name, newID := range map[string]byte{"a": 'c', "b": 'd'} {
		ref := "test/" + name + ":latest"
		if _, err := app.DB.SaveRemoteImage(ref, "docker.io", created, digest.Digest(sha('9'))); err != nil {
			t.Fatal(err)
		}
		api.addUntaggedImage(sha(newID), newCreated, "test/"+name+"@"+sha('9'))
		api.pullResult(ref, sha(newID))
	}

	containers := yacucontainer.Containers{loadContainer(t, app, ids["a"]), loadContainer(t, app, ids["b"])}
	return api, app, containers, ids
}

func TestApplyUpdatesFinishesUpdateInProgressOnShutdown(t *testing.T) {
	api, app, containers, ids := setupTwoUpdates(t)
	hook := withRecordingHook(app)

	// shutdown is requested in the middle of updating "a"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.fail = func(method, id string) error {
		if method == "ContainerCreate" && id == "a" {
			cancel()
		}
		return nil
	}

	app.ApplyUpdates(ctx, containers)

	a := api.byName("a")
	if a == nil || a.ID == ids["a"] || a.Image != sha('c') {
		t.Fatal("update of a in progress at shutdown was not completed")
	}
	assertRunning(t, api, "a", a.ID)
	if api.byName("a"+oldContainerSuffix) != nil {
		t.Fatal("previous container of a was left behind")
	}

	assertRunning(t, api, "b", ids["b"])
	if api.called("ContainerStop", ids["b"]) != 0 {
		t.Fatal("update of b was started after shutdown")
	}
	if hook.errors != 0 {
		t.Fatalf("%d error notifications sent during shutdown", hook.errors)
	}
}

func TestApplyUpdatesStopsQuietlyWhenShutdownInterruptsPulls(t *testing.T) {
	api, app, containers, ids := setupTwoUpdates(t)
	hook := withRecordingHook(app)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.fail = func(method, id string) error {
		if method == "ImagePull" {
			cancel()
			return context.Canceled
		}
		return nil
	}

	app.ApplyUpdates(ctx, containers)

	if got := api.called("ImageInspect", normalize("test/b:latest")) + api.called("ImagePull", normalize("test/b:latest")); got != 0 {
		t.Fatal("pulling continued after shutdown")
	}
	assertRunning(t, api, "a", ids["a"])
	assertRunning(t, api, "b", ids["b"])
	if hook.errors != 0 {
		t.Fatalf("%d error notifications sent for the interrupted pull", hook.errors)
	}
}

func TestRunReportsNothingWhenInterrupted(t *testing.T) {
	api := newFakeDocker()
	api.addImage("test/a:latest", sha('a'), oldCreated, "test/a@"+sha('1'))
	id := api.addContainer("a", "test/a:latest", map[string]string{yacucontainer.LABEL_ENABLE: "true"}, true)
	app := newTestApp(t, api)
	hook := withRecordingHook(app)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.Run(ctx)

	if hook.errors != 0 {
		t.Fatalf("%d error notifications sent for an interrupted scan", hook.errors)
	}
	assertRunning(t, api, "a", id)
}

func TestFetchUpdatesDoesNotRetryAfterShutdown(t *testing.T) {
	api := newFakeDocker()
	api.addImage("local/build:latest", sha('b'), oldCreated, "")
	enabled := map[string]string{yacucontainer.LABEL_ENABLE: "true"}
	first := api.addContainer("first", "local/build:latest", enabled, true)
	second := api.addContainer("second", "local/build:latest", enabled, true)
	app := newTestApp(t, api)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.fail = func(method, id string) error {
		if method == "ContainerInspect" {
			cancel()
		}
		return nil
	}

	done := make(chan struct{})
	go func() {
		app.FetchUpdates(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan kept retrying after shutdown")
	}
	if got := api.called("ContainerInspect", first) + api.called("ContainerInspect", second); got != 1 {
		t.Fatalf("containers inspected %d times after shutdown, want 1", got)
	}
}
