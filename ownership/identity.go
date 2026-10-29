package ownership

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Kaikei-e/kisage/model"
)

// ActorID identifies an execution principal (Actor) bound by the host environment (ADR 0002 §1, §4).
type ActorID string

// ResourceID identifies a registered target Resource (ADR 0002 §1).
type ResourceID string

// StoreID identifies a shared ownership store instance σ (ADR 0002 §1).
type StoreID string

// ValidateActorID checks that id is a non-empty, valid UTF-8 identifier without control characters.
func ValidateActorID(id ActorID) error {
	return validateIdentifier("actor ID", string(id))
}

// ValidateResourceID checks that id is a non-empty, valid UTF-8 identifier without control characters.
func ValidateResourceID(id ResourceID) error {
	return validateIdentifier("resource ID", string(id))
}

// ValidateStoreID checks that id is a non-empty, valid UTF-8 identifier without control characters.
func ValidateStoreID(id StoreID) error {
	return validateIdentifier("store ID", string(id))
}

func validateIdentifier(label, s string) error {
	if s == "" || strings.TrimSpace(s) == "" {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("%s must not be empty", label),
		}
	}
	if !utf8.ValidString(s) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("%s contains invalid UTF-8", label),
		}
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("%s must not contain control characters", label),
			}
		}
	}
	return nil
}

// Key is the canonical ownership key (ResourceID, PathSegments) in σ (ADR 0002 §1).
type Key struct {
	Resource ResourceID
	Path     model.Path
}

// NewKey constructs and validates an ownership Key.
func NewKey(resID ResourceID, p model.Path) (Key, error) {
	if err := ValidateResourceID(resID); err != nil {
		return Key{}, err
	}
	if err := model.ValidateLeafPath(p); err != nil {
		return Key{}, err
	}
	return Key{
		Resource: resID,
		Path:     p.Clone(),
	}, nil
}

// Validate checks that k has a valid ResourceID and leaf Path.
func (k Key) Validate() error {
	if err := ValidateResourceID(k.Resource); err != nil {
		return err
	}
	return model.ValidateLeafPath(k.Path)
}

// Equal reports whether k and other have the same ResourceID and Path segments.
func (k Key) Equal(other Key) bool {
	return k.Resource == other.Resource && k.Path.Equal(other.Path)
}

// Compare orders keys deterministically by ResourceID and then by Path segment order.
func (k Key) Compare(other Key) int {
	if k.Resource < other.Resource {
		return -1
	}
	if k.Resource > other.Resource {
		return 1
	}
	return k.Path.Compare(other.Path)
}

// String returns a human-readable representation of k.
func (k Key) String() string {
	return fmt.Sprintf("%s:%s", k.Resource, k.Path.String())
}

// ResolveActor returns the host-bound ActorID and ignores any self-declared untrusted
// CLI (`--actor`) or MCP tool argument (ADR 0002 §4, Counterexample #16).
// If hostBound itself is missing or invalid, it fails closed with K101.
func ResolveActor(hostBound ActorID, _ string) (ActorID, error) {
	if err := ValidateActorID(hostBound); err != nil {
		return "", err
	}
	return hostBound, nil
}

// VerifyActorClaim validates hostBound and rejects any non-empty untrustedClaim that
// attempts to impersonate a different Actor with K401 PolicyDenied (ADR 0002 §4, Counterexample #16).
func VerifyActorClaim(hostBound ActorID, untrustedClaim string) (ActorID, error) {
	if err := ValidateActorID(hostBound); err != nil {
		return "", err
	}
	if untrustedClaim != "" && ActorID(untrustedClaim) != hostBound {
		return "", model.Diagnostic{
			Code:       model.CodePolicyDenied,
			Reason:     fmt.Sprintf("untrusted actor claim %q rejected; execution is bound to host actor %q", untrustedClaim, hostBound),
			NextAction: "remove self-declared actor override and use host-bound actor identity",
		}
	}
	return hostBound, nil
}

