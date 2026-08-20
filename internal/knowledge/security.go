package knowledge

import (
	"errors"
	"regexp"
)

const MaxDocumentBytes = 2 * 1024 * 1024

var (
	ErrDocumentTooLarge = errors.New("knowledge document exceeds size limit")
	ErrSensitiveContent = errors.New("knowledge document contains credential-like content")
	credentialPatterns  = []*regexp.Regexp{
		regexp.MustCompile(`(?i)-----BEGIN (RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
		regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|client[_-]?secret|password)\s*[:=]\s*["']?[A-Za-z0-9_./+\-=]{16,}`),
	}
)

func ValidateDocument(content string) error {
	if len(content) > MaxDocumentBytes {
		return ErrDocumentTooLarge
	}
	for _, pattern := range credentialPatterns {
		if pattern.MatchString(content) {
			return ErrSensitiveContent
		}
	}
	return nil
}
