package knowledge

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode"
)

const EmbeddingDimensions = 64

// EmbedText builds a deterministic signed feature-hash vector. It provides a
// local, replayable semantic signal for Hybrid RAG without sending indexed
// source content to a third-party embedding service.
func EmbedText(value string) []float64 {
	vector := make([]float64, EmbeddingDimensions)
	for _, token := range tokens(value) {
		digest := sha256.Sum256([]byte(token))
		index := int(binary.BigEndian.Uint64(digest[:8]) % EmbeddingDimensions)
		sign := 1.0
		if digest[8]&1 == 1 {
			sign = -1
		}
		vector[index] += sign
	}
	var sum float64
	for _, value := range vector {
		sum += value * value
	}
	if sum == 0 {
		return vector
	}
	norm := math.Sqrt(sum)
	for index := range vector {
		vector[index] /= norm
	}
	return vector
}

func VectorLiteral(vector []float64) string {
	values := make([]string, len(vector))
	for index, value := range vector {
		values[index] = fmt.Sprintf("%.8f", value)
	}
	return "[" + strings.Join(values, ",") + "]"
}

func tokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
