package webhooks

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/webhook"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/internal/config"
	"github.com/terrails/yacu/internal/container"
	"github.com/terrails/yacu/internal/image"
	"github.com/terrails/yacu/internal/utils"
)

type DiscordWebhook struct {
	config *config.Webhook
	client *webhook.Client
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

// Discord rejects embeds with longer texts, counted in characters
const (
	titleLimit       = 256
	descriptionLimit = 4096
	// what a single warning in the list of an update is shortened to
	warningLimit = 1024
)

// shortens text to at most limit characters, marking the cut with an ellipsis
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// context in bold and err as a code block below, with err shortened so that the whole fits
func errorDescription(context string, err error) string {
	heading := ""
	if len(context) > 0 {
		heading = "**" + truncate(context, titleLimit) + "**\n"
	}
	return heading + "```" + truncate(fmt.Sprint(err), descriptionLimit-utf8.RuneCountInString(heading)-6) + "```"
}

// the warnings of an update as a list, as many as fit
func warningsDescription(warnings []string) string {
	description := "__Following errors occurred during update:__\n"
	for i, warning := range warnings {
		line := "* " + truncate(warning, warningLimit) + "\n"
		// room for the note on the warnings left out
		remaining := fmt.Sprintf("…and %d more", len(warnings)-i)
		if utf8.RuneCountInString(description+line) > descriptionLimit-utf8.RuneCountInString(remaining) {
			return description + remaining
		}
		description += line
	}
	return description
}

// only a well formed http(s) URL, as Discord rejects the whole message otherwise.
// Unraid's WebUI labels are templates like http://[IP]:[PORT:8080]/, which are not
func linkURL(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || len(parsed.Host) == 0 {
		return "", false
	}
	return value, true
}

func (hook *DiscordWebhook) send(ctx context.Context, embed discord.Embed) {
	if _, err := hook.client.CreateEmbeds([]discord.Embed{embed}); err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Encountered an error while sending a Discord Webhook")
	}
}

func (hook *DiscordWebhook) Error(ctx context.Context, context string, err error) {
	hook.send(ctx, hook.errorEmbed(context, err))
}

func (hook *DiscordWebhook) errorEmbed(context string, err error) discord.Embed {
	return hook.getStartingEmbedBuilder().
		WithTitle("An error occurred during update").
		WithDescription(errorDescription(context, err)).
		WithColor(12723739)
}

func (hook *DiscordWebhook) ImageUpdated(ctx context.Context, prevImage, newImage *image.ImageData) {
	hook.send(ctx, hook.imageUpdatedEmbed(prevImage, newImage))
}

func (hook *DiscordWebhook) imageUpdatedEmbed(prevImage, newImage *image.ImageData) discord.Embed {
	familiarNameTagged := utils.FamiliarTagged(newImage.Repository)
	shortId := utils.ShortId(newImage.ID)

	return hook.getStartingEmbedBuilder().
		WithTitle(truncate(fmt.Sprintf("%s (%s) has been updated", familiarNameTagged, shortId), titleLimit)).
		AddField("Previous Digest", prevImage.RepoDigest.Encoded(), false).
		AddField("New Digest", newImage.RepoDigest.Encoded(), false).
		WithColor(881812)
}

func (hook *DiscordWebhook) ImageError(ctx context.Context, image *image.ImageData, context string, err error) {
	hook.send(ctx, hook.imageErrorEmbed(image, context, err))
}

func (hook *DiscordWebhook) imageErrorEmbed(image *image.ImageData, context string, err error) discord.Embed {
	familiarNameTagged := utils.FamiliarTagged(image.Repository)
	shortId := utils.ShortId(image.ID)

	return hook.getStartingEmbedBuilder().
		WithTitle(truncate(fmt.Sprintf("%s (%s) threw an error during update", familiarNameTagged, shortId), titleLimit)).
		WithDescription(errorDescription(context, err)).
		WithColor(12723739)
}

func (hook *DiscordWebhook) ImageRemovalFailed(ctx context.Context, image *image.ImageData, err error) {
	hook.send(ctx, hook.imageRemovalFailedEmbed(image, err))
}

func (hook *DiscordWebhook) imageRemovalFailedEmbed(image *image.ImageData, err error) discord.Embed {
	familiarNameTagged := utils.FamiliarTagged(image.Repository)
	shortId := utils.ShortId(image.ID)

	return hook.getStartingEmbedBuilder().
		WithTitle(fmt.Sprintf("%s threw an error during removal", shortId)).
		WithDescription(errorDescription("", err)).
		AddField("Long ID", image.ID, false).
		AddField("Last Tag", familiarNameTagged, false).
		WithColor(12723739)
}

func (hook *DiscordWebhook) ContainerUpdated(ctx context.Context, prevContainer, newContainer *container.Container, warnings ...string) {
	hook.send(ctx, hook.containerUpdatedEmbed(newContainer, warnings))
}

func (hook *DiscordWebhook) containerUpdatedEmbed(newContainer *container.Container, warnings []string) discord.Embed {
	familiarNameTagged := utils.FamiliarTagged(newContainer.Repository)
	containerShortId := utils.ShortId(newContainer.ID)
	imageShortId := utils.ShortId(newContainer.Image.ID)

	embed := hook.getStartingEmbedBuilder().
		WithTitle(truncate(fmt.Sprintf("%s (%s) has been updated", strings.TrimPrefix(newContainer.Name, "/"), familiarNameTagged), titleLimit)).
		AddField("Container Id", containerShortId, true).
		AddField("Image Id", imageShortId, true).
		WithColor(2597142)

	if webui, ok := linkURL(newContainer.Labels["net.unraid.docker.webui"]); ok {
		embed = embed.WithURL(webui)
	}

	if len(warnings) > 0 {
		embed = embed.WithDescription(warningsDescription(warnings))
	}
	return embed
}

func (hook *DiscordWebhook) ContainerError(ctx context.Context, container *container.Container, context string, err error) {
	hook.send(ctx, hook.containerErrorEmbed(container, context, err))
}

func (hook *DiscordWebhook) containerErrorEmbed(container *container.Container, context string, err error) discord.Embed {
	familiarNameTagged := utils.FamiliarTagged(container.Repository)
	containerShortId := utils.ShortId(container.ID)
	imageShortId := utils.ShortId(container.Image.ID)

	return hook.getStartingEmbedBuilder().
		WithTitle(truncate(fmt.Sprintf("%s (%s) threw an error during update", strings.TrimPrefix(container.Name, "/"), familiarNameTagged), titleLimit)).
		WithDescription(errorDescription(context, err)).
		AddField("Container Id", containerShortId, true).
		AddField("Image Id", imageShortId, true).
		WithColor(12723739)
}

func (hook *DiscordWebhook) getStartingEmbedBuilder() discord.Embed {
	builder := discord.NewEmbed()
	builder = builder.WithTimestamp(time.Now().UTC())
	builder = builder.WithFooterText("YACU by Terrails")

	if len(hook.config.Author.Name) != 0 {
		builder = builder.WithAuthorName(hook.config.Author.Name)
	}
	if len(hook.config.Author.Url) != 0 {
		builder = builder.WithAuthorURL(hook.config.Author.Url)
	}
	if len(hook.config.Author.IconUrl) != 0 {
		builder = builder.WithAuthorIcon(hook.config.Author.IconUrl)
	}
	return builder
}
