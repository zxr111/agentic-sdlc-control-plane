package agents

import (
	"encoding/json"
	"testing"
)

func TestSchemasAreValidJSON(t *testing.T) {
	for name, schema := range map[string]json.RawMessage{
		"requirement": requirementSchema,
		"prd":         prdSchema,
		"test":        testPlanSchema,
	} {
		var value map[string]any
		if err := json.Unmarshal(schema, &value); err != nil {
			t.Fatalf("%s schema is invalid: %v", name, err)
		}
		if value["additionalProperties"] != false {
			t.Fatalf("%s schema must be strict", name)
		}
	}
}
