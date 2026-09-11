package store

import (
	"reflect"
	"testing"
)

func TestUnmatchedPaths(t *testing.T) {
	got := unmatchedPaths([]string{"services/orders/handler.go", "deploy/orders.yaml", "README.md"},
		[]string{"services/orders/*", "deploy/orders.yaml"})
	want := []string{"README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
