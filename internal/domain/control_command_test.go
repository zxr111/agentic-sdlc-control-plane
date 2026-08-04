package domain

import "testing"

func TestParseControlCommand(t *testing.T) {
	id := "51B9CA7E-A6F1-4BCF-A3D3-F113B9E44CB2"
	command, err := ParseControlCommand("\n/start-codex task:" + id + " client:bruce-mac\nquoted text")
	if err != nil {
		t.Fatal(err)
	}
	if command.Action != ControlStartCodex || command.WorkItemID != "51b9ca7e-a6f1-4bcf-a3d3-f113b9e44cb2" || command.ClientID != "bruce-mac" {
		t.Fatalf("unexpected command: %#v", command)
	}
}

func TestParseControlCommandDoesNotInferNaturalLanguage(t *testing.T) {
	if _, err := ParseControlCommand("please approve and start codex"); err == nil {
		t.Fatal("expected natural language to be rejected")
	}
}
