package structure

import (
	"fmt"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

// AncestorObservation pairs a proper ancestor path q ≺ p with its recorded observation o(q).
type AncestorObservation struct {
	Path     model.Path
	Observed observation.Observed
}

// PathCheckResult holds the structural and observation check outcome for a target path p
// and all of its proper ancestors q ≺ p (ADR 0001 §4).
type PathCheckResult struct {
	Path        model.Path
	Target      observation.Observed
	Ancestors   []AncestorObservation
	Diagnostics []model.Diagnostic
	PrimaryCode model.DiagnosticCode
}

// HasUnknown reports whether the target path or any of its proper ancestors has an Unknown observation (K301).
func (r PathCheckResult) HasUnknown() bool {
	return hasDiagnosticWithCode(r.Diagnostics, model.CodeObservationUnavailable)
}

// HasStructureConflict reports whether a Leaf/Record structural conflict was detected on the target or an ancestor (K202).
func (r PathCheckResult) HasStructureConflict() bool {
	return hasDiagnosticWithCode(r.Diagnostics, model.CodeStructureConflict)
}

// HasUnrepresentable reports whether the target or any proper ancestor is Present(Opaque) (K302).
func (r PathCheckResult) HasUnrepresentable() bool {
	return hasDiagnosticWithCode(r.Diagnostics, model.CodeUnrepresentable)
}

// PrimaryDiagnostic returns the highest-priority diagnostic on Path according to ADR 0001 §4 and ADR 0003 §1
// (K301 ObservationUnavailable takes precedence over K202 StructureConflict and K302 Unrepresentable).
func (r PathCheckResult) PrimaryDiagnostic() (model.Diagnostic, bool) {
	if len(r.Diagnostics) == 0 {
		return model.Diagnostic{}, false
	}
	for _, d := range r.Diagnostics {
		if d.Code == model.CodeObservationUnavailable {
			return d, true
		}
	}
	return r.Diagnostics[0], true
}

// Err returns the primary Diagnostic as an error if any diagnostic was recorded, or nil otherwise.
func (r PathCheckResult) Err() error {
	if d, ok := r.PrimaryDiagnostic(); ok {
		return d
	}
	return nil
}

// CanSet reports whether Set(p, v) is structurally permissible under ADR 0001 §4:
//   - No Unknown, StructureConflict, or Unrepresentable diagnostics exist on p or any q ≺ p.
//   - Every proper ancestor q ≺ p is either Absent (allowing implicit intermediate Record creation)
//     or Present(Record).
//   - Target p is either Absent or Present(Leaf).
//   - Observation tree is monotonic (no Present node exists beneath an Absent ancestor).
func (r PathCheckResult) CanSet() bool {
	if len(r.Path) == 0 || len(r.Diagnostics) > 0 {
		return false
	}
	if !r.Target.IsAbsent() && !r.Target.IsLeaf() {
		return false
	}
	seenAbsent := false
	for _, anc := range r.Ancestors {
		if anc.Observed.IsAbsent() {
			seenAbsent = true
			continue
		}
		if !anc.Observed.IsRecord() || seenAbsent {
			return false
		}
	}
	return !(seenAbsent && r.Target.IsPresent())
}

// CanDelete reports whether Delete(p) is structurally permissible under ADR 0001 §4:
//   - No Unknown, StructureConflict, or Unrepresentable diagnostics exist on p or any q ≺ p.
//   - Every proper ancestor q ≺ p is Present(Record).
//   - Target p is Present(Leaf).
func (r PathCheckResult) CanDelete() bool {
	if len(r.Path) == 0 || len(r.Diagnostics) > 0 || !r.Target.IsLeaf() {
		return false
	}
	for _, anc := range r.Ancestors {
		if !anc.Observed.IsRecord() {
			return false
		}
	}
	return true
}

// InspectPath evaluates the target path p and all of its proper ancestors q ≺ p against Snapshot.
//
// In accordance with ADR 0001 §4 (and Counterexamples #3 and #10), InspectPath NEVER skips
// proper ancestor checks even when the target path itself is observed as Absent.
func InspectPath(snap *observation.Snapshot, p model.Path) PathCheckResult {
	if err := model.ValidateLeafPath(p); err != nil {
		diag := err.(model.Diagnostic)
		return PathCheckResult{
			Path:        p.Clone(),
			Diagnostics: []model.Diagnostic{diag},
			PrimaryCode: model.CodeInvalidInput,
		}
	}

	ancObs, diags, firstAbsent := inspectAncestors(snap, p)
	targetObs := snap.Get(p)
	diags = append(diags, evaluateTarget(p, targetObs, firstAbsent)...)

	res := PathCheckResult{
		Path:        p.Clone(),
		Target:      targetObs,
		Ancestors:   ancObs,
		Diagnostics: diags,
	}
	if primary, ok := res.PrimaryDiagnostic(); ok {
		res.PrimaryCode = primary.Code
	}
	return res
}

// ValidateSet checks whether Set(p, v) is valid under ADR 0001 §1 and §4.
func ValidateSet(snap *observation.Snapshot, p model.Path, v lkal.Value) error {
	if err := model.ValidateLeafPath(p); err != nil {
		return err
	}
	if _, err := model.ValidateLeafValue(v); err != nil {
		return attachPathToError(err, p)
	}
	res := InspectPath(snap, p)
	if err := res.Err(); err != nil {
		return err
	}
	if !res.CanSet() {
		return model.Diagnostic{
			Code:   model.CodeStructureConflict,
			Path:   p.Clone(),
			Reason: fmt.Sprintf("Set(%s) rejected by structural protection rules", p.String()),
		}
	}
	return nil
}

// ValidateDelete checks whether Delete(p) is valid under ADR 0001 §4.
func ValidateDelete(snap *observation.Snapshot, p model.Path) error {
	res := InspectPath(snap, p)
	if err := res.Err(); err != nil {
		return err
	}
	if !res.CanDelete() {
		return model.Diagnostic{
			Code:   model.CodeStructureConflict,
			Path:   p.Clone(),
			Reason: fmt.Sprintf("Delete(%s) requires an existing Leaf with Present(Record) ancestors", p.String()),
		}
	}
	return nil
}

func inspectAncestors(snap *observation.Snapshot, target model.Path) ([]AncestorObservation, []model.Diagnostic, model.Path) {
	ancPaths := target.Ancestors()
	ancObs := make([]AncestorObservation, len(ancPaths))
	var diags []model.Diagnostic
	var firstAbsent model.Path

	for i, q := range ancPaths {
		oq := snap.Get(q)
		ancObs[i] = AncestorObservation{Path: q, Observed: oq}
		diags = append(diags, evaluateAncestor(target, q, oq, firstAbsent)...)
		if oq.IsAbsent() && len(firstAbsent) == 0 {
			firstAbsent = q
		}
	}
	return ancObs, diags, firstAbsent
}

func evaluateAncestor(target, anc model.Path, oq observation.Observed, firstAbsent model.Path) []model.Diagnostic {
	switch oq.Presence() {
	case observation.PresenceUnknown:
		return []model.Diagnostic{{
			Code:       model.CodeObservationUnavailable,
			Path:       target.Clone(),
			Reason:     fmt.Sprintf("proper ancestor %s observation is Unknown: %s", anc.String(), oq.UnknownReason()),
			NextAction: "resolve observation failure before retrying",
		}}
	case observation.PresencePresent:
		var diags []model.Diagnostic
		if len(firstAbsent) > 0 {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeStructureConflict,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("inconsistent observation: proper ancestor %s is Present under Absent ancestor %s", anc.String(), firstAbsent.String()),
				NextAction: "re-observe resource state consistently",
			})
		}
		if oq.IsLeaf() {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeStructureConflict,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("proper ancestor %s is a Leaf; cannot replace ancestor Leaf with a Record", anc.String()),
				NextAction: "align declaration structure with target resource or migrate ancestor leaf",
			})
		} else if oq.IsOpaque() {
			reason, _ := oq.OpaqueReason()
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeUnrepresentable,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("proper ancestor %s is Opaque: %s", anc.String(), reason),
				NextAction: "inspect unrepresentable ancestor node in target resource",
			})
		}
		return diags
	default:
		return nil
	}
}

func evaluateTarget(target model.Path, targetObs observation.Observed, firstAbsent model.Path) []model.Diagnostic {
	switch targetObs.Presence() {
	case observation.PresenceUnknown:
		return []model.Diagnostic{{
			Code:       model.CodeObservationUnavailable,
			Path:       target.Clone(),
			Reason:     fmt.Sprintf("target path %s observation is Unknown: %s", target.String(), targetObs.UnknownReason()),
			NextAction: "resolve observation failure before retrying",
		}}
	case observation.PresencePresent:
		var diags []model.Diagnostic
		if len(firstAbsent) > 0 {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeStructureConflict,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("inconsistent observation: target %s is Present under Absent ancestor %s", target.String(), firstAbsent.String()),
				NextAction: "re-observe resource state consistently",
			})
		}
		if targetObs.IsRecord() {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeStructureConflict,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("target path %s is an existing Record; cannot overwrite or delete a Record as a Leaf", target.String()),
				NextAction: "declare leaf paths inside the Record instead of targeting the Record itself",
			})
		} else if targetObs.IsOpaque() {
			reason, _ := targetObs.OpaqueReason()
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeUnrepresentable,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("target path %s is Opaque: %s", target.String(), reason),
				NextAction: "inspect unrepresentable target node in target resource",
			})
		}
		return diags
	default:
		return nil
	}
}

func hasDiagnosticWithCode(diags []model.Diagnostic, code model.DiagnosticCode) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func attachPathToError(err error, p model.Path) error {
	if d, ok := err.(model.Diagnostic); ok {
		return d.WithPath(p)
	}
	return err
}
