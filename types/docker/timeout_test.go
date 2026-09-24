package docker

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
)

// records the deadline of the last call it received
type deadlineAPI struct {
	API
	deadline    time.Time
	hasDeadline bool
}

func (d *deadlineAPI) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	d.deadline, d.hasDeadline = ctx.Deadline()
	return nil, nil
}

func (d *deadlineAPI) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	d.deadline, d.hasDeadline = ctx.Deadline()
	return nil
}

func assertDeadline(t *testing.T, api *deadlineAPI, want time.Duration) {
	t.Helper()
	if !api.hasDeadline {
		t.Fatal("call has no deadline")
	}
	// allow for the time the call itself took
	if got := time.Until(api.deadline); got > want || got < want-time.Second {
		t.Fatalf("deadline is %v away, want %v", got, want)
	}
}

func TestWithTimeoutsBoundsCalls(t *testing.T) {
	inner := &deadlineAPI{}
	api := WithTimeouts(inner, time.Minute)

	api.ContainerList(context.Background(), container.ListOptions{})
	assertDeadline(t, inner, time.Minute)
}

func TestWithTimeoutsAddsStopTimeout(t *testing.T) {
	inner := &deadlineAPI{}
	api := WithTimeouts(inner, time.Minute)

	stopTimeout := 30
	api.ContainerStop(context.Background(), "id", container.StopOptions{Timeout: &stopTimeout})
	assertDeadline(t, inner, time.Minute+30*time.Second)

	api.ContainerStop(context.Background(), "id", container.StopOptions{})
	assertDeadline(t, inner, time.Minute+defaultStopTimeout)

	// -1 makes the daemon wait for the container indefinitely
	stopTimeout = -1
	api.ContainerStop(context.Background(), "id", container.StopOptions{Timeout: &stopTimeout})
	if inner.hasDeadline {
		t.Fatal("stop without a timeout was given a deadline")
	}
}
