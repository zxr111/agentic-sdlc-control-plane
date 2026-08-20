package engine

import "context"

// planningModule owns requirement analysis and product/test planning.
type planningModule struct{ engine *Engine }

func (e *Engine) planning() planningModule { return planningModule{engine: e} }

func (m planningModule) analyzeRequirement(ctx context.Context, event AnalyzeRequirementEvent) error {
	return m.engine.analyzeRequirement(ctx, event)
}

func (m planningModule) generatePlans(ctx context.Context, event GeneratePlansEvent) error {
	return m.engine.generatePlans(ctx, event)
}
