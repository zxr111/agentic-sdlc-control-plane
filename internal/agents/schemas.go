package agents

import "encoding/json"

var requirementSchema = json.RawMessage(`{
  "type":"object","additionalProperties":false,
  "properties":{
    "decision":{"type":"string","enum":["changes_requested","ready_for_human_approval"]},
    "goal":{"type":"string"},"summary":{"type":"string"},
    "facts":{"type":"array","items":{"type":"string"}},
    "inferences":{"type":"array","items":{"type":"string"}},
    "questions":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{
      "id":{"type":"string"},"target_role":{"type":"string"},"question":{"type":"string"},
      "why_blocking":{"type":"string"},"evidence_needed":{"type":"string"},"blocking":{"type":"boolean"}
    },"required":["id","target_role","question","why_blocking","evidence_needed","blocking"]}},
    "in_scope":{"type":"array","items":{"type":"string"}},
    "out_of_scope":{"type":"array","items":{"type":"string"}},
    "constraints":{"type":"array","items":{"type":"string"}},
    "failure_modes":{"type":"array","items":{"type":"string"}},
    "risks":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{
      "id":{"type":"string"},"severity":{"type":"string","enum":["low","medium","high","critical"]},
      "evidence":{"type":"string"},"impact":{"type":"string"},"mitigation":{"type":"string"}
    },"required":["id","severity","evidence","impact","mitigation"]}},
    "acceptance_criteria":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{
      "id":{"type":"string"},"behavior":{"type":"string"},"evidence":{"type":"string"}
    },"required":["id","behavior","evidence"]}},
    "work_items":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{
      "key":{"type":"string","pattern":"^[a-z0-9][a-z0-9-]{1,62}$"},
      "title":{"type":"string","pattern":"^\\[(Feature|Bug|Tech|Spike)\\]\\[[A-Za-z0-9-]{2,20}\\] .+"},
      "owner_role":{"type":"string","enum":["requirements","backend","frontend","fullstack","quality"]},
      "rationale":{"type":"string"},"independent_boundary":{"type":"string"},
      "dependencies":{"type":"array","items":{"type":"string"}}
    },"required":["key","title","owner_role","rationale","independent_boundary","dependencies"]}}
  },
  "required":["decision","goal","summary","facts","inferences","questions","in_scope","out_of_scope",
    "constraints","failure_modes","risks","acceptance_criteria","work_items"]
}`)

var questionSchema = `{"type":"object","additionalProperties":false,"properties":{
  "id":{"type":"string"},"target_role":{"type":"string"},"question":{"type":"string"},
  "why_blocking":{"type":"string"},"evidence_needed":{"type":"string"},"blocking":{"type":"boolean"}
},"required":["id","target_role","question","why_blocking","evidence_needed","blocking"]}`

var requirementEntrySchema = `{"type":"object","additionalProperties":false,"properties":{
  "id":{"type":"string"},"description":{"type":"string"},"source_ac":{"type":"array","items":{"type":"string"}}
},"required":["id","description","source_ac"]}`

var prdSchema = json.RawMessage(`{
  "type":"object","additionalProperties":false,
  "properties":{
    "problem":{"type":"string"},"goal":{"type":"string"},
    "personas":{"type":"array","items":{"type":"string"}},
    "user_journeys":{"type":"array","items":{"type":"string"}},
    "functional_requirements":{"type":"array","items":` + requirementEntrySchema + `},
    "non_functional_requirements":{"type":"array","items":` + requirementEntrySchema + `},
    "data_contracts":{"type":"array","items":{"type":"string"}},
    "dependencies":{"type":"array","items":{"type":"string"}},
    "out_of_scope":{"type":"array","items":{"type":"string"}},
    "rollout":{"type":"array","items":{"type":"string"}},
    "rollback":{"type":"array","items":{"type":"string"}},
    "observability":{"type":"array","items":{"type":"string"}},
    "open_questions":{"type":"array","items":` + questionSchema + `}
  },
  "required":["problem","goal","personas","user_journeys","functional_requirements",
    "non_functional_requirements","data_contracts","dependencies","out_of_scope",
    "rollout","rollback","observability","open_questions"]
}`)

var testPlanSchema = json.RawMessage(`{
  "type":"object","additionalProperties":false,
  "properties":{
    "decision":{"type":"string","enum":["changes_requested","ready_for_human_approval"]},
    "coverage_summary":{"type":"string"},
    "blockers":{"type":"array","items":{"type":"string"}},
    "residual_risks":{"type":"array","items":{"type":"string"}},
    "test_cases":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{
      "id":{"type":"string"},"name":{"type":"string"},
      "acceptance_criteria":{"type":"array","items":{"type":"string"}},
      "layer":{"type":"string","enum":["unit","integration","API","E2E","manual"]},
      "execution":{"type":"string","enum":["automated","manual"]},
      "priority":{"type":"string","enum":["P0","P1","P2"]},
      "preconditions":{"type":"array","items":{"type":"string"}},
      "test_data":{"type":"array","items":{"type":"string"}},
      "steps":{"type":"array","items":{"type":"string"}},
      "expected_result":{"type":"string"},
      "cleanup":{"type":"array","items":{"type":"string"}},
      "coverage_dimensions":{"type":"array","items":{"type":"string"}}
    },"required":["id","name","acceptance_criteria","layer","execution","priority","preconditions",
      "test_data","steps","expected_result","cleanup","coverage_dimensions"]}},
    "coverage_matrix":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{
      "acceptance_criterion":{"type":"string"},
      "test_cases":{"type":"array","items":{"type":"string"}},
      "dimensions":{"type":"array","items":{"type":"string"}},
      "gaps":{"type":"array","items":{"type":"string"}}
    },"required":["acceptance_criterion","test_cases","dimensions","gaps"]}}
  },
  "required":["decision","coverage_summary","blockers","residual_risks","test_cases","coverage_matrix"]
}`)
