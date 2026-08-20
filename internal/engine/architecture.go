package engine

import "context"

// architectureModule owns architecture generation and governed review.
type architectureModule struct{ engine *Engine }

func (e *Engine) architecture() architectureModule { return architectureModule{engine: e} }

func (m architectureModule) generate(ctx context.Context, event GenerateArchitectureEvent) error {
	return m.engine.generateArchitecture(ctx, event)
}
