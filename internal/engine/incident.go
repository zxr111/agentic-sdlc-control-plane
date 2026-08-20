package engine

import (
	"context"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
)

// incidentModule owns production-locked incident intake and remediation Gates.
type incidentModule struct{ engine *Engine }

func (e *Engine) incident() incidentModule { return incidentModule{engine: e} }

func (m incidentModule) callback(ctx context.Context, event webhook.ExternalCallback) error {
	return m.engine.handleIncidentCallback(ctx, event)
}
