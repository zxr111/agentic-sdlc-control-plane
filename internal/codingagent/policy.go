package codingagent

type RolePolicy struct {
	ReadOnly     bool     `json:"read_only"`
	AllowWrite   bool     `json:"allow_write"`
	AllowCommit  bool     `json:"allow_commit"`
	AllowedTools []string `json:"allowed_tools"`
	PathScope    string   `json:"path_scope"`
	CommandScope string   `json:"command_scope"`
}

func PolicyForRole(role Role) RolePolicy {
	read := []string{"knowledge.search", "ontology.query", "repository.search", "repository.read", "git.diff"}
	switch role {
	case RoleCoder, RoleRepair:
		return RolePolicy{AllowWrite: true, AllowCommit: true, PathScope: "manifest.allowed_paths",
			CommandScope: "manifest.required_checks", AllowedTools: append(read,
				"workspace.apply_patch", "workspace.format", "test.run", "build.run", "git.commit")}
	case RoleReviewer:
		return RolePolicy{ReadOnly: true, PathScope: "manifest.allowed_paths", CommandScope: "manifest.required_checks",
			AllowedTools: append(read, "test.run", "lint.run")}
	case RoleSecurity:
		return RolePolicy{ReadOnly: true, PathScope: "manifest.allowed_paths", CommandScope: "security.read_only",
			AllowedTools: append(read, "security.scan", "dependency.scan")}
	case RoleQuality:
		return RolePolicy{ReadOnly: true, PathScope: "manifest.allowed_paths", CommandScope: "manifest.required_checks",
			AllowedTools: append(read, "test.run", "lint.run", "build.run", "security.scan")}
	case RoleJudge:
		return RolePolicy{ReadOnly: true, PathScope: "none", CommandScope: "none", AllowedTools: []string{}}
	default:
		return RolePolicy{ReadOnly: true, PathScope: "manifest.allowed_paths", CommandScope: "read_only", AllowedTools: read}
	}
}
