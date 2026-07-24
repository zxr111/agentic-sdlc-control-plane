package confluence

import "testing"

func TestExtractPageReferencesDeduplicates(t *testing.T) {
	text := "See https://x.atlassian.net/wiki/spaces/A/pages/635077069/PID and https://x.atlassian.net/wiki/spaces/A/pages/635077069/PID."
	got := ExtractPageReferences(text)
	if len(got) != 1 || got[0] != "635077069" {
		t.Fatalf("unexpected references: %#v", got)
	}
}

func TestNormalizeStoragePreservesEmbeddedOrder(t *testing.T) {
	storage := `<h1>Title</h1><p>Hello <strong>world</strong></p>
	<ac:image><ri:attachment ri:filename="workflow.png"/></ac:image>
	<ac:image><ri:attachment ri:filename="detail.png"/></ac:image>`
	text, images := NormalizeStorage(storage)
	if text != "Title\nHello world" {
		t.Fatalf("unexpected text %q", text)
	}
	if len(images) != 2 || images[0] != "workflow.png" || images[1] != "detail.png" {
		t.Fatalf("unexpected images: %#v", images)
	}
}
