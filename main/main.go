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
	"github.com/terrails/yacu/internal/config"
	"github.com/terrails/yacu/internal/docker"
	"github.com/terrails/yacu/internal/updater"
	"github.com/terrails/yacu/internal/utils"
	"github.com/terrails/yacu/internal/webhook"
	webhooks "github.com/terrails/yacu/internal/webhook/impl"
)

// how long a single Docker Engine API call may take
const dockerCallTimeout = 2 * time.Minute

func main() {
	os.Exit(run())
}

// runs yacu until shut down, returning the exit code. Separate from main so that
// deferred cleanup runs before exiting
func run() int {
	configPathPtr := flag.String("config", "yacu.yaml", "Path to config file. By default checks for 'yacu.yaml' in current directory.")
	oncePtr := flag.Bool("once", false, "Check for updates and apply them once, then exit instead of running on scanner.interval. Exits with 1 if anything failed.")
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

	yacu := updater.Yacu{
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
				yacu.Webhooks.Append(discordHook, &val.Kind)
				logger.Debug().Msg("discord webhook client initialized")
			}
		}
	}

	logger.Info().Msg("initialization completed")

	if *oncePtr {
		failures := yacu.Run(ctx)
		if ctx.Err() != nil {
			<-shutdownLogged
			logger.Info().Msg("shut down")
			return 1
		}
		if failures > 0 {
			logger.Error().Int("failures", failures).Msg("some containers could not be checked or updated")
			return 1
		}
		return 0
	}

	if config.Scanner.RunOnStart {
		logger.Info().Msg("checking for updates on start (scanner.run_on_start)")
		yacu.Run(ctx)
	}

	for {
		nextTime, err := gronx.NextTick(config.Scanner.Interval, false)

		if err != nil {
			// it fired at startup validation, so this was its last run, e.g. a year given in it has passed
			logger.Error().Err(err).Str("interval", config.Scanner.Interval).Msg("scanner.interval does not fire anymore, exiting")
			return 1
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
	return 0
}
