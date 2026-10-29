package scope_test

import (
	"errors"
	"testing"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/scope"
)

func assertDiagnosticCode(t *testing.T, err error, want model.DiagnosticCode) model.Diagnostic {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var diag model.Diagnostic
	if !errors.As(err, &diag) {
		t.Fatalf("expected model.Diagnostic (want %s), got %T: %v", want, err, err)
	}
	if diag.Code != want {
		t.Fatalf("expected diagnostic code %s, got %s (%v)", want, diag.Code, diag)
	}
	return diag
}

// TestCounterexample15_SegmentScopeAndOperationPermissions verifies Counterexample #15 (ADR 0002 §3):
//   - Scope checks operate strictly on parsed segment sequences, never raw string prefixes
//     (e.g., `/a/b` must not match `/a/bc`; `/a` must not match `/"a/b"/c`).
//   - Parent/child Scope rules compose additively per operation (`Set`, `Delete`, `Own`, `Resolve`).
func TestCounterexample15_SegmentScopeAndOperationPermissions(t *testing.T) {
	ruleAB, err := scope.NewSubtreeRule(model.MustParsePath("/a/b"), scope.NewPermissionSet(scope.PermSet, scope.PermOwn))
	if err != nil {
		t.Fatal(err)
	}
	ruleQuoted, err := scope.NewSubtreeRule(model.MustParsePath(`/"a/b"`), scope.NewPermissionSet(scope.PermDelete, scope.PermOwn))
	if err != nil {
		t.Fatal(err)
	}
	ruleExactC, err := scope.NewExactRule(model.MustParsePath("/a/b/c"), scope.NewPermissionSet(scope.PermResolve))
	if err != nil {
		t.Fatal(err)
	}
	s := scope.MustNew(ruleAB, ruleQuoted, ruleExactC)

	// 1. /a/b and /a/b/c are covered by ruleAB (Set+Own), and /a/b/c also has Exact Resolve.
	if err := s.AuthorizeAction(model.MustParsePath("/a/b"), scope.ActionSet); err != nil {
		t.Fatalf("expected /a/b Set to be allowed: %v", err)
	}
	if err := s.AuthorizeAction(model.MustParsePath("/a/b/c"), scope.ActionSet); err != nil {
		t.Fatalf("expected /a/b/c Set to be allowed: %v", err)
	}
	if err := s.AuthorizeAction(model.MustParsePath("/a/b/c"), scope.ActionExplicitAdopt); err != nil {
		t.Fatalf("expected /a/b/c ExplicitAdopt (Own+Resolve) to be allowed: %v", err)
	}
	// /a/b/d does not have Resolve because ruleExactC is MatchExact on /a/b/c.
	assertDiagnosticCode(t, s.AuthorizeAction(model.MustParsePath("/a/b/d"), scope.ActionExplicitAdopt), model.CodePolicyDenied)

	// 2. String prefix collision: /a/bc starts with "/a/b" as a raw string, but is a distinct segment!
	pCollision := model.MustParsePath("/a/bc")
	if !s.PermissionsFor(pCollision).IsEmpty() {
		t.Fatalf("expected no permissions on /a/bc, got %s", s.PermissionsFor(pCollision))
	}
	assertDiagnosticCode(t, s.AuthorizeAction(pCollision, scope.ActionSet), model.CodePolicyDenied)

	// 3. Quoted segment containing '/': /"a/b"/c has first segment "a/b", not "a".
	ruleAOnly, err := scope.NewSubtreeRule(model.MustParsePath("/a"), scope.PermAll)
	if err != nil {
		t.Fatal(err)
	}
	scopeA := scope.MustNew(ruleAOnly)
	pQuotedChild := model.MustParsePath(`/"a/b"/c`)
	if !scopeA.PermissionsFor(pQuotedChild).IsEmpty() {
		t.Fatalf("/a rule must not match quoted segment path %s", pQuotedChild)
	}
	assertDiagnosticCode(t, scopeA.AuthorizeAction(pQuotedChild, scope.ActionSet), model.CodePolicyDenied)

	// Conversely, ruleQuoted on /"a/b" matches /"a/b"/c for Delete+Own, but not /a/b/c for Delete.
	if err := s.AuthorizeAction(pQuotedChild, scope.ActionDelete); err != nil {
		t.Fatalf("expected /\"a/b\"/c Delete to be allowed: %v", err)
	}
	assertDiagnosticCode(t, s.AuthorizeAction(model.MustParsePath("/a/b/c"), scope.ActionDelete), model.CodePolicyDenied)
}

// TestADR0002_ActionPermissionMapping verifies the normative 4-permission table in ADR 0002 §3
// and Counterexamples #11, #23, and #38.
func TestADR0002_ActionPermissionMapping(t *testing.T) {
	p := model.MustParsePath("/service/port")

	tests := []struct {
		name    string
		action  scope.ActionKind
		granted scope.PermissionSet
		allowed bool
	}{
		// ActionSet requires Set + Own
		{"Set with Set+Own", scope.ActionSet, scope.NewPermissionSet(scope.PermSet, scope.PermOwn), true},
		{"Set missing Own", scope.ActionSet, scope.NewPermissionSet(scope.PermSet), false},
		{"Set missing Set", scope.ActionSet, scope.NewPermissionSet(scope.PermOwn), false},

		// Metadata-only & recovery actions require Own (Counterexamples #11, #23)
		{"AdoptModified with Own", scope.ActionAdoptModified, scope.NewPermissionSet(scope.PermOwn), true},
		{"AdoptModified with Set only", scope.ActionAdoptModified, scope.NewPermissionSet(scope.PermSet), false},
		{"Unmanage with Own", scope.ActionUnmanage, scope.NewPermissionSet(scope.PermOwn), true},
		{"Unmanage without Own", scope.ActionUnmanage, scope.NewPermissionSet(scope.PermDelete), false},
		{"ClearRecord with Own", scope.ActionClearRecord, scope.NewPermissionSet(scope.PermOwn), true},
		{"ClearRecord without Own", scope.ActionClearRecord, scope.NewPermissionSet(scope.PermDelete), false},
		{"Recover with Own", scope.ActionRecover, scope.NewPermissionSet(scope.PermOwn), true},
		{"Recover without Own", scope.ActionRecover, scope.NewPermissionSet(scope.PermSet), false},

		// ActionDelete (prune) requires Delete + Own
		{"Delete with Delete+Own", scope.ActionDelete, scope.NewPermissionSet(scope.PermDelete, scope.PermOwn), true},
		{"Delete missing Own", scope.ActionDelete, scope.NewPermissionSet(scope.PermDelete), false},
		{"Delete missing Delete", scope.ActionDelete, scope.NewPermissionSet(scope.PermOwn), false},

		// ExplicitAdopt / ExplicitRelease require Own + Resolve; Resolve is never implied by Set/Delete/Own (Counterexample #38)
		{"ExplicitAdopt with Own+Resolve", scope.ActionExplicitAdopt, scope.NewPermissionSet(scope.PermOwn, scope.PermResolve), true},
		{"ExplicitAdopt with PermRegular (Set+Delete+Own, no Resolve)", scope.ActionExplicitAdopt, scope.PermRegular, false},
		{"ExplicitAdopt with Resolve only (no Own)", scope.ActionExplicitAdopt, scope.NewPermissionSet(scope.PermResolve), false},
		{"ExplicitRelease with Own+Resolve", scope.ActionExplicitRelease, scope.NewPermissionSet(scope.PermOwn, scope.PermResolve), true},
		{"ExplicitRelease with PermRegular (no Resolve)", scope.ActionExplicitRelease, scope.PermRegular, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := scope.NewExactRule(p, tc.granted)
			if err != nil {
				t.Fatal(err)
			}
			s := scope.MustNew(r)
			err = s.AuthorizeAction(p, tc.action)
			if tc.allowed && err != nil {
				t.Fatalf("expected allowed, got %v", err)
			}
			if !tc.allowed {
				diag := assertDiagnosticCode(t, err, model.CodePolicyDenied)
				if !diag.Path.Equal(p) {
					t.Fatalf("expected diagnostic path %s, got %s", p, diag.Path)
				}
			}
		})
	}
}

func TestFailClosedOnInvalidRulesAndPaths(t *testing.T) {
	// Zero-value Rule with empty Prefix and default MatchSubtree must fail closed and match nothing.
	uninitializedRule := scope.Rule{Perms: scope.PermAll}
	if uninitializedRule.Matches(model.MustParsePath("/a")) {
		t.Fatal("uninitialized Rule with empty Prefix must fail closed")
	}
	if _, err := scope.New(uninitializedRule); err == nil {
		t.Fatal("expected scope.New to reject uninitialized Rule")
	}

	// Explicit root rule matches valid leaf paths, but rejects root or invalid paths.
	rootRule, err := scope.NewRootRule(scope.PermRegular)
	if err != nil {
		t.Fatal(err)
	}
	s := scope.MustNew(rootRule)
	if !s.Allows(model.MustParsePath("/a/b"), scope.NewPermissionSet(scope.PermSet, scope.PermOwn)) {
		t.Fatal("expected root rule to allow /a/b")
	}
	if s.Allows(nil, scope.NewPermissionSet(scope.PermOwn)) {
		t.Fatal("root path itself is not a valid leaf path and must be denied")
	}
	assertDiagnosticCode(t, s.Authorize(model.MustParsePath(`/a/"$bad"`), scope.NewPermissionSet(scope.PermOwn)), model.CodeInvalidInput)
}
