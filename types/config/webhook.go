package config

type Webhooks map[string]Webhook

type Webhook struct {
	Url    string        `yaml:"url"`
	Author WebhookAuthor `yaml:"author"`
	Kind   WebhookKind   `yaml:"kind"`
}

type WebhookAuthor struct {
	Name    string `yaml:"name"`
	Url     string `yaml:"url"`
	IconUrl string `yaml:"icon_url"`
}

type WebhookKind struct {
	ImageSuccess     *bool `yaml:"image_success"`
	ContainerSuccess *bool `yaml:"container_success"`
	Errors           *bool `yaml:"errors"`
}
