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

// Enables the kinds of notifications a webhook does not set.
func (w Webhooks) applyDefaults() {
	enabled := func() *bool {
		value := true
		return &value
	}
	for name, hook := range w {
		if hook.Kind.ImageSuccess == nil {
			hook.Kind.ImageSuccess = enabled()
		}
		if hook.Kind.ContainerSuccess == nil {
			hook.Kind.ContainerSuccess = enabled()
		}
		if hook.Kind.Errors == nil {
			hook.Kind.Errors = enabled()
		}
		w[name] = hook
	}
}
