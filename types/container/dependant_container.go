package container

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog"
)

type DependantContainer struct {
	Data *types.Container
	Name string // Data.Names[0]

	StopTimeout    int
	DependsOn      *types.ContainerJSON
	DependencyType DependencyType
}

type DependantContainers []*DependantContainer

func (c DependantContainers) Stop(ctx context.Context, client *client.Client) []string {
	warnings := []string{}
	for _, container := range c {
		err := container.Stop(ctx, client)
		if err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings
}

func (c DependantContainers) Start(ctx context.Context, client *client.Client) []string {
	warnings := []string{}
	for _, container := range c {
		err := container.Start(ctx, client)
		if err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings
}

func NewDependant(data *types.Container, stopTimeout int, dependsOn *types.ContainerJSON, dependencyType DependencyType) *DependantContainer {
	if val, ok := data.Labels[LABEL_STOP_TIMEOUT]; ok {
		if ival, err := strconv.ParseInt(val, 10, 0); err == nil {
			stopTimeout = int(ival)
		}
	}

	return &DependantContainer{
		Data:           data,
		Name:           data.Names[0],
		StopTimeout:    stopTimeout,
		DependsOn:      dependsOn,
		DependencyType: dependencyType,
	}
}

func (c *DependantContainer) Stop(ctx context.Context, client *client.Client) error {
	logger := c.logger(ctx)
	logger.Debug().Msg("Attempting to stop container")

	if err := client.ContainerStop(
		context.Background(),
		c.Data.ID,
		container.StopOptions{
			Timeout: &c.StopTimeout,
		},
	); err != nil {
		logger.Err(err).Msg("Failed to stop container")
		return fmt.Errorf("failed to stop container %s: %w", c.Name, err)
	}
	logger.Debug().Msg("Stopped container")
	return nil
}

func (c *DependantContainer) Start(ctx context.Context, client *client.Client) error {
	logger := c.logger(ctx)
	logger.Debug().Msg("Attempting to start container")

	switch c.DependencyType {
	case DEPENDENCY_STARTED:
		if err := client.ContainerStart(
			context.Background(),
			c.Data.ID,
			types.ContainerStartOptions{},
		); err != nil {
			logger.Err(err).Msg("Failed to start container")
			return fmt.Errorf("failed to start container %s depending on %s: %w", c.Name, c.DependsOn.Name, err)
		}
	case DEPENDENCY_COMPLETED:
		respCh, errCh := client.ContainerWait(
			context.Background(),
			c.DependsOn.ID,
			container.WaitConditionNotRunning,
		)

		// 300 seconds should be more than enough. Better to limit it to not keep the app waiting
		timer := time.NewTimer(time.Minute * 5)
		select {
		case <-timer.C:
			logger.Warn().Msg("Timed out starting container due to depends_on not exitting in a reasonable amount of time")
			return fmt.Errorf("timed out starting container %s due to %s not exitting in a reasonable amount of time", c.Name, c.DependsOn.Name)
		case err := <-errCh:
			timer.Stop()
			logger.Err(err).Msg("An error occurred while sending or receiving a ContainerWait request")
			return fmt.Errorf("an error occurred while sending or receiving a ContainerWait request for %s: %w", c.DependsOn.Name, err)
		case resp := <-respCh:
			timer.Stop()

			if resp.Error != nil {
				err := errors.New(resp.Error.Message)
				logger.Err(err).Msg("Received an error from ContainerWait request")
				return fmt.Errorf("received an error from ContainerWait request for %s: %w", c.DependsOn.Name, err)
			}

			var warning error = nil
			if resp.StatusCode != 0 {
				logger.Warn().Int64("exit_code", resp.StatusCode).Msg("depends_on container exit code not clean")
				warning = fmt.Errorf("container %s exit code not clean: %d", c.DependsOn.Name, resp.StatusCode)
			}

			if err := client.ContainerStart(context.Background(), c.Data.ID, types.ContainerStartOptions{}); err != nil {
				logger.Err(err).Msg("Failed to start container")
				return fmt.Errorf("failed to start container %s depending on %s: %w", c.Name, c.DependsOn.Name, err)
			}

			return warning
		}

	default:
		// 300 seconds should be more than enough. Better to limit it to not keep the app waiting
		timer := time.NewTimer(time.Minute * 5)
		// Recheck container status every 10 seconds
		ticker := time.NewTicker(time.Second * 10)

		for {
			select {
			case <-timer.C:
				ticker.Stop()
				logger.Warn().Msg("Timed out starting container due to depends_on not starting or becoming healthy in a reasonable amount of time.")
				return fmt.Errorf("timed out starting container %s due to %s not starting or becoming healthy in a reasonable amount of time", c.Name, c.DependsOn.Name)
			case <-ticker.C:
				logger.Debug().Msg("Waiting on depending container to start or become healthy")
				container, err_ := client.ContainerInspect(
					context.Background(),
					c.DependsOn.ID,
				)

				if err_ != nil {
					logger.Err(err_).Msg("Failed to start container due to an error from ContainerInspect")
					return fmt.Errorf("failed to start container %s due to an error from ContainerInspect: %w", c.Name, err_)
				}

				switch container.State.Health.Status {
				case types.NoHealthcheck, types.Healthy:
					timer.Stop()
					ticker.Stop()
					if err := client.ContainerStart(
						context.Background(),
						c.Data.ID,
						types.ContainerStartOptions{},
					); err != nil {
						logger.Err(err).Msg("Failed to start container")
						return fmt.Errorf("failed to start container %s: %w", c.Name, err)
					}
					return nil
				case types.Unhealthy:
					timer.Stop()
					ticker.Stop()
					logger.Warn().Msg("failed to start container because depends_on became unhealthy")
					return fmt.Errorf("failed to start container %s because %s became unhealthy", c.Name, container.Name)
				}
			}
		}
	}
	return nil
}

func (c *DependantContainer) logger(ctx context.Context) *zerolog.Logger {
	logger := zerolog.Ctx(ctx).With().
		Str("container", c.Name).
		Str("dependency_type", string(c.DependencyType)).
		Str("depends_on", c.DependsOn.Name).
		Logger()
	return &logger
}
