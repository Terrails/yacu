package webhooks

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/disgoorg/disgo/discord"
	"github.com/distribution/reference"
	"github.com/terrails/yacu/internal/config"
	"github.com/terrails/yacu/internal/container"
	"github.com/terrails/yacu/internal/image"
)

func TestStartingEmbedHasAuthorFooterAndTimestamp(t *testing.T) {
	hook := &DiscordWebhook{config: &config.Webhook{
		Author: config.WebhookAuthor{Name: "server", Url: "https://server.lan", IconUrl: "https://server.lan/icon.png"},
	}}

	embed := hook.getStartingEmbedBuilder()

	if embed.Timestamp == nil {
		t.Error("timestamp is missing")
	}
	if embed.Footer == nil || embed.Footer.Text != "YACU by Terrails" {
		t.Errorf("footer is %+v", embed.Footer)
	}
	if embed.Author == nil || embed.Author.Name != "server" || embed.Author.URL != "https://server.lan" || embed.Author.IconURL != "https://server.lan/icon.png" {
		t.Errorf("author is %+v", embed.Author)
	}
}

func testContainer(t *testing.T, labels map[string]string) *container.Container {
	t.Helper()
	named, err := reference.ParseNormalizedNamed("linuxserver/sonarr:latest")
	if err != nil {
		t.Fatal(err)
	}
	return &container.Container{
		ID:         strings.Repeat("ab", 32),
		Name:       "/sonarr",
		Labels:     labels,
		Repository: named.(reference.NamedTagged),
		Image:      &image.ImageData{ID: "sha256:" + strings.Repeat("cd", 32)},
	}
}

func testHook() *DiscordWebhook {
	return &DiscordWebhook{config: &config.Webhook{}}
}

func TestContainerUpdatedEmbed(t *testing.T) {
	embed := testHook().containerUpdatedEmbed(testContainer(t, map[string]string{"net.unraid.docker.webui": "http://192.168.1.10:8989/"}), nil)

	if embed.Title != "sonarr (linuxserver/sonarr:latest) has been updated" {
		t.Errorf("title is %q", embed.Title)
	}
	if embed.URL != "http://192.168.1.10:8989/" {
		t.Errorf("url is %q, want the web UI", embed.URL)
	}
	if len(embed.Fields) != 2 || embed.Fields[0].Value != strings.Repeat("ab", 6) || embed.Fields[1].Value != strings.Repeat("cd", 6) {
		t.Errorf("fields are %+v, want the short container and image IDs", embed.Fields)
	}
	if len(embed.Description) > 0 {
		t.Errorf("description is %q without warnings", embed.Description)
	}
}

func TestContainerUpdatedEmbedSkipsUnraidTemplateURL(t *testing.T) {
	// Discord rejects the whole message with a URL that is not well formed
	embed := testHook().containerUpdatedEmbed(testContainer(t, map[string]string{"net.unraid.docker.webui": "http://[IP]:[PORT:8989]/"}), nil)

	if len(embed.URL) > 0 {
		t.Fatalf("url is %q, want none for a template", embed.URL)
	}
}

func TestContainerUpdatedEmbedListsWarnings(t *testing.T) {
	embed := testHook().containerUpdatedEmbed(testContainer(t, nil), []string{"network x unavailable", "dependant y left stopped"})

	want := "__Following errors occurred during update:__\n* network x unavailable\n* dependant y left stopped\n"
	if embed.Description != want {
		t.Fatalf("description is %q, want %q", embed.Description, want)
	}
}

func TestWarningsDescriptionFitsDiscordLimit(t *testing.T) {
	warnings := make([]string, 100)
	for i := range warnings {
		warnings[i] = strings.Repeat("w", 2000)
	}

	description := warningsDescription(warnings)

	if length := utf8.RuneCountInString(description); length > descriptionLimit {
		t.Fatalf("description is %d characters, Discord allows %d", length, descriptionLimit)
	}
	listed := strings.Count(description, "\n* ")
	if !strings.HasSuffix(description, fmt.Sprintf("…and %d more", len(warnings)-listed)) {
		t.Fatalf("description does not note the %d warnings left out: ...%q", len(warnings)-listed, description[len(description)-40:])
	}
}

func TestErrorEmbedsFitDiscordLimits(t *testing.T) {
	longErr := errors.New(strings.Repeat("e", 10000))
	cnt := testContainer(t, nil)
	cnt.Name = "/" + strings.Repeat("n", 300)
	cnt.Image.Repository = cnt.Repository

	embeds := map[string]discord.Embed{
		"error":           testHook().errorEmbed("Unable to fetch updates", longErr),
		"container error": testHook().containerErrorEmbed(cnt, "Unable to start container", longErr),
		"image error":     testHook().imageErrorEmbed(cnt.Image, "Unable to pull image", longErr),
	}
	for name, embed := range embeds {
		t.Run(name, func(t *testing.T) {
			if length := utf8.RuneCountInString(embed.Title); length > titleLimit {
				t.Errorf("title is %d characters, Discord allows %d", length, titleLimit)
			}
			if length := utf8.RuneCountInString(embed.Description); length > descriptionLimit {
				t.Errorf("description is %d characters, Discord allows %d", length, descriptionLimit)
			}
			// the code block must still be closed after shortening the error
			if !strings.HasSuffix(embed.Description, "…```") {
				t.Errorf("description does not end with the shortened error's code block: ...%q", embed.Description[len(embed.Description)-10:])
			}
		})
	}
}

func TestErrorDescription(t *testing.T) {
	if got, want := errorDescription("Unable to pull image", errors.New("manifest unknown")), "**Unable to pull image**\n```manifest unknown```"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := errorDescription("", errors.New("image in use")), "```image in use```"; got != want {
		t.Errorf("without context got %q, want %q", got, want)
	}
}
