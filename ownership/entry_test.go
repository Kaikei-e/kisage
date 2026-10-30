package ownership_test

import (
	"errors"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
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

// TestOriginNoneOnlyValidWhilePending verifies ADR 0002 §2:
//   - Origin=None is valid ONLY during a Pending Set reservation from Absent.
//   - Settled Origin=None (Pending == nil) or Origin=None with LastApplied != nil is rejected.
func TestOriginNoneOnlyValidWhilePending(t *testing.T) {
	actor := ownership.ActorID("agent-a")

	validPendingCreate, err := ownership.NewPendingCreate(actor, "batch-1", lkal.Int(10), 1)
	if err != nil {
		t.Fatalf("NewPendingCreate failed: %v", err)
	}
	if validPendingCreate.Origin != ownership.OriginNone || !validPendingCreate.HasPending() || validPendingCreate.CanAutoDelete() {
		t.Fatalf("unexpected pending create state: %+v", validPendingCreate)
	}

	invalidEntries := []struct {
		name  string
		entry ownership.OwnerEntry
	}{
		{
			name: "settled Origin=None without Pending",
			entry: ownership.OwnerEntry{
				Manager: actor,
				Origin:  ownership.OriginNone,
			},
		},
		{
			name: "Origin=None with LastApplied set",
			entry: ownership.OwnerEntry{
				Manager:     actor,
				Origin:      ownership.OriginNone,
				LastApplied: lkal.Int(1),
				Pending:     validPendingCreate.Pending.Clone(),
			},
		},
		{
			name: "Origin=None with PendingOpDelete",
			entry: ownership.OwnerEntry{
				Manager: actor,
				Origin:  ownership.OriginNone,
				Pending: &ownership.Pending{
					BatchID:  "b1",
					Op:       ownership.PendingOpDelete,
					Expected: observation.AbsentObserved(),
				},
			},
		},
		{
			name: "Origin=None with non-Absent Expected",
			entry: ownership.OwnerEntry{
				Manager: actor,
				Origin:  ownership.OriginNone,
				Pending: &ownership.Pending{
					BatchID:  "b1",
					Op:       ownership.PendingOpSet,
					Expected: observation.MustPresentLeaf(lkal.Int(1)),
					Target:   lkal.Int(2),
				},
			},
		},
		{
			name: "Origin=Modified with PendingOpDelete",
			entry: ownership.OwnerEntry{
				Manager:     actor,
				Origin:      ownership.OriginModified,
				LastApplied: lkal.Int(1),
				Pending: &ownership.Pending{
					BatchID:  "b1",
					Op:       ownership.PendingOpDelete,
					Expected: observation.MustPresentLeaf(lkal.Int(1)),
					Previous: &ownership.SettledState{
						Origin:      ownership.OriginModified,
						LastApplied: lkal.Int(1),
					},
				},
			},
		},
	}

	for _, tc := range invalidEntries {
		t.Run(tc.name, func(t *testing.T) {
			assertDiagnosticCode(t, tc.entry.Validate(), model.CodeInvalidInput)
		})
	}
}

// TestCounterexample34_CreatedHistoryInvariantAcrossUpdates verifies ADR 0002 §2 and Counterexample #34:
//   - Confirmed creation from Absent sets Origin=Created and grants automatic deletion right.
//   - Subsequent value updates by the same Actor preserve Origin=Created and update LastApplied.
//   - Matching adoption sets Origin=Modified and never grants automatic deletion right even after updates.
func TestCounterexample34_CreatedHistoryInvariantAcrossUpdates(t *testing.T) {
	actor := ownership.ActorID("agent-a")

	// 1. Initial creation reservation (Absent -> Present(10))
	intent, err := ownership.NewPendingCreate(actor, "batch-1", lkal.Int(10), 1)
	if err != nil {
		t.Fatal(err)
	}
	created, err := ownership.CommitConfirmedSet(intent, 2)
	if err != nil {
		t.Fatal(err)
	}
	if created.Origin != ownership.OriginCreated || !created.CanAutoDelete() {
		t.Fatalf("expected settled Origin=Created with auto-delete right, got %+v", created)
	}
	if !model.EqualLeafValues(created.LastApplied, lkal.Int(10)) {
		t.Fatalf("expected LastApplied=10, got %v", created.LastApplied)
	}

	// 2. Subsequent update by the same Actor (10 -> 20) preserves Origin=Created.
	updIntent, err := ownership.BeginSetUpdate(created, "batch-2", lkal.Int(20), 3)
	if err != nil {
		t.Fatal(err)
	}
	if updIntent.Origin != ownership.OriginCreated || !model.EqualLeafValues(updIntent.LastApplied, lkal.Int(10)) {
		t.Fatalf("BeginSetUpdate must preserve settled Origin and LastApplied during intent: %+v", updIntent)
	}
	updated, err := ownership.CommitConfirmedSet(updIntent, 4)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Origin != ownership.OriginCreated || !updated.CanAutoDelete() {
		t.Fatalf("expected Origin=Created to be preserved after update, got %+v", updated)
	}
	if !model.EqualLeafValues(updated.LastApplied, lkal.Int(20)) {
		t.Fatalf("expected LastApplied=20, got %v", updated.LastApplied)
	}

	// 3. Adopted Modified entry updated from 10 -> 20 remains Origin=Modified without auto-delete right.
	modified, err := ownership.NewSettledModified(actor, lkal.Int(10), 1)
	if err != nil {
		t.Fatal(err)
	}
	if modified.CanAutoDelete() {
		t.Fatal("Origin=Modified must never grant automatic deletion right")
	}
	modUpdIntent, err := ownership.BeginSetUpdate(modified, "batch-3", lkal.Int(20), 2)
	if err != nil {
		t.Fatal(err)
	}
	modUpdated, err := ownership.CommitConfirmedSet(modUpdIntent, 3)
	if err != nil {
		t.Fatal(err)
	}
	if modUpdated.Origin != ownership.OriginModified || modUpdated.CanAutoDelete() {
		t.Fatalf("expected Origin=Modified to remain Modified after update, got %+v", modUpdated)
	}
}

// TestCounterexamples06And07_CrashRecoveryOriginSemantics verifies Counterexamples #6 and #7 (ADR 0002 §2, ADR 0004 §3):
//   - #6: Recovering a Pending creation (Origin=None) when Target is observed downgrades to Origin=Modified and emits K601.
//   - #7: Recovering a Pending update on an existing Created/Modified entry preserves its Origin without emitting K601.
func TestCounterexamples06And07_CrashRecoveryOriginSemantics(t *testing.T) {
	actor := ownership.ActorID("agent-a")
	p := model.MustParsePath("/app/replicas")

	// Counterexample #6: Origin=None downgraded to Modified with K601
	pendingCreate, err := ownership.NewPendingCreate(actor, "batch-create", lkal.Int(3), 1)
	if err != nil {
		t.Fatal(err)
	}
	recoveredCreate, warn, err := ownership.CommitRecoveredSet(pendingCreate, p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if warn == nil || warn.Code != model.CodeCreatedOriginUnverified {
		t.Fatalf("expected K601 warning on recovered creation, got %+v", warn)
	}
	if recoveredCreate.Origin != ownership.OriginModified || recoveredCreate.CanAutoDelete() {
		t.Fatalf("expected recovered creation to downgrade to Modified without auto-delete right, got %+v", recoveredCreate)
	}

	// Counterexample #7: Existing Origin=Created preserved on recovery
	settledCreated, err := ownership.NewSettledCreated(actor, lkal.Int(3), 1)
	if err != nil {
		t.Fatal(err)
	}
	pendingUpdate, err := ownership.BeginSetUpdate(settledCreated, "batch-update", lkal.Int(5), 2)
	if err != nil {
		t.Fatal(err)
	}
	recoveredUpdate, warn, err := ownership.CommitRecoveredSet(pendingUpdate, p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if warn != nil {
		t.Fatalf("expected no K601 warning when recovering existing Created entry, got %+v", warn)
	}
	if recoveredUpdate.Origin != ownership.OriginCreated || !recoveredUpdate.CanAutoDelete() {
		t.Fatalf("expected Origin=Created to be preserved, got %+v", recoveredUpdate)
	}
	if !model.EqualLeafValues(recoveredUpdate.LastApplied, lkal.Int(5)) {
		t.Fatalf("expected LastApplied=5, got %v", recoveredUpdate.LastApplied)
	}
}

// TestCounterexamples36And38_ExplicitAdoptAndReleaseTransitions verifies ADR 0002 §2 and ADR 0003 §3:
//   - Explicit adopt sets Origin=Modified and emits K602 when downgrading from Origin=Created.
//   - Explicit release emits K602 when releasing Origin=Created.
//   - Both reject foreign-owned or Pending entries.
func TestCounterexamples36And38_ExplicitAdoptAndReleaseTransitions(t *testing.T) {
	actorA := ownership.ActorID("agent-a")
	actorB := ownership.ActorID("agent-b")
	p := model.MustParsePath("/db/host")

	created, err := ownership.NewSettledCreated(actorA, lkal.String("old.local"), 1)
	if err != nil {
		t.Fatal(err)
	}

	// Actor B cannot adopt or release Actor A's entry (#38).
	_, _, err = ownership.TransitionExplicitAdopt(&created, actorB, p, lkal.String("new.local"), 2)
	assertDiagnosticCode(t, err, model.CodeOwnershipConflict)
	_, err = ownership.TransitionExplicitRelease(&created, actorB, p)
	assertDiagnosticCode(t, err, model.CodeOwnershipConflict)

	// Actor A explicitly adopts drifted Created entry -> downgraded to Modified with K602 (#36).
	adopted, notice, err := ownership.TransitionExplicitAdopt(&created, actorA, p, lkal.String("new.local"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if notice == nil || notice.Code != model.CodeExplicitDeletionRightWaived {
		t.Fatalf("expected K602 notice when downgrading Created on adopt, got %+v", notice)
	}
	if adopted.Origin != ownership.OriginModified || adopted.CanAutoDelete() {
		t.Fatalf("expected adopted entry to be Origin=Modified, got %+v", adopted)
	}

	// Actor A explicitly releases Created entry -> emits K602.
	relNotice, err := ownership.TransitionExplicitRelease(&created, actorA, p)
	if err != nil {
		t.Fatal(err)
	}
	if relNotice == nil || relNotice.Code != model.CodeExplicitDeletionRightWaived {
		t.Fatalf("expected K602 notice when releasing Created entry, got %+v", relNotice)
	}
}
