package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/adhocore/gronx"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
	"github.com/terrails/yacu/types/config"
	"github.com/terrails/yacu/types/webhook"
	webhooks "github.com/terrails/yacu/types/webhook/impl"
	"github.com/terrails/yacu/utils"
)

func main() {
	configPathPtr := flag.String("config", "yacu.yaml", "Path to config file. By default checks for 'yacu.yaml' in current directory.")
	flag.Parse()

	config := config.GetDefaultConfig()
	if err := config.ReadConfigIfFound(*configPathPtr); err != nil {
		log.Fatal().Err(err).Msg("failed to setup configuration.")
	}

	logger := config.Logging.CreateLogger()
	logger.Debug().Msg("logger initialized")

	logger.Debug().Str("config", fmt.Sprintf("%v", *config)).Msg("config loaded")

	ctx := logger.WithContext(context.Background())

	client, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		logger.Fatal().Err(err).Msg("creating local docker engine client failed")
	}
	// set the API version to one server has
	client.NegotiateAPIVersion(context.Background())
	logger.Debug().Msg("docker engine client initialized")

	database, err := config.Database.LoadDatabase(ctx)
	if err != nil {
		logger.Fatal().Msg("loading local database failed")
	}
	logger.Debug().Msg("local database initialized")

	yacu := Yacu{
		Client:     client,
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

	if !config.Scanner.IsIntervalValid() {
		logger.Fatal().Str("interval", config.Scanner.Interval).Msg("invalid cron format")
	}

	logger.Info().Msg("initialization completed")

	for {
		nextTime, err := gronx.NextTick(config.Scanner.Interval, false)

		if err != nil {
			logger.Err(err).Msg("unknown error while calculating next run time")
			time.Sleep(time.Second * 3)
			continue
		}

		timeRemaining := time.Until(nextTime)
		humanized := utils.HumanizeDuration(timeRemaining)

		logger.Info().Msg(fmt.Sprintf("next run time in %s.", humanized))

		time.Sleep(timeRemaining)

		yacu.Run(ctx)
	}
}
