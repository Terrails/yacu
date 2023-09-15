package webhook

import (
	"context"

	"github.com/terrails/yacu/types/config"
	"github.com/terrails/yacu/types/container"
	"github.com/terrails/yacu/types/image"
)

type webhookFuncs interface {
	Error(ctx context.Context, context string, err error)

	ImageUpdated(ctx context.Context, prevImage, newImage *image.ImageData)
	ImageError(ctx context.Context, image *image.ImageData, context string, err error)
	ImageRemovalFailed(ctx context.Context, image *image.ImageData, err error)

	ContainerUpdated(ctx context.Context, prevContainer, newContainer *container.Container, warnings ...string)
	ContainerError(ctx context.Context, container *container.Container, context string, err error)
}

type webhook struct {
	funcs             webhookFuncs
	errors            bool
	image_success     bool
	container_success bool
}

type Webhooks struct {
	webhooks []webhook
}

func NewWebhookHandler() *Webhooks {
	return &Webhooks{
		webhooks: []webhook{},
	}
}

func (w *Webhooks) Append(hook webhookFuncs, config *config.WebhookKind) {
	w.webhooks = append(w.webhooks, webhook{
		funcs:             hook,
		errors:            *config.Errors,
		image_success:     *config.ImageSuccess,
		container_success: *config.ContainerSuccess,
	})
}

func (w *Webhooks) Error(ctx context.Context, context string, err error) {
	for _, hook := range w.webhooks {
		if hook.errors {
			hook.funcs.Error(ctx, context, err)
		}
	}
}

func (w *Webhooks) ImageUpdated(ctx context.Context, prevImage, newImage *image.ImageData) {
	for _, hook := range w.webhooks {
		if hook.image_success {
			hook.funcs.ImageUpdated(ctx, prevImage, newImage)
		}
	}
}

func (w *Webhooks) ImageError(ctx context.Context, image *image.ImageData, context string, err error) {
	for _, hook := range w.webhooks {
		if hook.errors {
			hook.funcs.ImageError(ctx, image, context, err)
		}
	}
}

func (w *Webhooks) ImageRemovalFailed(ctx context.Context, image *image.ImageData, err error) {
	for _, hook := range w.webhooks {
		if hook.errors {
			hook.funcs.ImageRemovalFailed(ctx, image, err)
		}
	}
}

func (w *Webhooks) ContainerUpdated(ctx context.Context, prevContainer, newContainer *container.Container, warnings ...string) {
	for _, hook := range w.webhooks {
		if hook.container_success {
			hook.funcs.ContainerUpdated(ctx, prevContainer, newContainer, warnings...)
		}
	}
}

func (w *Webhooks) ContainerError(ctx context.Context, container *container.Container, context string, err error) {
	for _, hook := range w.webhooks {
		if hook.errors {
			hook.funcs.ContainerError(ctx, container, context, err)
		}
	}
}
