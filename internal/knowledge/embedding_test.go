package knowledge

import (
	"math"
	"reflect"
	"testing"
)

func TestEmbedTextIsDeterministicAndNormalized(t *testing.T) {
	first := EmbedText("Payment retry preserves idempotency")
	second := EmbedText("Payment retry preserves idempotency")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("embedding is not deterministic")
	}
	if len(first) != EmbeddingDimensions {
		t.Fatalf("dimensions=%d", len(first))
	}
	var sum float64
	for _, value := range first {
		sum += value * value
	}
	if math.Abs(math.Sqrt(sum)-1) > 0.000001 {
		t.Fatalf("norm=%f", math.Sqrt(sum))
	}
	if VectorLiteral(first)[0] != '[' {
		t.Fatal("invalid vector literal")
	}
}
