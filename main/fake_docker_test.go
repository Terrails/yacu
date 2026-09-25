package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/terrails/yacu/types/docker"
)

type fakeDocker struct {
	mu sync.Mutex

	containers map[string]*container.InspectResponse // by ID
	images     map[string]image.InspectResponse      // by ID
	tags       map[string]string                     // reference -> image ID
	pulls      map[string]string                     // reference -> image ID the tag points to after a pull
	pullBody   map[string]string                     // reference -> pull progress stream

	// fail, when set, is consulted before every call; a non-nil result is returned as the call's error
	fail func(method, id string) error

	calls  []string
	nextID int
}

var _ docker.API = (*fakeDocker)(nil)

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers: map[string]*container.InspectResponse{},
		images:     map[string]image.InspectResponse{},
		tags:       map[string]string{},
		pulls:      map[string]string{},
		pullBody:   map[string]string{},
	}
}

// turns "test/app:latest" into "docker.io/test/app:latest", the form yacu pulls and inspects by
func normalize(ref string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ref
	}
	return reference.TagNameOnly(named).String()
}

// registers an image and points ref at it. An empty repoDigest models a local build.
func (f *fakeDocker) addImage(ref, id, created, repoDigest string) {
	f.addUntaggedImage(id, created, repoDigest)
	f.tags[normalize(ref)] = id
}

func (f *fakeDocker) addUntaggedImage(id, created, repoDigest string) {
	img := image.InspectResponse{ID: id, Created: created}
	if len(repoDigest) > 0 {
		img.RepoDigests = []string{repoDigest}
	}
	f.images[id] = img
}

// makes a pull of ref succeed and move ref to the image newID
func (f *fakeDocker) pullResult(ref, newID string) {
	f.pulls[normalize(ref)] = newID
}

// makes a pull of ref report message inside the progress stream
func (f *fakeDocker) pullError(ref, message string) {
	f.pullBody[normalize(ref)] = `{"status":"Pulling from test"}` + "\n" +
		fmt.Sprintf(`{"errorDetail":{"message":%q},"error":%q}`, message, message) + "\n"
}

func (f *fakeDocker) addContainer(name, ref string, labels map[string]string, running bool) string {
	f.nextID++
	id := fmt.Sprintf("%064d", f.nextID)

	status := container.StateExited
	if running {
		status = container.StateRunning
	}

	f.containers[id] = &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:         id,
			Name:       "/" + name,
			Image:      f.tags[normalize(ref)],
			State:      &container.State{Status: status, Running: running},
			HostConfig: &container.HostConfig{},
		},
		Config: &container.Config{Image: ref, Labels: labels},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{"bridge": {}},
		},
	}
	return id
}

func (f *fakeDocker) byName(name string) *container.InspectResponse {
	for _, c := range f.containers {
		if c.Name == "/"+name {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) called(method, id string) int {
	count := 0
	for _, call := range f.calls {
		if call == method+" "+id {
			count++
		}
	}
	return count
}

// like the real client, a call made with a cancelled context fails
func (f *fakeDocker) record(ctx context.Context, method, id string) error {
	f.calls = append(f.calls, method+" "+id)
	if f.fail != nil {
		if err := f.fail(method, id); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (f *fakeDocker) lookup(idOrName string) (*container.InspectResponse, error) {
	if c, ok := f.containers[idOrName]; ok {
		return c, nil
	}
	if c := f.byName(strings.TrimPrefix(idOrName, "/")); c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("No such container: %s", idOrName)
}

func (f *fakeDocker) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ContainerList", ""); err != nil {
		return nil, err
	}

	var list []container.Summary
	for _, c := range f.containers {
		if !options.All && c.State.Status != container.StateRunning {
			continue
		}
		if statuses := options.Filters.Get("status"); len(statuses) > 0 && string(c.State.Status) != statuses[0] {
			continue
		}
		matches := true
		for _, filter := range options.Filters.Get("label") {
			key, value, hasValue := strings.Cut(filter, "=")
			if actual, ok := c.Config.Labels[key]; !ok || (hasValue && actual != value) {
				matches = false
			}
		}
		if !matches {
			continue
		}

		list = append(list, container.Summary{
			ID:      c.ID,
			Names:   []string{c.Name},
			Image:   c.Config.Image,
			ImageID: c.Image,
			Labels:  maps.Clone(c.Config.Labels),
			State:   c.State.Status,
		})
	}
	return list, nil
}

func (f *fakeDocker) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ContainerInspect", containerID); err != nil {
		return container.InspectResponse{}, err
	}

	c, err := f.lookup(containerID)
	if err != nil {
		return container.InspectResponse{}, err
	}
	base := *c.ContainerJSONBase
	state := *c.State
	base.State = &state
	return container.InspectResponse{ContainerJSONBase: &base, Mounts: c.Mounts, Config: c.Config, NetworkSettings: c.NetworkSettings}, nil
}

func (f *fakeDocker) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ContainerStop", containerID); err != nil {
		return err
	}

	c, err := f.lookup(containerID)
	if err != nil {
		return err
	}
	c.State.Status, c.State.Running = container.StateExited, false
	return nil
}

func (f *fakeDocker) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ContainerStart", containerID); err != nil {
		return err
	}

	c, err := f.lookup(containerID)
	if err != nil {
		return err
	}
	c.State.Status, c.State.Running = container.StateRunning, true
	return nil
}

func (f *fakeDocker) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ContainerRemove", containerID); err != nil {
		return err
	}

	c, err := f.lookup(containerID)
	if err != nil {
		return err
	}
	delete(f.containers, c.ID)
	return nil
}

func (f *fakeDocker) ContainerRename(ctx context.Context, containerID, newContainerName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ContainerRename", containerID); err != nil {
		return err
	}

	c, err := f.lookup(containerID)
	if err != nil {
		return err
	}
	if other := f.byName(newContainerName); other != nil && other != c {
		return fmt.Errorf("Conflict. The container name %q is already in use", "/"+newContainerName)
	}
	c.Name = "/" + newContainerName
	return nil
}

func (f *fakeDocker) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ContainerCreate", containerName); err != nil {
		return container.CreateResponse{}, err
	}

	if f.byName(containerName) != nil {
		return container.CreateResponse{}, fmt.Errorf("Conflict. The container name %q is already in use", "/"+containerName)
	}

	f.nextID++
	id := fmt.Sprintf("%064d", f.nextID)
	f.containers[id] = &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:         id,
			Name:       "/" + containerName,
			Image:      f.tags[normalize(config.Image)],
			State:      &container.State{Status: container.StateCreated},
			HostConfig: hostConfig,
		},
		Config: config,
		NetworkSettings: &container.NetworkSettings{
			Networks: maps.Clone(networkingConfig.EndpointsConfig),
		},
	}
	return container.CreateResponse{ID: id}, nil
}

func (f *fakeDocker) ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	respCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	respCh <- container.WaitResponse{StatusCode: 0}
	return respCh, errCh
}

func (f *fakeDocker) NetworkConnect(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "NetworkConnect", containerID); err != nil {
		return err
	}

	c, err := f.lookup(containerID)
	if err != nil {
		return err
	}
	if c.NetworkSettings.Networks == nil {
		c.NetworkSettings.Networks = map[string]*network.EndpointSettings{}
	}
	c.NetworkSettings.Networks[networkID] = config
	return nil
}

func (f *fakeDocker) ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ImagePull", refStr); err != nil {
		return nil, err
	}

	ref := normalize(refStr)
	body, ok := f.pullBody[ref]
	if !ok {
		body = `{"status":"Pulling from test"}` + "\n" + `{"status":"Status: Downloaded newer image"}` + "\n"
		if id, ok := f.pulls[ref]; ok {
			f.tags[ref] = id
		}
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func (f *fakeDocker) ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (image.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ImageInspect", imageID); err != nil {
		return image.InspectResponse{}, err
	}

	// image IDs are looked up first, "sha256:<hex>" would otherwise parse as a reference
	if _, ok := f.images[imageID]; !ok {
		if id, ok := f.tags[normalize(imageID)]; ok {
			imageID = id
		}
	}
	img, ok := f.images[imageID]
	if !ok {
		return image.InspectResponse{}, fmt.Errorf("No such image: %s", imageID)
	}
	return img, nil
}

func (f *fakeDocker) ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(ctx, "ImageRemove", imageID); err != nil {
		return nil, err
	}

	if _, ok := f.images[imageID]; !ok {
		return nil, fmt.Errorf("No such image: %s", imageID)
	}
	delete(f.images, imageID)
	return []image.DeleteResponse{{Deleted: imageID}}, nil
}
