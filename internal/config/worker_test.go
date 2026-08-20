package config

import (
	"reflect"
	"testing"
)

func TestCSVEnvTrimsAndDeduplicatesWorkerTypes(t *testing.T) {
	t.Setenv("WORKER_EVENT_TYPES", " workflow.a, evaluation.run,workflow.a , ")
	if got := csvEnv("WORKER_EVENT_TYPES"); !reflect.DeepEqual(got, []string{"workflow.a", "evaluation.run"}) {
		t.Fatalf("types=%#v", got)
	}
}
