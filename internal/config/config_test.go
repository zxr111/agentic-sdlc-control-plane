package config

import (
	"os"
	"testing"
)

func TestLoadProjects(t *testing.T) {
	t.Setenv("FACTORY_PROJECTS_JSON", `[{
		"gitlab_project_id": 10,
		"path": "argus/argus-server",
		"reviewer_ids": {
			"REQUIREMENT": [1],
			"PRD": [2],
			"TEST": [3]
		}
	}]`)
	got, err := loadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if got[10].EnabledLabel != "automation::enabled" {
		t.Fatalf("default label missing: %#v", got[10])
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
