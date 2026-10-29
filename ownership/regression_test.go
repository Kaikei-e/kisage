package ownership_test

import (
	"math"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
)

// Counterexample #38: exhausted revisions must never make old owner tokens valid.
func TestCounterexample38_RevisionExhaustion(t *testing.T) {
	p := model.MustParsePath("/feature/flag")
	entry, err := ownership.NewSettledModified("agent-a", lkal.Bool(true), math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ownership.NewStore().Put("res-main", p, entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, rev := range []uint64{0, math.MaxUint64} {
		update := entry
		update.Revision, update.LastApplied = rev, lkal.Bool(false)
		if next, err := store.Put("res-main", p, update); err == nil || next != nil {
			t.Fatalf("Put at exhausted revision returned (%v, %v)", next, err)
		}
	}
	if next, err := store.Delete("res-main", p); err == nil || next != nil {
		t.Fatalf("Delete at exhausted revision returned (%v, %v)", next, err)
	}
	got, ok := store.Get("res-main", p)
	if !ok || !got.Equal(entry) || store.Revision("res-main", p) != math.MaxUint64 {
		t.Fatal("rejected operations changed the original entry or revision")
	}
	assertDiagnosticCode(t, store.VerifyRevision("res-main", p, 0), model.CodeOwnershipConflict)
}

func TestCounterexample38_LastRevisionAndUnownedTombstone(t *testing.T) {
	p := model.MustParsePath("/feature/flag")
	entry, err := ownership.NewSettledModified("agent-a", lkal.Bool(true), math.MaxUint64-1)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ownership.NewStore().Put("res-main", p, entry)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Put("res-main", p, entry)
	if err != nil || updated.Revision("res-main", p) != math.MaxUint64 {
		t.Fatalf("last representable update failed: %v", err)
	}
	released, err := store.Delete("res-main", p)
	if err != nil || released.Has("res-main", p) || released.Revision("res-main", p) != math.MaxUint64 {
		t.Fatalf("last representable release failed: %v", err)
	}
	noop, err := released.Delete("res-main", p)
	if err != nil || noop.Revision("res-main", p) != math.MaxUint64 {
		t.Fatalf("unowned Delete must stay NoOp at exhausted revision: %v", err)
	}
	entry.Revision = 0
	if next, err := released.Put("res-main", p, entry); err == nil || next != nil {
		t.Fatalf("reacquisition reset exhausted tombstone: (%v, %v)", next, err)
	}
	assertDiagnosticCode(t, released.VerifyRevision("res-main", p, 0), model.CodeOwnershipConflict)
}

func TestProgrammaticOwnershipValuesRemainUsable(t *testing.T) {
	p := model.MustParsePath("/app/value")
	for _, value := range []struct {
		name string
		raw  lkal.Value
	}{
		{"Decimal", lkal.Decimal("1.20")},
		{"Quantity", lkal.Quantity{Number: "1.00", Unit: "Mi"}},
		{"Decimal List", lkal.List{lkal.Decimal("1.20"), lkal.Decimal("2.00")}},
	} {
		t.Run(value.name, func(t *testing.T) {
			norm, err := model.ValidateLeafValue(value.raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, origin := range []ownership.Origin{ownership.OriginCreated, ownership.OriginModified} {
				raw := ownership.OwnerEntry{Manager: "agent-a", Origin: origin, LastApplied: value.raw}
				if err := raw.Validate(); err != nil {
					t.Fatal(err)
				}
				store, err := ownership.NewStore().Put("res-main", p, raw)
				if err != nil {
					t.Fatal(err)
				}
				stored, ok := store.Get("res-main", p)
				if !ok || !lkal.Equal(stored.LastApplied, norm) {
					t.Errorf("Store retained noncanonical value: %v", stored.LastApplied)
				}
				for _, entry := range []ownership.OwnerEntry{raw, stored} {
					pending, err := ownership.BeginSetUpdate(entry, "batch-update", lkal.Int(9), 2)
					if err != nil {
						t.Errorf("accepted ownership entry cannot be updated: %v", err)
						continue
					}
					if !lkal.Equal(pending.LastApplied, norm) || !lkal.Equal(pending.Pending.Previous.LastApplied, norm) {
						t.Error("update retained noncanonical settled values")
					}
					if origin == ownership.OriginCreated {
						if _, err := ownership.BeginDelete(entry, "batch-delete", 2); err != nil {
							t.Errorf("accepted Created entry cannot be deleted: %v", err)
						}
					}
				}
				if !lkal.Equal(raw.LastApplied, value.raw) {
					t.Fatal("normalization mutated caller input")
				}
			}
		})
	}
}

func TestProgrammaticPendingValuesRemainCanonicalAcrossTransitions(t *testing.T) {
	p := model.MustParsePath("/app/value")
	raw := ownership.OwnerEntry{
		Manager: "agent-a", Origin: ownership.OriginCreated, LastApplied: lkal.Decimal("1.20"),
		Pending: &ownership.Pending{
			BatchID: "batch-update", Op: ownership.PendingOpSet,
			Expected: observation.MustPresentLeaf(lkal.Decimal("1.2")), Target: lkal.Decimal("2.00"),
			Previous: &ownership.SettledState{Origin: ownership.OriginCreated, LastApplied: lkal.Decimal("1.200")},
		},
	}
	if err := raw.Validate(); err != nil {
		t.Fatalf("equivalent programmatic settled values rejected: %v", err)
	}
	store, err := ownership.NewStore().Put("res-main", p, raw)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := store.Get("res-main", p)
	if !lkal.Equal(stored.LastApplied, lkal.Decimal("1.2")) ||
		!lkal.Equal(stored.Pending.Previous.LastApplied, lkal.Decimal("1.2")) ||
		!lkal.Equal(stored.Pending.Target, lkal.Decimal("2.0")) {
		t.Fatal("Store did not normalize all pending values")
	}
	confirmed, err := ownership.CommitConfirmedSet(raw, 2)
	if err != nil || !lkal.Equal(confirmed.LastApplied, lkal.Decimal("2.0")) {
		t.Fatalf("confirmed Target is not canonical: %+v, %v", confirmed, err)
	}
	recovered, _, err := ownership.CommitRecoveredSet(raw, p, 2)
	if err != nil || !lkal.Equal(recovered.LastApplied, lkal.Decimal("2.0")) {
		t.Fatalf("recovered Target is not canonical: %+v, %v", recovered, err)
	}
	restored, err := ownership.RollbackPending(raw, 2)
	if err != nil || restored == nil || !lkal.Equal(restored.LastApplied, lkal.Decimal("1.2")) {
		t.Fatalf("rollback Previous value is not canonical: %+v, %v", restored, err)
	}
	if raw.LastApplied != lkal.Decimal("1.20") || raw.Pending.Target != lkal.Decimal("2.00") ||
		raw.Pending.Previous.LastApplied != lkal.Decimal("1.200") {
		t.Fatal("normalization mutated caller's pending entry")
	}
}

func TestNormalizationPreservesPendingGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ownership.OwnerEntry)
	}{
		{"different Previous value", func(e *ownership.OwnerEntry) { e.Pending.Previous.LastApplied = lkal.Decimal("1.21") }},
		{"different Expected type", func(e *ownership.OwnerEntry) { e.Pending.Expected = observation.MustPresentLeaf(lkal.Int(1)) }},
		{"different Previous origin", func(e *ownership.OwnerEntry) { e.Pending.Previous.Origin = ownership.OriginModified }},
		{"normalized no-op Set", func(e *ownership.OwnerEntry) { e.Pending.Target = lkal.Decimal("1.2000") }},
		{"invalid Target", func(e *ownership.OwnerEntry) { e.Pending.Target = lkal.Decimal("invalid") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := ownership.OwnerEntry{
				Manager: "agent-a", Origin: ownership.OriginCreated, LastApplied: lkal.Decimal("1.20"),
				Pending: &ownership.Pending{
					BatchID: "batch", Op: ownership.PendingOpSet,
					Expected: observation.MustPresentLeaf(lkal.Decimal("1.2")), Target: lkal.Decimal("2.00"),
					Previous: &ownership.SettledState{Origin: ownership.OriginCreated, LastApplied: lkal.Decimal("1.200")},
				},
			}
			tc.mutate(&entry)
			assertDiagnosticCode(t, entry.Validate(), model.CodeInvalidInput)
			_, err := ownership.NewStore().Put("res-main", model.MustParsePath("/app/value"), entry)
			assertDiagnosticCode(t, err, model.CodeInvalidInput)
		})
	}
}

// Counterexample #6: normalization must not promote an unverified recovered creation.
func TestCounterexample06_NormalizedCreationRecovery(t *testing.T) {
	entry := ownership.OwnerEntry{
		Manager: "agent-a", Origin: ownership.OriginNone,
		Pending: &ownership.Pending{
			BatchID: "batch-create", Op: ownership.PendingOpSet,
			Expected: observation.AbsentObserved(), Target: lkal.Decimal("2.00"),
		},
	}
	confirmed, err := ownership.CommitConfirmedSet(entry, 2)
	if err != nil || confirmed.Origin != ownership.OriginCreated || !lkal.Equal(confirmed.LastApplied, lkal.Decimal("2.0")) {
		t.Fatalf("confirmed creation is not canonical: %+v, %v", confirmed, err)
	}
	recovered, warning, err := ownership.CommitRecoveredSet(entry, model.MustParsePath("/app/value"), 2)
	if err != nil || recovered.Origin != ownership.OriginModified || recovered.CanAutoDelete() ||
		!lkal.Equal(recovered.LastApplied, lkal.Decimal("2.0")) || warning == nil || warning.Code != model.CodeCreatedOriginUnverified {
		t.Fatalf("recovered creation was not conservatively normalized: %+v, warning=%v, err=%v", recovered, warning, err)
	}
}
