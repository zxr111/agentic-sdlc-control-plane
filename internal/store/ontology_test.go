package store

import "testing"

func TestOntologyKeyIsStable(t *testing.T) {
	if got := ontologyKey("  Order-Service  "); got != "order-service" {
		t.Fatalf("unexpected ontology key %q", got)
	}
	if ontologyHash("a", "bc") == ontologyHash("ab", "c") {
		t.Fatal("ontology provenance hash must preserve field boundaries")
	}
}
