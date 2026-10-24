package codingagent

import "testing"

func TestOnlyWriterRolesCanWriteAndCommit(t *testing.T) {
	for _, role := range []Role{RolePlanner, RoleTest, RoleReviewer, RoleSecurity, RoleJudge, RoleQuality} {
		policy := PolicyForRole(role)
		if policy.AllowWrite || policy.AllowCommit {
			t.Fatalf("role %s unexpectedly has write authority", role)
		}
	}
	for _, role := range []Role{RoleCoder, RoleRepair} {
		policy := PolicyForRole(role)
		if !policy.AllowWrite || !policy.AllowCommit {
			t.Fatalf("role %s is missing controlled write authority", role)
		}
	}
}
