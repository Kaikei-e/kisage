package scope

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/kisage/model"
)

// MatchMode specifies how a Rule's parsed path matches target leaf paths.
type MatchMode uint8

const (
	// MatchSubtree matches the rule's non-root Prefix path itself and any proper descendant path (Prefix ≼ p).
	// An empty Prefix with MatchSubtree fails closed and matches nothing.
	MatchSubtree MatchMode = iota
	// MatchExact matches only the exact non-root Prefix path (Prefix == p).
	MatchExact
	// MatchRoot matches any valid leaf path in the target resource.
	MatchRoot
)

// Rule grants a PermissionSet on a parsed path scope (ADR 0002 §3, Counterexample #15).
// All matching operates strictly on parsed segment sequences, never on raw string prefixes.
type Rule struct {
	Prefix model.Path
	Match  MatchMode
	Perms  PermissionSet
}

// NewSubtreeRule creates a Rule that grants perms on prefix and all of its proper descendants.
func NewSubtreeRule(prefix model.Path, perms PermissionSet) (Rule, error) {
	if err := model.ValidateLeafPath(prefix); err != nil {
		return Rule{}, err
	}
	if err := validatePermSet(perms, prefix); err != nil {
		return Rule{}, err
	}
	return Rule{
		Prefix: prefix.Clone(),
		Match:  MatchSubtree,
		Perms:  perms,
	}, nil
}

// NewExactRule creates a Rule that grants perms only on the exact path target.
func NewExactRule(target model.Path, perms PermissionSet) (Rule, error) {
	if err := model.ValidateLeafPath(target); err != nil {
		return Rule{}, err
	}
	if err := validatePermSet(perms, target); err != nil {
		return Rule{}, err
	}
	return Rule{
		Prefix: target.Clone(),
		Match:  MatchExact,
		Perms:  perms,
	}, nil
}

// NewRootRule creates a Rule that grants perms on all valid leaf paths in the resource.
func NewRootRule(perms PermissionSet) (Rule, error) {
	if err := validatePermSet(perms, nil); err != nil {
		return Rule{}, err
	}
	return Rule{
		Match: MatchRoot,
		Perms: perms,
	}, nil
}

// Validate checks that r is a well-formed Rule.
func (r Rule) Validate() error {
	if err := validatePermSet(r.Perms, r.Prefix); err != nil {
		return err
	}
	switch r.Match {
	case MatchSubtree, MatchExact:
		return model.ValidateLeafPath(r.Prefix)
	case MatchRoot:
		if len(r.Prefix) != 0 {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   r.Prefix.Clone(),
				Reason: "MatchRoot rule must not specify a non-root prefix",
			}
		}
		return nil
	default:
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   r.Prefix.Clone(),
			Reason: fmt.Sprintf("invalid scope match mode %d", r.Match),
		}
	}
}

// Clone returns a defensive copy of r.
func (r Rule) Clone() Rule {
	return Rule{
		Prefix: r.Prefix.Clone(),
		Match:  r.Match,
		Perms:  r.Perms,
	}
}

// Matches reports whether r applies to target leaf path p using parsed segment comparison.
// Invalid rules or invalid target leaf paths fail closed and return false.
func (r Rule) Matches(p model.Path) bool {
	if r.Validate() != nil || model.ValidateLeafPath(p) != nil {
		return false
	}
	switch r.Match {
	case MatchRoot:
		return true
	case MatchExact:
		return r.Prefix.Equal(p)
	case MatchSubtree:
		return r.Prefix.Equal(p) || r.Prefix.IsProperAncestorOf(p)
	default:
		return false
	}
}

// Scope represents the immutable set of authorization rules bound to an Actor for a Resource.
// A zero or empty Scope denies all permissions (fail-closed).
type Scope struct {
	rules []Rule
}

// New constructs a validated Scope from the given rules.
func New(rules ...Rule) (Scope, error) {
	cloned := make([]Rule, len(rules))
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			return Scope{}, err
		}
		cloned[i] = r.Clone()
	}
	return Scope{rules: cloned}, nil
}

// MustNew constructs a Scope from rules and panics if any rule is invalid.
func MustNew(rules ...Rule) Scope {
	s, err := New(rules...)
	if err != nil {
		panic(err)
	}
	return s
}

// Rules returns a defensive copy of the rules in s.
func (s Scope) Rules() []Rule {
	if len(s.rules) == 0 {
		return nil
	}
	out := make([]Rule, len(s.rules))
	for i, r := range s.rules {
		out[i] = r.Clone()
	}
	return out
}

// Validate checks that all rules in s are valid.
func (s Scope) Validate() error {
	for _, r := range s.rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// PermissionsFor computes the effective PermissionSet granted by s on target leaf path p.
// It evaluates each rule on parsed path segments (ADR 0002 §3, Counterexample #15).
func (s Scope) PermissionsFor(p model.Path) PermissionSet {
	if model.ValidateLeafPath(p) != nil {
		return PermNone
	}
	var granted PermissionSet
	for _, r := range s.rules {
		if r.Matches(p) {
			granted = granted.Union(r.Perms)
		}
	}
	return granted
}

// Allows reports whether s grants all permissions in required on target leaf path p.
func (s Scope) Allows(p model.Path, required PermissionSet) bool {
	if !required.IsValid() || model.ValidateLeafPath(p) != nil {
		return false
	}
	return s.PermissionsFor(p).Has(required)
}

// Authorize checks whether s grants all required permissions on p.
// If any required permission is missing, it returns a K401 PolicyDenied diagnostic.
func (s Scope) Authorize(p model.Path, required PermissionSet) error {
	if err := model.ValidateLeafPath(p); err != nil {
		return err
	}
	if err := validatePermSet(required, p); err != nil {
		return err
	}
	granted := s.PermissionsFor(p)
	if granted.Has(required) {
		return nil
	}
	missing := granted.Missing(required)
	return model.Diagnostic{
		Code:       model.CodePolicyDenied,
		Path:       p.Clone(),
		Reason:     fmt.Sprintf("path %s requires %s permission (granted: %s, missing: %s)", p.String(), required.String(), granted.String(), missing.String()),
		NextAction: fmt.Sprintf("grant %s permission on %s in host Scope policy", missing.String(), p.String()),
	}
}

// AuthorizeAction checks whether s grants the normative permissions required for action on p (ADR 0002 §3).
func (s Scope) AuthorizeAction(p model.Path, action ActionKind) error {
	if err := model.ValidateLeafPath(p); err != nil {
		return err
	}
	req, err := RequiredPermissions(action)
	if err != nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   p.Clone(),
			Reason: err.Error(),
		}
	}
	granted := s.PermissionsFor(p)
	if granted.Has(req) {
		return nil
	}
	missing := granted.Missing(req)
	return model.Diagnostic{
		Code:       model.CodePolicyDenied,
		Path:       p.Clone(),
		Reason:     fmt.Sprintf("action %s on %s requires %s permission (granted: %s, missing: %s)", action.String(), p.String(), req.String(), granted.String(), missing.String()),
		NextAction: fmt.Sprintf("grant %s permission on %s in host Scope policy", missing.String(), p.String()),
	}
}

func validatePermSet(perms PermissionSet, p model.Path) error {
	if !perms.IsValid() || perms.IsEmpty() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   slices.Clone(p),
			Reason: "permission set must be non-empty and contain only Set, Delete, Own, or Resolve",
		}
	}
	return nil
}
