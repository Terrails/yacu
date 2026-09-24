package docker

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// the daemon's stop timeout when neither the request nor the container sets one
const defaultStopTimeout = 10 * time.Second

// Bounds every call to api by timeout so that an unresponsive daemon cannot stall yacu.
// ContainerStop is additionally given the time the daemon waits before killing the container.
// ImagePull and ContainerWait are passed through untouched, as they legitimately take long
// and their callers bound them.
func WithTimeouts(api API, timeout time.Duration) API {
	return timeoutAPI{API: api, timeout: timeout}
}

type timeoutAPI struct {
	API
	timeout time.Duration
}

func (t timeoutAPI) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ContainerList(ctx, options)
}

func (t timeoutAPI) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ContainerInspect(ctx, containerID)
}

func (t timeoutAPI) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	timeout := t.timeout + defaultStopTimeout
	if options.Timeout != nil {
		if *options.Timeout < 0 {
			// the daemon waits indefinitely for the container to exit
			return t.API.ContainerStop(ctx, containerID, options)
		}
		timeout = t.timeout + time.Duration(*options.Timeout)*time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return t.API.ContainerStop(ctx, containerID, options)
}

func (t timeoutAPI) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ContainerStart(ctx, containerID, options)
}

func (t timeoutAPI) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ContainerRemove(ctx, containerID, options)
}

func (t timeoutAPI) ContainerRename(ctx context.Context, containerID, newContainerName string) error {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ContainerRename(ctx, containerID, newContainerName)
}

func (t timeoutAPI) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ContainerCreate(ctx, config, hostConfig, networkingConfig, platform, containerName)
}

func (t timeoutAPI) NetworkConnect(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.NetworkConnect(ctx, networkID, containerID, config)
}

func (t timeoutAPI) ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (image.InspectResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ImageInspect(ctx, imageID, inspectOpts...)
}

func (t timeoutAPI) ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.API.ImageRemove(ctx, imageID, options)
}
