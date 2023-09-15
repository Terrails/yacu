package webhooks

import (
	"context"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/webhook"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/types/config"
	"github.com/terrails/yacu/types/container"
	"github.com/terrails/yacu/types/image"
	"github.com/terrails/yacu/utils"
)

type DiscordWebhook struct {
	config *config.Webhook
	client webhook.Client
}

func SetupDiscordWebhook(ctx context.Context, config *config.Webhook) (*DiscordWebhook, error) {
	logger := zerolog.Ctx(ctx)
	client, err := webhook.NewWithURL(config.Url)
	if err != nil {
		logger.Err(err).Msg("failed to send discord webhook")
		return nil, err
	}
	w := DiscordWebhook{
		config: config,
		client: client,
	}
	return &w, nil
}

func (hook *DiscordWebhook) Error(ctx context.Context, context string, err error) {
	logger := zerolog.Ctx(ctx)

	if _, err := hook.client.CreateEmbeds([]discord.Embed{
		hook.getStartingEmbedBuilder().
			SetTitle("An error occurred during update").
			SetDescription(fmt.Sprintf("**%s**\n```%v```", context, err)).
			SetColor(12723739).
			Build(),
	},
	); err != nil {
		logger.Err(err).Msg("Encountered an error while sending a Discord Webhook")
	}
}

func (hook *DiscordWebhook) ImageUpdated(ctx context.Context, prevImage, newImage *image.ImageData) {
	logger := zerolog.Ctx(ctx)

	familiarNameTagged := utils.FamiliarTagged(newImage.Repository)
	shortId := utils.ShortId(newImage.ID)
	prevDigest := prevImage.RepoDigest.Encoded()
	newDigest := newImage.RepoDigest.Encoded()

	if _, err := hook.client.CreateEmbeds([]discord.Embed{
		hook.getStartingEmbedBuilder().
			SetTitle(fmt.Sprintf("%s (%s) has been updated", familiarNameTagged, shortId)).
			AddField("Previous Digest", prevDigest, false).
			AddField("New Digest", newDigest, false).
			SetColor(881812).
			Build(),
	},
	); err != nil {
		logger.Err(err).Msg("Encountered an error while sending a Discord Webhook")
	}
}

func (hook *DiscordWebhook) ImageError(ctx context.Context, image *image.ImageData, context string, err error) {
	logger := zerolog.Ctx(ctx)

	familiarNameTagged := utils.FamiliarTagged(image.Repository)
	shortId := utils.ShortId(image.ID)

	if _, err := hook.client.CreateEmbeds([]discord.Embed{
		hook.getStartingEmbedBuilder().
			SetTitle(fmt.Sprintf("%s (%s) threw an error during update", familiarNameTagged, shortId)).
			SetDescription(fmt.Sprintf("**%s**\n```%v```", context, err)).
			SetColor(12723739).
			Build(),
	},
	); err != nil {
		logger.Err(err).Msg("Encountered an error while sending a Discord Webhook")
	}
}

func (hook *DiscordWebhook) ImageRemovalFailed(ctx context.Context, image *image.ImageData, err error) {
	logger := zerolog.Ctx(ctx)

	familiarNameTagged := utils.FamiliarTagged(image.Repository)
	shortId := utils.ShortId(image.ID)

	if _, err := hook.client.CreateEmbeds([]discord.Embed{
		hook.getStartingEmbedBuilder().
			SetTitle(fmt.Sprintf("%s threw an error during removal", shortId)).
			SetDescription(fmt.Sprintf("```%v```", err)).
			AddField("Long ID", image.ID, false).
			AddField("Last Tag", familiarNameTagged, false).
			SetColor(12723739).
			Build(),
	},
	); err != nil {
		logger.Err(err).Msg("Encountered an error while sending a Discord Webhook")
	}
}

func (hook *DiscordWebhook) ContainerUpdated(ctx context.Context, prevContainer, newContainer *container.Container, warnings ...string) {
	logger := zerolog.Ctx(ctx)

	familiarNameTagged := utils.FamiliarTagged(newContainer.Repository)
	containerShortId := utils.ShortId(newContainer.ID)
	imageShortId := utils.ShortId(newContainer.Image.ID)

	embed := hook.getStartingEmbedBuilder().
		SetTitle(fmt.Sprintf("%s (%s) has been updated", newContainer.Name, familiarNameTagged)).
		AddField("Container Id", containerShortId, true).
		AddField("Image Id", imageShortId, true).
		SetColor(2597142)

	if webui, ok := newContainer.Labels["net.unraid.docker.webui"]; ok && len(webui) > 0 {
		embed.SetURL(webui)
	}

	if len(warnings) > 0 {
		description := "__Following errors occurred during update:\n"
		for _, value := range warnings {
			description += fmt.Sprintf("* %s\n", value)
		}
		embed.SetDescription(description)
	}

	if _, err := hook.client.CreateEmbeds([]discord.Embed{
		embed.Build(),
	},
	); err != nil {
		logger.Err(err).Msg("Encountered an error while sending a Discord Webhook")
	}
}

func (hook *DiscordWebhook) ContainerError(ctx context.Context, container *container.Container, context string, err error) {
	logger := zerolog.Ctx(ctx)

	familiarNameTagged := utils.FamiliarTagged(container.Repository)
	containerShortId := utils.ShortId(container.ID)
	imageShortId := utils.ShortId(container.Image.ID)

	if _, err := hook.client.CreateEmbeds([]discord.Embed{
		hook.getStartingEmbedBuilder().
			SetTitle(fmt.Sprintf("%s (%s) threw an error during update", container.Name, familiarNameTagged)).
			SetDescription(fmt.Sprintf("**%s**\n```%v```", context, err)).
			AddField("Container Id", containerShortId, true).
			AddField("Image Id", imageShortId, true).
			SetColor(12723739).
			Build(),
	},
	); err != nil {
		logger.Err(err).Msg("Encountered an error while sending a Discord Webhook")
	}
}

func (hook *DiscordWebhook) getStartingEmbedBuilder() *discord.EmbedBuilder {
	builder := discord.NewEmbedBuilder()
	builder.SetTimestamp(time.Now().UTC())
	builder.SetFooterText("YACU by Terrails")

	if len(hook.config.Author.Name) != 0 {
		builder.SetAuthorName(hook.config.Author.Name)
	}
	if len(hook.config.Author.Url) != 0 {
		builder.SetAuthorURL(hook.config.Author.Url)
	}
	if len(hook.config.Author.IconUrl) != 0 {
		builder.SetAuthorIcon(hook.config.Author.IconUrl)
	}
	return builder
}
