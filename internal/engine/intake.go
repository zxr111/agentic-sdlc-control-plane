package engine

import (
	"context"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
)

// intakeModule owns external requirement intake and authoritative source refresh.
type intakeModule struct{ engine *Engine }

func (e *Engine) intake() intakeModule { return intakeModule{engine: e} }

func (m intakeModule) issueChanged(ctx context.Context, event webhook.IssueChanged) error {
	return m.engine.handleIssueChanged(ctx, event)
}
