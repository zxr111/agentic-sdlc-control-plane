package knowledge

import "testing"

func TestRewriteQueryIsBoundedAndGrounded(t *testing.T) {
	got := RewriteQuery("How do we preserve the payment retry idempotency key with payment retry?")
	if got != "preserve payment retry idempotency key" {
		t.Fatalf("rewrite=%q", got)
	}
}
