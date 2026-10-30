package plan_test

import (
	"testing"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/scope"
)

const testResID ownership.ResourceID = "res-main"

var (
	actorA = ownership.ActorID("agent-a")
	actorB = ownership.ActorID("agent-b")
)

func mustDecl(t *testing.T, src string) *projection.Declaration {
	t.Helper()
	decl, err := projection.ParseDeclaration("test.lkal", []byte("@lkal \"0.1\"\n"+src+"\n"))
	if err != nil {
		t.Fatalf("ParseDeclaration failed: %v", err)
	}
	return decl
}

func mustSnap(t *testing.T, entries map[string]observation.Observed) *observation.Snapshot {
	t.Helper()
	snap := observation.NewSnapshot()
	for rawPath, obs := range entries {
		snap.Set(model.MustParsePath(rawPath), obs)
	}
	return snap
}

func fullScope(t *testing.T) scope.Scope {
	t.Helper()
	r, err := scope.NewRootRule(scope.PermAll)
	if err != nil {
		t.Fatalf("NewRootRule failed: %v", err)
	}
	return scope.MustNew(r)
}

func exactScope(t *testing.T, pathStr string, perms ...scope.Permission) scope.Scope {
	t.Helper()
	r, err := scope.NewExactRule(model.MustParsePath(pathStr), scope.NewPermissionSet(perms...))
	if err != nil {
		t.Fatalf("NewExactRule(%q) failed: %v", pathStr, err)
	}
	return scope.MustNew(r)
}

func mustPut(t *testing.T, s *ownership.Store, p string, entry ownership.OwnerEntry) *ownership.Store {
	t.Helper()
	next, err := s.Put(testResID, model.MustParsePath(p), entry)
	if err != nil {
		t.Fatalf("Store.Put(%q) failed: %v", p, err)
	}
	return next
}

func hasDiagnosticCode(diags []model.Diagnostic, code model.DiagnosticCode) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func hasDiagnosticAtPath(diags []model.Diagnostic, p string, code model.DiagnosticCode) bool {
	target := model.MustParsePath(p)
	for _, d := range diags {
		if d.Code == code && d.Path.Equal(target) {
			return true
		}
	}
	return false
}

func findPathResult(results []plan.PathResult, p string) (plan.PathResult, bool) {
	target := model.MustParsePath(p)
	for _, r := range results {
		if r.Path.Equal(target) {
			return r, true
		}
	}
	return plan.PathResult{}, false
}
