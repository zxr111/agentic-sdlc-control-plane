package knowledge

import "testing"

func TestEvaluateRanking(t *testing.T) {
	metrics := EvaluateRanking([]string{"noise", "a", "b"}, map[string]bool{"a": true, "b": true}, 3)
	if metrics.RecallAtK != 1 || metrics.MRR != .5 || metrics.NDCGAtK <= 0 || metrics.NDCGAtK >= 1 {
		t.Fatalf("metrics=%#v", metrics)
	}
}

func TestEvaluateCitations(t *testing.T) {
	metrics := EvaluateCitations([]string{"K-001", "K-999"}, []string{"K-001", "K-002"})
	if metrics.Precision != .5 || metrics.Recall != .5 {
		t.Fatalf("metrics=%#v", metrics)
	}
}
