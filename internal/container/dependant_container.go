package container

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/internal/docker"
)

type DependantContainer struct {
	Data *container.Summary
	Name string // Data.Names[0]

	StopTimeout    int
	DependsOnName  string
	DependencyType DependencyType
}

type DependantContainers []*DependantContainer

var (
	dependencyTimeout      = time.Minute * 5
	dependencyPollInterval = time.Second * 10
)

func (c DependantContainers) Stop(ctx context.Context, api docker.API) []string {
	warnings := []string{}
	for _, container := range c {
		err := container.Stop(ctx, api)
		if err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings
}

// Starts every dependant once the container it depends on (identified by
// dependencyID, which changes when that container is recreated) satisfies the
// dependency condition. If ctx is cancelled (shutdown), waiting on the condition
// is abandoned and the dependants waiting on it are left stopped, as starting them
// before their dependency is ready could do more harm than leaving them off.
func (c DependantContainers) Start(ctx context.Context, api docker.API, dependencyID string) []string {
	warnings := []string{}
	for _, container := range c {
		err := container.Start(ctx, api, dependencyID)
		if err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings
}

func NewDependant(data *container.Summary, stopTimeout int, dependsOnName string, dependencyType DependencyType) *DependantContainer {
	if val, ok := data.Labels[LABEL_STOP_TIMEOUT]; ok {
		if ival, err := strconv.ParseInt(val, 10, 0); err == nil {
			stopTimeout = int(ival)
		}
	}

	return &DependantContainer{
		Data:           data,
		Name:           data.Names[0],
		StopTimeout:    stopTimeout,
		DependsOnName:  dependsOnName,
		DependencyType: dependencyType,
	}
}

func (c *DependantContainer) Stop(ctx context.Context, api docker.API) error {
	logger := c.logger(ctx)
	logger.Debug().Msg("Attempting to stop container")

	if err := api.ContainerStop(ctx, c.Data.ID, container.StopOptions{Timeout: &c.StopTimeout}); err != nil {
		logger.Err(err).Msg("Failed to stop container")
		return fmt.Errorf("failed to stop container %s: %w", c.Name, err)
	}
	logger.Debug().Msg("Stopped container")
	return nil
}

func (c *DependantContainer) Start(ctx context.Context, api docker.API, dependencyID string) error {
	logger := c.logger(ctx)
	logger.Debug().Msg("Attempting to start container")

	// only the waiting is cut short by shutdown, a start that is due must still happen
	callCtx := context.WithoutCancel(ctx)
	start := func() error {
		if err := api.ContainerStart(callCtx, c.Data.ID, container.StartOptions{}); err != nil {
			logger.Err(err).Msg("Failed to start container")
			return fmt.Errorf("failed to start container %s depending on %s: %w", c.Name, c.DependsOnName, err)
		}
		return nil
	}
	leaveStoppedOnShutdown := func() error {
		logger.Warn().Msg("Shutting down, leaving container stopped as depends_on is not satisfied yet")
		return fmt.Errorf("container %s left stopped due to shutdown before %s satisfied %s", c.Name, c.DependsOnName, c.DependencyType)
	}

	switch c.DependencyType {
	case DEPENDENCY_STARTED:
		return start()
	case DEPENDENCY_COMPLETED:
		waitCtx, cancelWait := context.WithCancel(callCtx)
		defer cancelWait()
		respCh, errCh := api.ContainerWait(waitCtx, dependencyID, container.WaitConditionNotRunning)

		// Better to limit it to not keep the app waiting
		timer := time.NewTimer(dependencyTimeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return leaveStoppedOnShutdown()
		case <-timer.C:
			logger.Warn().Msg("Timed out starting container due to depends_on not exitting in a reasonable amount of time")
			return fmt.Errorf("timed out starting container %s due to %s not exitting in a reasonable amount of time", c.Name, c.DependsOnName)
		case err := <-errCh:
			logger.Err(err).Msg("An error occurred while sending or receiving a ContainerWait request")
			return fmt.Errorf("an error occurred while sending or receiving a ContainerWait request for %s: %w", c.DependsOnName, err)
		case resp := <-respCh:
			if resp.Error != nil {
				err := errors.New(resp.Error.Message)
				logger.Err(err).Msg("Received an error from ContainerWait request")
				return fmt.Errorf("received an error from ContainerWait request for %s: %w", c.DependsOnName, err)
			}

			var warning error = nil
			if resp.StatusCode != 0 {
				logger.Warn().Int64("exit_code", resp.StatusCode).Msg("depends_on container exit code not clean")
				warning = fmt.Errorf("container %s exit code not clean: %d", c.DependsOnName, resp.StatusCode)
			}

			if err := start(); err != nil {
				return err
			}
			return warning
		}

	default:
		// Better to limit it to not keep the app waiting
		timer := time.NewTimer(dependencyTimeout)
		// Recheck container status periodically
		ticker := time.NewTicker(dependencyPollInterval)
		defer timer.Stop()
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return leaveStoppedOnShutdown()
			case <-timer.C:
				logger.Warn().Msg("Timed out starting container due to depends_on not starting or becoming healthy in a reasonable amount of time.")
				return fmt.Errorf("timed out starting container %s due to %s not starting or becoming healthy in a reasonable amount of time", c.Name, c.DependsOnName)
			case <-ticker.C:
				logger.Debug().Msg("Waiting on depending container to start or become healthy")
				cnt, err_ := api.ContainerInspect(callCtx, dependencyID)

				if err_ != nil {
					logger.Err(err_).Msg("Failed to start container due to an error from ContainerInspect")
					return fmt.Errorf("failed to start container %s due to an error from ContainerInspect: %w", c.Name, err_)
				}

				// Health is nil when the image defines no healthcheck
				health := container.NoHealthcheck
				if cnt.State != nil && cnt.State.Health != nil {
					health = cnt.State.Health.Status
				}

				switch health {
				case container.NoHealthcheck, container.Healthy:
					return start()
				case container.Unhealthy:
					logger.Warn().Msg("failed to start container because depends_on became unhealthy")
					return fmt.Errorf("failed to start container %s because %s became unhealthy", c.Name, cnt.Name)
				}
			}
		}
	}
}

func (c *DependantContainer) logger(ctx context.Context) *zerolog.Logger {
	logger := zerolog.Ctx(ctx).With().
		Str("container", c.Name).
		Str("dependency_type", string(c.DependencyType)).
		Str("depends_on", c.DependsOnName).
		Logger()
	return &logger
}
