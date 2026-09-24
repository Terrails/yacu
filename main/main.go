package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adhocore/gronx"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
	"github.com/terrails/yacu/types/config"
	"github.com/terrails/yacu/types/docker"
	"github.com/terrails/yacu/types/webhook"
	webhooks "github.com/terrails/yacu/types/webhook/impl"
	"github.com/terrails/yacu/utils"
)

// how long a single Docker Engine API call may take
const dockerCallTimeout = 2 * time.Minute

func main() {
	configPathPtr := flag.String("config", "yacu.yaml", "Path to config file. By default checks for 'yacu.yaml' in current directory.")
	flag.Parse()

	config, err := config.LoadConfig(*configPathPtr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to setup configuration.")
	}

	logger := config.Logging.CreateLogger()
	logger.Debug().Msg("logger initialized")

	// cancelled on SIGINT/SIGTERM, a second signal terminates immediately
	ctx, stop := signal.NotifyContext(logger.WithContext(context.Background()), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownLogged := make(chan struct{})
	context.AfterFunc(ctx, func() {
		stop()
		logger.Info().Msg("shutdown requested, finishing any container update in progress. Send the signal again to exit immediately")
		close(shutdownLogged)
	})

	client, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		logger.Fatal().Err(err).Msg("creating local docker engine client failed")
	}
	// set the API version to one server has
	client.NegotiateAPIVersion(ctx)
	logger.Debug().Msg("docker engine client initialized")

	database, err := config.Database.LoadDatabase(ctx)
	if err != nil {
		logger.Fatal().Err(err).Msg("loading local database failed")
	}
	defer database.Close()
	logger.Debug().Msg("local database initialized")

	yacu := Yacu{
		Client:     docker.WithTimeouts(client, dockerCallTimeout),
		Webhooks:   webhook.NewWebhookHandler(),
		DB:         *database,
		Scanner:    config.Scanner,
		Updater:    config.Updater,
		Registries: config.Registries,
	}

	if val, ok := config.Webhooks["discord"]; ok {
		if len(val.Url) > 0 {
			discordHook, err := webhooks.SetupDiscordWebhook(ctx, &val)
			if err != nil {
				logger.Err(err).Msg("setting up discord webhook client failed")
			} else {
				defVal := true
				if val.Kind.Errors == nil {
					val.Kind.Errors = &defVal
				}
				if val.Kind.ImageSuccess == nil {
					val.Kind.ImageSuccess = &defVal
				}
				if val.Kind.ContainerSuccess == nil {
					val.Kind.ContainerSuccess = &defVal
				}

				yacu.Webhooks.Append(discordHook, &val.Kind)
				logger.Debug().Msg("discord webhook client initialized")
			}
		}
	}

	logger.Info().Msg("initialization completed")

	for {
		nextTime, err := gronx.NextTick(config.Scanner.Interval, false)

		if err != nil {
			logger.Err(err).Msg("unknown error while calculating next run time")
			if utils.Sleep(ctx, time.Second*3) != nil {
				break
			}
			continue
		}

		timeRemaining := time.Until(nextTime)
		humanized := utils.HumanizeDuration(timeRemaining)

		logger.Info().Msg(fmt.Sprintf("next run time in %s.", humanized))

		if utils.Sleep(ctx, timeRemaining) != nil {
			break
		}

		yacu.Run(ctx)
	}

	// the loop only ends once shutdown was requested
	<-shutdownLogged
	logger.Info().Msg("shut down")
}
