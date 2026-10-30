package structure

import (
	"fmt"
	"reflect"
	"slices"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

// ApplyLeafSet applies Set(p, v) to an *lkal.File while enforcing ADR 0001 §4:
//   - Rejects setting a Record or invalid LKAL value as a leaf (K101).
//   - Rejects overwriting an existing Record at p or replacing an existing Leaf ancestor q ≺ p (K202).
//   - Implicitly creates any Absent intermediate Records along q ≺ p.
//   - Verifies the Frame Condition for all non-target leaf paths and existing Records.
func ApplyLeafSet(f *lkal.File, p model.Path, v lkal.Value) error {
	if f == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "nil LKAL file",
		}
	}
	if err := model.ValidateLeafPath(p); err != nil {
		return err
	}
	normVal, err := model.ValidateLeafValue(v)
	if err != nil {
		return attachPathToError(err, p)
	}

	before := f.Data()
	snap := observation.ObserveRecord(before, p)
	if err := ValidateSet(snap, p, normVal); err != nil {
		return err
	}
	// LKAL Set creates missing ancestors via label compression in one edit.
	// Separate ancestor edits would leave partial changes if the final Set failed.
	if err := f.Set(p.LKALPath(), normVal); err != nil {
		return err
	}
	return VerifyFrameCondition(before, f.Data(), p)
}

// ApplyLeafDelete applies Delete(p) to an *lkal.File while enforcing ADR 0001 §4:
//   - Rejects deleting an existing Record at p or traversing a Leaf ancestor q ≺ p (K202).
//   - Deletes only the target leaf at p.
//   - Never auto-cleans intermediate Records that become empty after deleting their last leaf (Counterexample #32).
//   - Verifies the Frame Condition for all non-target leaf paths and existing Records.
func ApplyLeafDelete(f *lkal.File, p model.Path) error {
	if f == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "nil LKAL file",
		}
	}
	if err := model.ValidateLeafPath(p); err != nil {
		return err
	}

	before := f.Data()
	snap := observation.ObserveRecord(before, p)
	if err := ValidateDelete(snap, p); err != nil {
		return err
	}
	if err := f.Delete(p.LKALPath()); err != nil {
		return err
	}
	if err := restoreEmptiedAncestors(f, p); err != nil {
		return err
	}
	return VerifyFrameCondition(before, f.Data(), p)
}

// VerifyFrameCondition checks the ADR 0001 §4 Frame Condition between before and after:
//   - Every leaf path q ∉ targetPaths has the exact same existence and LKAL value in before and after.
//   - Every path r that was Present(Record) in before remains Present(Record) in after
//     (existing Records are neither turned into Leaves nor auto-deleted when emptied).
//   - Opaque values retain their original contents and reachability. Their diagnostic
//     reason alone is insufficient to establish preservation.
func VerifyFrameCondition(before, after lkal.Record, targetPaths ...model.Path) error {
	targetSet := buildPathSet(targetPaths)
	beforeSnap := observation.ObserveRecord(before, targetPaths...)
	afterSnap := observation.ObserveRecord(after, targetPaths...)

	for _, p := range collectUnionPaths(beforeSnap, afterSnap) {
		isTarget := targetSet[p.String()]
		bObs, aObs := beforeSnap.Get(p), afterSnap.Get(p)
		if bObs.IsOpaque() || aObs.IsOpaque() {
			bVal, bExists := recordValueAtPath(before, p)
			aVal, aExists := recordValueAtPath(after, p)
			// Equal handles LKAL value trees, including invalid scalar/list contents.
			// DeepEqual also recognizes unchanged unsupported Go forms, such as a
			// typed nil pointer implementing lkal.Value, without treating their
			// shared diagnostic reason as evidence of content equality.
			if !bObs.IsOpaque() || !aObs.IsOpaque() || !bExists || !aExists ||
				(!safeLKALEqual(bVal, aVal, 0) && !reflect.DeepEqual(bVal, aVal)) {
				return model.Diagnostic{
					Code:   model.CodeStructureConflict,
					Path:   p.Clone(),
					Reason: fmt.Sprintf("frame condition violated: Opaque value at %s changed or lost reachability", p.String()),
				}
			}
			continue
		}
		if err := verifyPathFrame(p, bObs, aObs, isTarget, targetPaths, afterSnap); err != nil {
			return err
		}
	}
	return nil
}

func safeLKALEqual(a, b lkal.Value, depth int) bool {
	if depth > 64 {
		return false
	}
	la, oka := a.(lkal.List)
	lb, okb := b.(lkal.List)
	if oka || okb {
		if !oka || !okb || len(la) != len(lb) {
			return false
		}
		for i := range la {
			if !safeLKALEqual(la[i], lb[i], depth+1) {
				return false
			}
		}
		return true
	}
	return lkal.Equal(a, b)
}

func recordValueAtPath(rec lkal.Record, p model.Path) (lkal.Value, bool) {
	var cur lkal.Value = rec
	for _, seg := range p {
		record, ok := cur.(lkal.Record)
		if !ok {
			return nil, false
		}
		found := false
		for _, field := range record.Fields {
			if field.Key == seg {
				// Match ObserveRecord's last-matching-field semantics on duplicate keys.
				cur, found = field.Value, true
			}
		}
		if !found {
			return nil, false
		}
	}
	return cur, true
}

func restoreEmptiedAncestors(f *lkal.File, p model.Path) error {
	// If the deleted leaf was represented via LKAL label compression (e.g., `a "b" "c" = 1`),
	// lkal.File.Delete removes the entire labeled statement. Restore any proper ancestor
	// that became Absent as an empty Record so that intermediate Records are never auto-cleaned (ADR 0001 §4).
	afterSnap := observation.ObserveRecord(f.Data(), p)
	for _, anc := range p.Ancestors() {
		if afterSnap.Get(anc).IsAbsent() {
			if err := f.Set(anc.LKALPath(), lkal.Record{}); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildPathSet(paths []model.Path) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		if len(p) > 0 {
			set[p.String()] = true
		}
	}
	return set
}

func collectUnionPaths(a, b *observation.Snapshot) []model.Path {
	seen := make(map[string]model.Path)
	for _, p := range a.Paths() {
		seen[p.String()] = p
	}
	for _, p := range b.Paths() {
		seen[p.String()] = p
	}
	out := make([]model.Path, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

func verifyPathFrame(p model.Path, bObs, aObs observation.Observed, isTarget bool, targetPaths []model.Path, afterSnap *observation.Snapshot) error {
	if bObs.IsRecord() && !aObs.IsRecord() {
		return model.Diagnostic{
			Code:   model.CodeStructureConflict,
			Path:   p.Clone(),
			Reason: fmt.Sprintf("frame condition violated: existing Record at %s changed to %s", p.String(), aObs.String()),
		}
	}
	if isTarget {
		if !aObs.IsLeaf() && !aObs.IsAbsent() {
			return model.Diagnostic{
				Code:   model.CodeStructureConflict,
				Path:   p.Clone(),
				Reason: fmt.Sprintf("frame condition violated: target path %s became %s instead of Leaf or Absent", p.String(), aObs.String()),
			}
		}
		return nil
	}
	if bObs.IsLeaf() {
		if !aObs.IsLeaf() {
			return model.Diagnostic{
				Code:   model.CodeStructureConflict,
				Path:   p.Clone(),
				Reason: fmt.Sprintf("frame condition violated: non-target leaf %s was lost or changed kind to %s", p.String(), aObs.String()),
			}
		}
		bVal, _ := bObs.LeafValue()
		aVal, _ := aObs.LeafValue()
		if !model.EqualLeafValues(bVal, aVal) {
			return model.Diagnostic{
				Code:   model.CodeStructureConflict,
				Path:   p.Clone(),
				Reason: fmt.Sprintf("frame condition violated: non-target leaf %s value changed from %v to %v", p.String(), bVal, aVal),
			}
		}
		return nil
	}
	if aObs.IsLeaf() {
		return model.Diagnostic{
			Code:   model.CodeStructureConflict,
			Path:   p.Clone(),
			Reason: fmt.Sprintf("frame condition violated: unexpected non-target leaf %s appeared", p.String()),
		}
	}
	if !bObs.IsRecord() && aObs.IsRecord() {
		if !bObs.IsAbsent() || !isAncestorOfPresentTargetLeaf(p, targetPaths, afterSnap) {
			return model.Diagnostic{
				Code:   model.CodeStructureConflict,
				Path:   p.Clone(),
				Reason: fmt.Sprintf("frame condition violated: unsolicited Record appeared at %s", p.String()),
			}
		}
	}
	return nil
}

func isAncestorOfPresentTargetLeaf(p model.Path, targetPaths []model.Path, afterSnap *observation.Snapshot) bool {
	for _, tp := range targetPaths {
		if p.IsProperAncestorOf(tp) && afterSnap.Get(tp).IsLeaf() {
			return true
		}
	}
	return false
}
