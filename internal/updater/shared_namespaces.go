package updater

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/rs/zerolog"
	yacucontainer "github.com/terrails/yacu/internal/container"
	yacuimage "github.com/terrails/yacu/internal/image"
	"github.com/terrails/yacu/internal/utils"
)

// A container joining the network, IPC or PID namespace of the container being
// updated (its parent), e.g. an app behind a VPN container.
//
// Such a container is set to `container:<parent>`. Compose writes the parent's ID
// there for `network_mode: service:<name>`, while `docker run --network
// container:<name>` keeps the name. The namespace goes away with the parent, so
// the child has to be stopped with it and afterwards join the replacement.
type namespaceChild struct {
	Raw     container.InspectResponse
	Running bool
	// the parent is referenced by ID, which cannot be changed on an existing
	// container, so the child has to be recreated to join the replacement.
	// A name is resolved again when the child starts, so restarting it is enough
	ByID bool
}

type namespaceChildren []*namespaceChild

func (children namespaceChildren) contains(id string) bool {
	return slices.ContainsFunc(children, func(child *namespaceChild) bool { return child.Raw.ID == id })
}

// the parent references of a host config's namespace modes, empty for modes not set to `container:<ref>`
func namespaceReferences(hostConfig *container.HostConfig) []string {
	return []string{hostConfig.NetworkMode.ConnectedContainer(), hostConfig.IpcMode.Container(), hostConfig.PidMode.Container()}
}

// whether ref names parent, resolved the way the daemon does: full ID, name, then ID prefix
func referencesParent(ref string, parent *container.InspectResponse) (matches, byID bool) {
	switch {
	case len(ref) == 0:
		return false, false
	case ref == parent.ID:
		return true, true
	case strings.TrimPrefix(ref, "/") == strings.TrimPrefix(parent.Name, "/"):
		return true, false
	case strings.HasPrefix(parent.ID, ref):
		return true, true
	}
	return false, false
}

// Whether the container joins another container's namespace.
func sharesNamespace(cnt *yacucontainer.Container) bool {
	return slices.ContainsFunc(namespaceReferences(cnt.Raw.HostConfig), func(ref string) bool { return len(ref) > 0 })
}

// Finds the containers joining a namespace of parent.
//
// The list data only includes the network mode, so containers are inspected when
// they share a network namespace or depend on parent. The latter covers compose
// services sharing only the IPC or PID namespace, as compose makes a service
// depend on the one whose namespace it shares.
func (app Yacu) GetNamespaceChildren(ctx context.Context, parent *container.InspectResponse, dependants yacucontainer.DependantContainers) (namespaceChildren, error) {
	logger := zerolog.Ctx(ctx)

	containers, err := app.Client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		logger.Err(err).Msg("ContainerList request failed")
		return nil, fmt.Errorf("listing containers failed: %w", err)
	}

	children := namespaceChildren{}
	for _, summary := range containers {
		if summary.ID == parent.ID {
			continue
		}
		isDependant := slices.ContainsFunc(dependants, func(d *yacucontainer.DependantContainer) bool { return d.Data.ID == summary.ID })
		if !container.NetworkMode(summary.HostConfig.NetworkMode).IsContainer() && !isDependant {
			continue
		}

		data, err := app.Client.ContainerInspect(ctx, summary.ID)
		if err != nil {
			logger.Err(err).Str("id", summary.ID).Msg("ContainerInspect request failed")
			return nil, fmt.Errorf("inspecting container %s failed: %w", summary.ID, err)
		}

		shares, byID := false, false
		for _, ref := range namespaceReferences(data.HostConfig) {
			if matches, isID := referencesParent(ref, parent); matches {
				shares, byID = true, byID || isID
			}
		}
		if shares {
			children = append(children, &namespaceChild{Raw: data, Running: data.State.Running, ByID: byID})
		}
	}
	return children, nil
}

func (app Yacu) stopTimeout(labels map[string]string) *int {
	timeout := app.Updater.StopTimeout
	if value, err := strconv.Atoi(labels[yacucontainer.LABEL_STOP_TIMEOUT]); err == nil {
		timeout = value
	}
	return &timeout
}

// Stops the running children, returning warnings for those that failed to stop.
func (app Yacu) stopNamespaceChildren(ctx context.Context, children namespaceChildren) []string {
	warnings := []string{}
	for _, child := range children {
		if !child.Running {
			continue
		}
		if err := app.Client.ContainerStop(ctx, child.Raw.ID, container.StopOptions{Timeout: app.stopTimeout(child.Raw.Config.Labels)}); err != nil {
			zerolog.Ctx(ctx).Err(err).Str("child", child.Raw.Name).Msg("Failed to stop container sharing namespaces")
			warnings = append(warnings, fmt.Sprintf("failed to stop container %s sharing namespaces: %v", child.Raw.Name, err))
		}
	}
	return warnings
}

// Starts the children that were running again, as they were. Used when the
// parent's update is rolled back and the previous parent runs again.
func (app Yacu) startNamespaceChildren(ctx context.Context, children namespaceChildren) []error {
	var errs []error
	for _, child := range children {
		if !child.Running {
			continue
		}
		if err := app.Client.ContainerStart(ctx, child.Raw.ID, container.StartOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("failed to start container %s sharing namespaces: %w", child.Raw.Name, err))
		}
	}
	return errs
}

// Makes the children join the replacement (newParentID) of their parent and
// starts those that were running, returning warnings for those that could not.
func (app Yacu) reattachNamespaceChildren(ctx context.Context, children namespaceChildren, parent *container.InspectResponse, newParentID string) []string {
	logger := zerolog.Ctx(ctx)

	warnings := []string{}
	for _, child := range children {
		childLogger := logger.With().Str("child", child.Raw.Name).Logger()

		if !child.ByID {
			if child.Running {
				if err := app.Client.ContainerStart(ctx, child.Raw.ID, container.StartOptions{}); err != nil {
					childLogger.Err(err).Msg("Failed to start container sharing namespaces")
					warnings = append(warnings, fmt.Sprintf("failed to start container %s sharing namespaces: %v", child.Raw.Name, err))
				}
			}
			continue
		}

		childWarnings, err := app.recreateNamespaceChild(childLogger.WithContext(ctx), child, parent, newParentID)
		warnings = append(warnings, childWarnings...)
		if err != nil {
			childLogger.Err(err).Msg("Failed to recreate container sharing namespaces, it is left stopped")
			warnings = append(warnings, fmt.Sprintf("container %s sharing namespaces is left stopped as it could not be recreated to join the new container: %v", child.Raw.Name, err))
		} else {
			childLogger.Info().Msg("Recreated container sharing namespaces")
		}
	}
	return warnings
}

// Recreates the child from the image it runs, with its references to the parent's
// ID pointing to newParentID instead. The previous child is only removed once its
// replacement is in place, and kept (stopped) if anything fails.
func (app Yacu) recreateNamespaceChild(ctx context.Context, child *namespaceChild, parent *container.InspectResponse, newParentID string) ([]string, error) {
	logger := zerolog.Ctx(ctx)

	// the child is recreated from its tag, which may have been moved to another
	// image, e.g. pulled for another container. Using it would update the child
	// regardless of its own settings, so it is left to the user instead
	img, err := app.Client.ImageInspect(ctx, child.Raw.Config.Image)
	if err != nil {
		return nil, fmt.Errorf("inspecting image %s failed: %w", child.Raw.Config.Image, err)
	}
	if utils.IdEncoded(img.ID) != utils.IdEncoded(child.Raw.Image) {
		return nil, fmt.Errorf("%s now refers to a different image than the one the container runs, recreate it yourself (e.g. `docker compose up -d`)", child.Raw.Config.Image)
	}

	cnt := &yacucontainer.Container{Raw: &child.Raw, ID: child.Raw.ID, Name: child.Raw.Name, Image: &yacuimage.ImageData{Raw: &img, ID: img.ID}}
	config := cnt.CreateConfig()
	hostConfig := cnt.CreateHostConfig(img.Config)

	// references by name keep resolving to the replacement
	byID := func(ref string) bool {
		matches, byID := referencesParent(ref, parent)
		return matches && byID
	}
	if byID(hostConfig.NetworkMode.ConnectedContainer()) {
		hostConfig.NetworkMode = container.NetworkMode("container:" + newParentID)
	}
	if byID(hostConfig.IpcMode.Container()) {
		hostConfig.IpcMode = container.IpcMode("container:" + newParentID)
	}
	if byID(hostConfig.PidMode.Container()) {
		hostConfig.PidMode = container.PidMode("container:" + newParentID)
	}

	name := strings.TrimPrefix(child.Raw.Name, "/")
	oldName := name + oldContainerSuffix

	if err := app.Client.ContainerRename(ctx, child.Raw.ID, oldName); err != nil {
		return nil, fmt.Errorf("renaming container failed: %w", err)
	}
	renameBack := func() error {
		return app.Client.ContainerRename(ctx, child.Raw.ID, name)
	}

	response, err := app.Client.ContainerCreate(ctx, config, hostConfig, &network.NetworkingConfig{}, nil, name)
	if err != nil {
		if renameErr := renameBack(); renameErr != nil {
			logger.Err(renameErr).Msg("Renaming container back failed")
		}
		return nil, fmt.Errorf("creating container failed: %w", err)
	}

	warnings := []string{}
	if len(response.Warnings) > 0 {
		warnings = append(warnings, response.Warnings...)
	}

	if child.Running {
		if err := app.Client.ContainerStart(ctx, response.ID, container.StartOptions{}); err != nil {
			if removeErr := app.Client.ContainerRemove(ctx, response.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); removeErr != nil {
				logger.Err(removeErr).Msg("Removing new container failed")
			} else if renameErr := renameBack(); renameErr != nil {
				logger.Err(renameErr).Msg("Renaming container back failed")
			}
			return warnings, fmt.Errorf("starting container failed: %w", err)
		}
	}

	if err := app.Client.ContainerRemove(ctx, child.Raw.ID, container.RemoveOptions{Force: true, RemoveVolumes: app.Updater.RemoveVolumes}); err != nil {
		logger.Err(err).Str("name", oldName).Msg("Failed to remove previous container")
		warnings = append(warnings, fmt.Sprintf("removing previous container %s failed: %v", oldName, err))
	}
	return warnings, nil
}
