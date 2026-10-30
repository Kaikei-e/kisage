package protocol

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
)

// QuarantineRecord represents the persistent quarantine state on a Resource after a Provider
// contract violation (K501 -> K503) in Step 5–6 (ADR 0004 §2 Step 6, §4, Counterexample #45).
type QuarantineRecord struct {
	Resource    ownership.ResourceID
	BatchID     string
	Reason      string
	Diagnostics []model.Diagnostic
}

// Clone returns a deep defensive copy of q.
func (q QuarantineRecord) Clone() QuarantineRecord {
	return QuarantineRecord{
		Resource:    q.Resource,
		BatchID:     q.BatchID,
		Reason:      q.Reason,
		Diagnostics: slices.Clone(q.Diagnostics),
	}
}

// QuarantineTable is an immutable map of quarantined Resources.
type QuarantineTable struct {
	records map[ownership.ResourceID]QuarantineRecord
}

// NewQuarantineTable constructs an empty QuarantineTable.
func NewQuarantineTable() *QuarantineTable {
	return &QuarantineTable{
		records: make(map[ownership.ResourceID]QuarantineRecord),
	}
}

func (t *QuarantineTable) Resources() []ownership.ResourceID {
	if t == nil {
		return nil
	}
	out := make([]ownership.ResourceID, 0, len(t.records))
	for res := range t.records {
		out = append(out, res)
	}
	slices.Sort(out)
	return out
}

// Clone returns a deep defensive copy of t.
func (t *QuarantineTable) Clone() *QuarantineTable {
	out := NewQuarantineTable()
	if t == nil {
		return out
	}
	for resID, rec := range t.records {
		out.records[resID] = rec.Clone()
	}
	return out
}

// IsQuarantined reports whether resID is currently quarantined (K503).
func (t *QuarantineTable) IsQuarantined(resID ownership.ResourceID) bool {
	if t == nil {
		return false
	}
	_, ok := t.records[resID]
	return ok
}

// Get returns the QuarantineRecord for resID if quarantined.
func (t *QuarantineTable) Get(resID ownership.ResourceID) (QuarantineRecord, bool) {
	if t == nil {
		return QuarantineRecord{}, false
	}
	rec, ok := t.records[resID]
	if !ok {
		return QuarantineRecord{}, false
	}
	return rec.Clone(), true
}

// Quarantine returns a new QuarantineTable with rec recorded for rec.Resource.
func (t *QuarantineTable) Quarantine(rec QuarantineRecord) (*QuarantineTable, error) {
	if err := ownership.ValidateResourceID(rec.Resource); err != nil {
		return nil, err
	}
	next := t.Clone()
	next.records[rec.Resource] = rec.Clone()
	return next, nil
}

// Clear returns a new QuarantineTable with any quarantine record for resID removed.
func (t *QuarantineTable) Clear(resID ownership.ResourceID) *QuarantineTable {
	next := t.Clone()
	delete(next.records, resID)
	return next
}

// BuildContractViolationQuarantine constructs the QuarantineRecord and combined K501 + K503 diagnostics
// when post-apply in-lock contract verification fails (ADR 0004 §2 Step 6, ADR 0005 §2, Counterexamples #18, #45).
func BuildContractViolationQuarantine(
	resID ownership.ResourceID,
	batchID string,
	contractDiags []model.Diagnostic,
) (QuarantineRecord, []model.Diagnostic) {
	diags := make([]model.Diagnostic, 0, len(contractDiags)+2)
	if len(contractDiags) == 0 {
		diags = append(diags, model.Diagnostic{
			Code:       model.CodeProviderContractViolation,
			Reason:     fmt.Sprintf("provider contract violation detected on resource %q in batch %q", resID, batchID),
			NextAction: plan.NextActionInspectQuarantine,
		})
	} else {
		diags = append(diags, slices.Clone(contractDiags)...)
	}

	quarantineDiag := model.Diagnostic{
		Code:       model.CodeResourceQuarantined,
		Reason:     fmt.Sprintf("resource %q quarantined after Provider contract violation in batch %q; all Actor modifications and automatic recovery are prohibited", resID, batchID),
		NextAction: plan.NextActionInspectQuarantine,
	}
	diags = append(diags, quarantineDiag)
	model.SortDiagnostics(diags)

	rec := QuarantineRecord{
		Resource:    resID,
		BatchID:     batchID,
		Reason:      quarantineDiag.Reason,
		Diagnostics: slices.Clone(diags),
	}
	return rec, diags
}
