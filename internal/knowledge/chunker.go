package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type Chunk struct {
	Index      int
	ParentPath string
	Content    string
	TokenCount int
	Hash       string
}

// ChunkText creates deterministic overlapping chunks. It uses whitespace
// tokens as a conservative local estimate; provider token usage remains the
// authoritative runtime measurement.
func ChunkText(text, parentPath string, size, overlap int) []Chunk {
	words := strings.Fields(text)
	if size <= 0 {
		size = 400
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 8
	}
	if len(words) <= 1 && EstimateTokens(text) > size {
		return chunkRunes(text, parentPath, size, overlap)
	}
	step := size - overlap
	var chunks []Chunk
	for start := 0; start < len(words); start += step {
		end := start + size
		if end > len(words) {
			end = len(words)
		}
		content := strings.Join(words[start:end], " ")
		digest := sha256.Sum256([]byte(content))
		chunks = append(chunks, Chunk{Index: len(chunks), ParentPath: parentPath, Content: content,
			TokenCount: len(words[start:end]), Hash: hex.EncodeToString(digest[:])})
		if end == len(words) {
			break
		}
	}
	return chunks
}

func chunkRunes(text, parentPath string, size, overlap int) []Chunk {
	values := []rune(text)
	step := size - overlap
	var chunks []Chunk
	for start := 0; start < len(values); start += step {
		end := min(start+size, len(values))
		content := strings.TrimSpace(string(values[start:end]))
		if content != "" {
			digest := sha256.Sum256([]byte(content))
			chunks = append(chunks, Chunk{Index: len(chunks), ParentPath: parentPath, Content: content,
				TokenCount: EstimateTokens(content), Hash: hex.EncodeToString(digest[:])})
		}
		if end == len(values) {
			break
		}
	}
	return chunks
}
