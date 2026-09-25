package updater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/internal/utils"

	yacucontainer "github.com/terrails/yacu/internal/container"
)

// suffix given to a container while its replacement is being created
const oldContainerSuffix = "-yacu-old"

// Recreates cnt from its (already pulled) image.
//
// The old container is renamed rather than removed until its replacement has
// been created and started, so that any failure can be rolled back: the
// replacement is removed, the old container gets its name back and it (and
// any stopped dependants) is started again.
//
// Containers joining a namespace of cnt (network_mode container:<cnt>) are
// stopped with it and made to join the replacement.
//
// Once started, an update is not interrupted by shutdown (ctx cancelled) so
// that it always ends committed or rolled back. Only waiting on the conditions
// of dependants is cut short, those still waiting are left stopped.
func (app Yacu) UpdateContainer(ctx context.Context, cnt *yacucontainer.Container) (*yacucontainer.Container, []string, error) {
	logger := zerolog.Ctx(ctx)

	shutdownCtx := ctx
	ctx = context.WithoutCancel(ctx)

	warnings := []string{}
	shouldRestart := cnt.IsRunning()
	var dependantContainers yacucontainer.DependantContainers
	var children namespaceChildren

	// fail undoes the steps taken so far (most recent first), restarts the old
	// container and its dependants if they were running, and returns the error
	fail := func(context string, err error, undo ...func() error) error {
		var rollbackErrs []error
		for _, fn := range undo {
			if undoErr := fn(); undoErr != nil {
				rollbackErrs = append(rollbackErrs, undoErr)
			}
		}

		if shouldRestart {
			if startErr := cnt.Start(ctx, app.Client); startErr != nil {
				rollbackErrs = append(rollbackErrs, startErr)
			} else {
				rollbackErrs = append(rollbackErrs, app.startNamespaceChildren(ctx, children)...)
				for _, warning := range dependantContainers.Start(shutdownCtx, app.Client, cnt.ID) {
					rollbackErrs = append(rollbackErrs, errors.New(warning))
				}
			}
		}

		if len(rollbackErrs) > 0 {
			logger.Error().Errs("errors", rollbackErrs).Msg("Rolling back container update failed")
			err = errors.Join(err, fmt.Errorf("rollback failed: %w", errors.Join(rollbackErrs...)))
		} else {
			logger.Info().Msg("Rolled back container update")
		}
		return &updateError{Context: context, Err: err}
	}

	// what the replacement is created from, already pulled under the container's tag
	newImage, err := app.Client.ImageInspect(ctx, cnt.Repository.String())
	if err != nil {
		logger.Err(err).Msg("ImageInspect request failed")
		return nil, nil, &updateError{Context: "Unable to inspect image", Err: err}
	}

	if shouldRestart {
		dependantContainers, err = app.GetDependingContainers(ctx, cnt.Raw)
		if err != nil {
			return nil, nil, &updateError{Context: "Unable to fetch depending containers", Err: err}
		}
	}

	// needed even if cnt is not running, as children referencing it by ID could never start again otherwise
	children, err = app.GetNamespaceChildren(ctx, cnt.Raw, dependantContainers)
	if err != nil {
		return nil, nil, &updateError{Context: "Unable to fetch containers sharing namespaces", Err: err}
	}
	// compose makes a child depend on its parent, but it is handled as a child
	dependantContainers = slices.DeleteFunc(dependantContainers, func(d *yacucontainer.DependantContainer) bool {
		return children.contains(d.Data.ID)
	})

	if shouldRestart {
		stopWarnings := dependantContainers.Stop(ctx, app.Client)
		stopWarnings = append(stopWarnings, app.stopNamespaceChildren(ctx, children)...)
		if len(stopWarnings) > 0 {
			warnings = append(warnings, stopWarnings...)
			logger.Warn().Strs("warnings", stopWarnings).Msg("Received warnings while stopping depending containers")
		}

		if err = cnt.Stop(ctx, app.Client); err != nil {
			return nil, nil, fail("Unable to stop container", err)
		}
	}

	name := strings.TrimPrefix(cnt.Name, "/")
	oldName := name + oldContainerSuffix

	logger.Debug().Str("name", oldName).Msg("Renaming container")
	if err := app.Client.ContainerRename(ctx, cnt.ID, oldName); err != nil {
		logger.Err(err).Msg("Failed to rename container")
		return nil, nil, fail("Unable to rename container", err)
	}
	renameBack := func() error {
		if err := app.Client.ContainerRename(ctx, cnt.ID, name); err != nil {
			return fmt.Errorf("renaming %s back to %s failed: %w", oldName, name, err)
		}
		return nil
	}

	endpoints := cnt.EndpointsConfig()

	var singleNetSettings network.NetworkingConfig = network.NetworkingConfig{}
	for netName, netSettings := range endpoints {
		singleNetSettings.EndpointsConfig = map[string]*network.EndpointSettings{
			netName: netSettings,
		}
		break
	}

	logger.Debug().Msg("Creating container")
	response, err := app.Client.ContainerCreate(ctx, cnt.CreateConfig(), cnt.CreateHostConfig(newImage.Config), &singleNetSettings, nil, name)
	if err != nil {
		logger.Err(err).Msg("Failed to create container")
		return nil, nil, fail("Unable to create container", err, renameBack)
	}

	newId := response.ID
	removeNew := func() error {
		// the replacement's anonymous volumes are fresh and unused
		if err := app.Client.ContainerRemove(ctx, newId, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			return fmt.Errorf("removing new container %s failed: %w", utils.ShortId(newId), err)
		}
		return nil
	}

	if len(response.Warnings) > 0 {
		warnings = append(warnings, response.Warnings...)
		logger.Warn().Strs("warnings", response.Warnings).Msg("Received warnings while creating container")
	}

	// cannot use multiple networks if host networking is enabled
	if !cnt.Raw.HostConfig.NetworkMode.IsHost() {

		// should be already connected to 1 network
		if len(endpoints) > 1 {
			logger.Debug().Msg("Connecting container to networks")
		}

		// Add other networks
		for netName, netSettings := range endpoints {

			// skip already connected
			if _, ok := singleNetSettings.EndpointsConfig[netName]; ok {
				continue
			}

			logger.Debug().Str("network", netName).Msg("Connecting container to network")

			if err := app.Client.NetworkConnect(ctx, netName, newId, netSettings); err != nil {
				logger.Err(err).Str("network", netName).Msg("Connecting to network failed")
				// since we already came this far, might as well do everything and send a warning about it
				warnings = append(warnings, fmt.Sprintf("connecting to network %s failed: %v", netName, err))
			}
		}
	}

	newData, err := app.Client.ContainerInspect(ctx, newId)
	if err != nil {
		logger.Err(err).Str("id", newId).Msg("ContainerInspect request failed")
		return nil, nil, fail("Unable to inspect container", err, removeNew, renameBack)
	}

	newContainer, err := yacucontainer.New(ctx, app.Client, &newData, app.Updater.StopTimeout, app.Scanner.ImageAge)
	if err != nil {
		logger.Err(err).Str("container", newData.Name).Msg("Initializing recreated container failed")
		return nil, nil, fail("Unable to initialize container", err, removeNew, renameBack)
	}

	if shouldRestart {
		if err = newContainer.Start(ctx, app.Client); err != nil {
			return nil, nil, fail("Unable to start container", err, removeNew, renameBack)
		}
	}

	// before the previous container is removed, which children may still reference
	if len(children) > 0 {
		logger.Debug().Int("count", len(children)).Msg("Moving containers sharing namespaces to the new container")
		childWarnings := app.reattachNamespaceChildren(ctx, children, cnt.Raw, newId)
		if len(childWarnings) > 0 {
			warnings = append(warnings, childWarnings...)
			logger.Warn().Strs("warnings", childWarnings).Msg("Received warnings while moving containers sharing namespaces")
		}
	}

	// the replacement is in place, the old container is no longer needed
	logger.Debug().Str("name", oldName).Msg("Removing previous container")
	if err := app.Client.ContainerRemove(ctx, cnt.ID,
		container.RemoveOptions{
			Force:         true,
			RemoveVolumes: app.Updater.RemoveVolumes,
		},
	); err != nil {
		logger.Err(err).Str("name", oldName).Msg("Failed to remove previous container")
		warnings = append(warnings, fmt.Sprintf("removing previous container %s failed: %v", oldName, err))
	}

	if shouldRestart {
		startWarnings := dependantContainers.Start(shutdownCtx, app.Client, newId)
		if len(startWarnings) > 0 {
			warnings = append(warnings, startWarnings...)
			logger.Warn().Strs("warnings", startWarnings).Msg("Received warnings while starting depending containers")
		}
	}

	return newContainer, warnings, nil
}

// Finds the running containers that declare a compose depends_on on the given container.
//
// Compose records dependencies by service name, so for compose containers the
// service name is matched within the same project or otherwise the container name is matched.
func (app Yacu) GetDependingContainers(ctx context.Context, dependsOn *container.InspectResponse) (yacucontainer.DependantContainers, error) {
	logger := zerolog.Ctx(ctx)
	logger.Debug().Msg("Fetching depending containers")

	target := strings.TrimPrefix(dependsOn.Name, "/")
	listFilters := filters.NewArgs(
		filters.KeyValuePair{
			Key: "label", Value: yacucontainer.LABEL_DEPENDS_ON,
		},
		filters.KeyValuePair{
			Key: "status", Value: "running",
		},
	)

	project := dependsOn.Config.Labels[yacucontainer.LABEL_PROJECT]
	service := dependsOn.Config.Labels[yacucontainer.LABEL_SERVICE]
	if len(project) > 0 && len(service) > 0 {
		target = service
		listFilters.Add("label", yacucontainer.LABEL_PROJECT+"="+project)
	}

	// list all containers with the compose label
	containers, err := app.Client.ContainerList(
		ctx,
		container.ListOptions{Filters: listFilters},
	)
	if err != nil {
		logger.Err(err).Msg("ContainerList request failed")
		return nil, fmt.Errorf("listing containers failed: %w", err)
	}

	dependantContainers := yacucontainer.DependantContainers{}
	for i := range containers {
		c := &containers[i]

		// no need to check if it exists as it is filtered that way
		val := c.Labels[yacucontainer.LABEL_DEPENDS_ON]
		// skip if value is empty
		if len(val) == 0 {
			continue
		}

		// format: service[:condition[:restart]],...
		for _, value := range strings.Split(val, ",") {
			depVals := strings.Split(value, ":")

			dependency := depVals[0]
			if dependency != target {
				continue
			}

			condition := yacucontainer.DEPENDENCY_HEALTHY

			if len(depVals) > 1 {
				condStr := strings.ToLower(depVals[1])
				if condStr == "service_started" {
					condition = yacucontainer.DEPENDENCY_STARTED
				} else if condStr == "service_completed_successfully" {
					condition = yacucontainer.DEPENDENCY_COMPLETED
				} // else DEPENDENCY_HEALTHY

				if len(depVals) > 2 {
					restart, err := strconv.ParseBool(depVals[2])
					// can recreate without restarting this container if false
					if err == nil && !restart {
						break
					}
				}
			}

			dependant := yacucontainer.NewDependant(c, app.Updater.StopTimeout, target, condition)
			dependantContainers = append(dependantContainers, dependant)
			// can be stopped as there cannot be multiple instances of the same dependency
			break
		}
	}
	return dependantContainers, nil
}
