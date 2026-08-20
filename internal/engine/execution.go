package engine

import (
	"context"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
)

// executionModule owns engineer-visible coding dispatch and work-item lifecycle.
type executionModule struct{ engine *Engine }

func (e *Engine) execution() executionModule { return executionModule{engine: e} }

func (m executionModule) control(ctx context.Context, event webhook.ControlNote) error {
	return m.engine.handleControlCommand(ctx, event)
}

func (m executionModule) lifecycle(ctx context.Context, event webhook.LifecycleEvent) error {
	return m.engine.handleLifecycleEvent(ctx, event)
}
