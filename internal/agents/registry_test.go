package agents

import "testing"

func TestBuiltinDefinitionsAreCompleteAndUnique(t *testing.T) {
	definitions := BuiltinDefinitions()
	if len(definitions) != 4 {
		t.Fatalf("expected four built-in agents, got %d", len(definitions))
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.AgentType == "" || definition.PromptKey == "" || definition.SchemaName == "" ||
			definition.Instructions == "" || len(definition.OutputSchema) == 0 {
			t.Fatalf("incomplete definition %#v", definition)
		}
		if seen[definition.AgentType] {
			t.Fatalf("duplicate agent type %s", definition.AgentType)
		}
		seen[definition.AgentType] = true
	}
}
