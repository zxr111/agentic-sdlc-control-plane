package engine

import (
	"context"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
)

// releaseModule owns staging, release approval, deployment, and observation.
type releaseModule struct{ engine *Engine }

func (e *Engine) release() releaseModule { return releaseModule{engine: e} }

func (m releaseModule) callback(ctx context.Context, event webhook.ExternalCallback) error {
	return m.engine.handleDeliveryCallback(ctx, event)
}
