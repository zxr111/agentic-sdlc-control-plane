package engine

import (
	"context"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
)

// qualityModule owns Engineer Gate commands and exact-evidence decisions.
type qualityModule struct{ engine *Engine }

func (e *Engine) quality() qualityModule { return qualityModule{engine: e} }

func (m qualityModule) gateCommand(ctx context.Context, event webhook.GateNote) error {
	return m.engine.handleGateCommand(ctx, event)
}
