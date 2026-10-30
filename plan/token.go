package plan

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/structure"
)

const conditionTokenPrefix = "ksg-tok-v1:"

// ConditionToken represents the `--expect TOKEN` condition token returned by `explain`
// and verified by `adopt` and `release` (ADR 0003 §3, Counterexample #38).
//
// A ConditionToken binds deterministically to:
//   - ResourceID
//   - Canonical leaf Path
//   - Current observed state of every proper ancestor q ≺ Path
//   - Current observed state of Target Path (including LKAL value type and canonical value when Present(Leaf))
//
// It is neither an authentication credential nor a proof of physical creation (ABA is not detected by value tokens).
type ConditionToken string

// IsEmpty reports whether t is empty or whitespace-only.
func (t ConditionToken) IsEmpty() bool {
	return strings.TrimSpace(string(t)) == ""
}

// String returns the string representation of t.
func (t ConditionToken) String() string {
	return string(t)
}

// ComputeConditionToken computes the deterministic ConditionToken for (resID, path) from snap (ADR 0003 §3).
// If target path or any proper ancestor observation is Unknown, ComputeConditionToken fails closed with K301.
func ComputeConditionToken(
	resID ownership.ResourceID,
	path model.Path,
	snap *observation.Snapshot,
) (ConditionToken, error) {
	if err := ownership.ValidateResourceID(resID); err != nil {
		return "", err
	}
	if err := model.ValidateLeafPath(path); err != nil {
		return "", err
	}
	if snap == nil {
		return "", model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   path.Clone(),
			Reason: "observation snapshot must not be nil",
		}
	}
	check := structure.InspectPath(snap, path)
	if check.HasUnknown() {
		diag, _ := check.PrimaryDiagnostic()
		return "", diag
	}

	var b strings.Builder
	fmt.Fprintf(&b, "res=%s\npath=%s\n", resID, path.String())
	for _, anc := range check.Ancestors {
		fmt.Fprintf(&b, "anc=%s:%s\n", anc.Path.String(), serializeObservedForToken(anc.Observed))
	}
	fmt.Fprintf(&b, "target=%s:%s\n", path.String(), serializeObservedForToken(check.Target))

	sum := sha256.Sum256([]byte(b.String()))
	return ConditionToken(conditionTokenPrefix + hex.EncodeToString(sum[:])), nil
}

// VerifyConditionToken checks that expected matches the current ConditionToken for (resID, path) in snap (ADR 0003 §3, Counterexample #38).
func VerifyConditionToken(
	expected ConditionToken,
	resID ownership.ResourceID,
	path model.Path,
	snap *observation.Snapshot,
) error {
	if expected.IsEmpty() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   path.Clone(),
			Reason: "condition token (--expect) must not be empty",
		}
	}
	actual, err := ComputeConditionToken(resID, path, snap)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return model.Diagnostic{
			Code:       model.CodeExternalDrift,
			Path:       path.Clone(),
			Reason:     fmt.Sprintf("condition token (--expect) mismatch for %s: observed value or ancestor structure changed since explain", path),
			NextAction: "re-run explain to inspect current state and obtain a fresh --expect token",
		}
	}
	return nil
}

func serializeObservedForToken(obs observation.Observed) string {
	switch obs.Presence() {
	case observation.PresenceAbsent:
		return "Absent"
	case observation.PresencePresent:
		if obs.IsRecord() {
			return "Present(Record)"
		}
		if obs.IsOpaque() {
			reason, _ := obs.OpaqueReason()
			return fmt.Sprintf("Present(Opaque:%q)", reason)
		}
		if v, ok := obs.LeafValue(); ok {
			return fmt.Sprintf("Present(Leaf:%s)", serializeLKALValueForToken(v))
		}
		return "Present(Invalid)"
	default:
		return fmt.Sprintf("Unknown(%q)", obs.UnknownReason())
	}
}

func serializeLKALValueForToken(v lkal.Value) string {
	switch val := v.(type) {
	case lkal.String:
		return fmt.Sprintf("String(%q)", string(val))
	case lkal.Int:
		return fmt.Sprintf("Int(%d)", int64(val))
	case lkal.Decimal:
		return fmt.Sprintf("Decimal(%s)", string(val))
	case lkal.Bool:
		return fmt.Sprintf("Bool(%t)", bool(val))
	case lkal.Enum:
		return fmt.Sprintf("Enum(%s)", string(val))
	case lkal.Quantity:
		return fmt.Sprintf("Quantity(%s,%s)", val.Number, val.Unit)
	case lkal.Ref:
		return fmt.Sprintf("Ref(%s)", model.Path(val).String())
	case lkal.List:
		parts := make([]string, len(val))
		for i, el := range val {
			parts[i] = serializeLKALValueForToken(el)
		}
		return fmt.Sprintf("List[%s]", strings.Join(parts, ","))
	default:
		return fmt.Sprintf("UnknownValue(%v)", v)
	}
}
