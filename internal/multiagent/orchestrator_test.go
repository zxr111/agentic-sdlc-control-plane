package multiagent

import (
	"context"
	"sync"
	"testing"
)

type fakeRunner struct{}

func (fakeRunner) Analyze(_ context.Context, role string, _ Input) (Opinion, error) {
	decision := "READY"
	if role == RoleSecurity {
		decision = "CHANGES_REQUESTED"
	}
	return Opinion{Role: role, Decision: decision, Confidence: .8, Summary: role + " summary", Evidence: []string{"source@v1"}}, nil
}
func (fakeRunner) Judge(_ context.Context, _ Input, _ []Opinion) (Synthesis, error) {
	return Synthesis{Decision: "READY", Summary: "judge", Consensus: []string{"bounded"}}, nil
}

type memoryRecorder struct {
	mu       sync.Mutex
	values   []Opinion
	minority []bool
}

func (r *memoryRecorder) RecordOpinion(_ context.Context, opinion Opinion, minority bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, opinion)
	r.minority = append(r.minority, minority)
	return nil
}

func TestOrchestratorPreservesMinorityOpinion(t *testing.T) {
	recorder := &memoryRecorder{}
	opinions, synthesis, err := New(fakeRunner{}).Execute(context.Background(), Input{WorkflowID: "w"}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if len(opinions) != 3 || len(synthesis.Disagreements) != 1 || synthesis.Disagreements[0].Role != RoleSecurity {
		t.Fatalf("minority lost opinions=%#v synthesis=%#v", opinions, synthesis)
	}
	if len(recorder.values) != 5 || !recorder.minority[4] {
		t.Fatalf("unexpected persisted opinions %#v %#v", recorder.values, recorder.minority)
	}
}
