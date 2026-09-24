package container

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/terrails/yacu/types/docker"
)

// implements the calls a dependant makes while starting
type stubAPI struct {
	docker.API
	dependency container.InspectResponse
	inspected  []string
	started    []string
}

func (s *stubAPI) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	s.inspected = append(s.inspected, containerID)
	return s.dependency, nil
}

func (s *stubAPI) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	s.started = append(s.started, containerID)
	return nil
}

func fastPolling(t *testing.T) {
	previousTimeout, previousInterval := dependencyTimeout, dependencyPollInterval
	dependencyTimeout, dependencyPollInterval = time.Second, time.Millisecond
	t.Cleanup(func() { dependencyTimeout, dependencyPollInterval = previousTimeout, previousInterval })
}

func newStub(health *container.Health) *stubAPI {
	return &stubAPI{
		dependency: container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{
				ID:    "new-db",
				Name:  "/db",
				State: &container.State{Status: container.StateRunning, Health: health},
			},
		},
	}
}

func TestDependantStartsWhenDependencyHasNoHealthcheck(t *testing.T) {
	fastPolling(t)
	api := newStub(nil)
	dependant := NewDependant(&container.Summary{ID: "web", Names: []string{"/web"}}, 1, "db", DEPENDENCY_HEALTHY)

	if err := dependant.Start(context.Background(), api, "new-db"); err != nil {
		t.Fatal(err)
	}
	if len(api.inspected) == 0 || api.inspected[0] != "new-db" {
		t.Fatalf("expected the recreated dependency to be inspected, inspected %v", api.inspected)
	}
	if len(api.started) != 1 || api.started[0] != "web" {
		t.Fatalf("expected web to be started, started %v", api.started)
	}
}

func TestDependantIsNotStartedWhenDependencyIsUnhealthy(t *testing.T) {
	fastPolling(t)
	api := newStub(&container.Health{Status: container.Unhealthy})
	dependant := NewDependant(&container.Summary{ID: "web", Names: []string{"/web"}}, 1, "db", DEPENDENCY_HEALTHY)

	if err := dependant.Start(context.Background(), api, "new-db"); err == nil {
		t.Fatal("expected an error for an unhealthy dependency")
	}
	if len(api.started) != 0 {
		t.Fatalf("expected nothing to be started, started %v", api.started)
	}
}
