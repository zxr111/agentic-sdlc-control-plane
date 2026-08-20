package knowledge

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateDocumentRejectsSecretsAndOversize(t *testing.T) {
	if err := ValidateDocument("normal architecture requirements"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDocument("api_key=abcdefghijklmnopqrstuvwxyz123456"); !errors.Is(err, ErrSensitiveContent) {
		t.Fatalf("secret err=%v", err)
	}
	if err := ValidateDocument(strings.Repeat("x", MaxDocumentBytes+1)); !errors.Is(err, ErrDocumentTooLarge) {
		t.Fatalf("size err=%v", err)
	}
}
