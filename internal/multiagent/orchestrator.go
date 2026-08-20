package multiagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const (
	RolePrimary  = "PRIMARY"
	RoleCritic   = "CRITIC"
	RoleSecurity = "SECURITY_RELIABILITY"
	RoleJudge    = "JUDGE"
)

type Input struct {
	WorkflowID        string
	AgentType         string
	ContextManifestID string
	AuthoritativeText string
	PrimaryArtifact   []byte
}

type Opinion struct {
	Role       string         `json:"role"`
	Decision   string         `json:"decision"`
	Confidence float64        `json:"confidence"`
	Summary    string         `json:"summary"`
	Findings   []string       `json:"findings"`
	Evidence   []string       `json:"evidence"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type Synthesis struct {
	Decision        string    `json:"decision"`
	Consensus       []string  `json:"consensus"`
	Disagreements   []Opinion `json:"disagreements"`
	UnresolvedRisks []string  `json:"unresolved_risks"`
	Summary         string    `json:"summary"`
}

type Runner interface {
	Analyze(context.Context, string, Input) (Opinion, error)
	Judge(context.Context, Input, []Opinion) (Synthesis, error)
}

type Recorder interface {
	RecordOpinion(context.Context, Opinion, bool) error
}

type Orchestrator struct{ runner Runner }

func New(runner Runner) *Orchestrator { return &Orchestrator{runner: runner} }

// Execute is deliberately bounded to one independent round followed by one
// Judge call. Members never receive another member's hidden model state.
func (o *Orchestrator) Execute(ctx context.Context, input Input, recorder Recorder) ([]Opinion, Synthesis, error) {
	if o.runner == nil {
		return nil, Synthesis{}, errors.New("multi-agent runner is required")
	}
	roles := []string{RolePrimary, RoleCritic, RoleSecurity}
	opinions := make([]Opinion, len(roles))
	errorsByRole := make([]error, len(roles))
	var wait sync.WaitGroup
	for index, role := range roles {
		wait.Add(1)
		go func(index int, role string) {
			defer wait.Done()
			opinion, err := o.runner.Analyze(ctx, role, input)
			if err == nil {
				opinion.Role = role
				err = validateOpinion(opinion)
			}
			opinions[index], errorsByRole[index] = opinion, err
		}(index, role)
	}
	wait.Wait()
	for index, err := range errorsByRole {
		if err != nil {
			return opinions, Synthesis{}, fmt.Errorf("%s opinion failed: %w", roles[index], err)
		}
		if recorder != nil {
			if err := recorder.RecordOpinion(ctx, opinions[index], false); err != nil {
				return opinions, Synthesis{}, err
			}
		}
	}
	synthesis, err := o.runner.Judge(ctx, input, opinions)
	if err != nil {
		return opinions, Synthesis{}, fmt.Errorf("judge failed: %w", err)
	}
	// The runtime, not the Judge, enforces preservation of every dissenting
	// formal opinion. This prevents a model from silently erasing minorities.
	for _, opinion := range opinions {
		if opinion.Decision != synthesis.Decision && !containsRole(synthesis.Disagreements, opinion.Role) {
			synthesis.Disagreements = append(synthesis.Disagreements, opinion)
		}
	}
	if recorder != nil {
		judge := Opinion{Role: RoleJudge, Decision: synthesis.Decision, Confidence: 1, Summary: synthesis.Summary,
			Findings: append(append([]string{}, synthesis.Consensus...), synthesis.UnresolvedRisks...)}
		if err := recorder.RecordOpinion(ctx, judge, false); err != nil {
			return opinions, Synthesis{}, err
		}
		for _, minority := range synthesis.Disagreements {
			if err := recorder.RecordOpinion(ctx, minority, true); err != nil {
				return opinions, Synthesis{}, err
			}
		}
	}
	return opinions, synthesis, nil
}

func validateOpinion(opinion Opinion) error {
	if opinion.Decision == "" || opinion.Summary == "" {
		return errors.New("decision and summary are required")
	}
	if opinion.Confidence < 0 || opinion.Confidence > 1 {
		return errors.New("confidence must be between zero and one")
	}
	return nil
}

func containsRole(opinions []Opinion, role string) bool {
	for _, opinion := range opinions {
		if opinion.Role == role {
			return true
		}
	}
	return false
}
