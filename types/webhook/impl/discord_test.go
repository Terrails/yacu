package webhooks

import (
	"testing"

	"github.com/terrails/yacu/types/config"
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
