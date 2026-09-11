package knowledge

import "math"

type RetrievalMetrics struct {
	RecallAtK float64
	MRR       float64
	NDCGAtK   float64
}

type CitationMetrics struct {
	Precision float64
	Recall    float64
}

func EvaluateCitations(referenced, expected []string) CitationMetrics {
	expectedSet := map[string]bool{}
	for _, id := range expected {
		expectedSet[id] = true
	}
	matched := 0
	for _, id := range referenced {
		if expectedSet[id] {
			matched++
		}
	}
	metrics := CitationMetrics{}
	if len(referenced) > 0 {
		metrics.Precision = float64(matched) / float64(len(referenced))
	}
	if len(expected) > 0 {
		metrics.Recall = float64(matched) / float64(len(expected))
	}
	return metrics
}

// EvaluateRanking computes deterministic offline retrieval metrics from the
// ordered result identifiers and the set of expected relevant identifiers.
func EvaluateRanking(results []string, relevant map[string]bool, k int) RetrievalMetrics {
	if k <= 0 || k > len(results) {
		k = len(results)
	}
	if len(relevant) == 0 {
		return RetrievalMetrics{}
	}
	matched := 0
	dcg := 0.0
	mrr := 0.0
	for index := 0; index < k; index++ {
		if !relevant[results[index]] {
			continue
		}
		matched++
		if mrr == 0 {
			mrr = 1 / float64(index+1)
		}
		dcg += 1 / math.Log2(float64(index+2))
	}
	idealCount := min(k, len(relevant))
	idcg := 0.0
	for index := 0; index < idealCount; index++ {
		idcg += 1 / math.Log2(float64(index+2))
	}
	return RetrievalMetrics{RecallAtK: float64(matched) / float64(len(relevant)), MRR: mrr, NDCGAtK: dcg / idcg}
}
