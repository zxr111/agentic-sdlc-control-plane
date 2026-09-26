package codingagent

import "testing"

func TestManifestRejectsAuthorityEscalation(t *testing.T) {
	m := TaskManifest{ID: "m", Version: 1, DispatchID: "d", WorkItemID: "wi", WorkflowID: "wf",
		Provider: "pi", Repository: "group/repo", BaseRef: "main", Branch: "ai/x", AllowedPaths: []string{"internal/**"},
		SourceArtifactID: "sdd", SourceArtifactHash: "hash", Permissions: PermissionSet{Merge: true}}
	if err := m.Seal(); err == nil {
		t.Fatal("expected merge permission to be rejected")
	}
}

func TestEvidenceMustMatchManifest(t *testing.T) {
	m := TaskManifest{ID: "m", Version: 1, DispatchID: "d", WorkItemID: "wi", WorkflowID: "wf",
		Provider: "pi", Repository: "group/repo", BaseRef: "main", Branch: "ai/x", AllowedPaths: []string{"internal/**"},
		SourceArtifactID: "sdd", SourceArtifactHash: "hash"}
	if err := m.Seal(); err != nil {
		t.Fatal(err)
	}
	e := Evidence{DispatchID: "d", ManifestHash: "wrong", Provider: "pi", SessionID: "session", Kind: "SESSION_STARTED"}
	if err := e.Validate(m); err == nil {
		t.Fatal("expected mismatched manifest hash to be rejected")
	}
}

func TestManifestDetectsMutation(t *testing.T) {
	m := TaskManifest{ID: "m", Version: 1, DispatchID: "d", WorkItemID: "wi", WorkflowID: "wf",
		Provider: "pi", Repository: "group/repo", BaseRef: "main", Branch: "ai/x", AllowedPaths: []string{"internal/**"},
		SourceArtifactID: "sdd", SourceArtifactHash: "hash"}
	if err := m.Seal(); err != nil {
		t.Fatal(err)
	}
	m.AllowedPaths = append(m.AllowedPaths, "deploy/**")
	if err := m.Verify(); err == nil {
		t.Fatal("expected changed manifest to fail verification")
	}
}
