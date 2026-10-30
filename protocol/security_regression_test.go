package protocol_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
)

func TestSecurityRegression_GC05_GC06_AdminRecoveryAndRollbackHardening(t *testing.T) {
	p := model.MustParsePath("/app/key")
	settled, err := ownership.NewSettledModified("actor-a", lkal.String("v1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ownership.NewStore().Put("res-1", p, settled)
	if err != nil {
		t.Fatal(err)
	}
	qt := protocol.NewQuarantineTable()
	snap := observation.NewSnapshot()
	snap.Set(model.MustParsePath("/app"), observation.PresentRecord())
	snap.Set(p, observation.MustPresentLeaf(lkal.String("v1")))

	// Settled entry on unquarantined resource must be rejected by ResolveAdminRecovery.
	_, err = protocol.ResolveAdminRecovery(protocol.AdminRecoveryRequest{
		CallerIsAdmin:     true,
		InFlightExecution: false,
		Resource:          "res-1",
		Path:              p,
		Action:            protocol.AdminActionAdoptCurrentObserved,
		Store:             store,
		Quarantine:        qt,
		Snapshot:          snap,
		Stabilize:         provider.StabilizeResult{Outcome: provider.StabilizeDurable},
	})
	if err == nil {
		t.Fatal("expected ResolveAdminRecovery to reject settled entry on unquarantined resource")
	}

	// Pending entry with Origin=Created where environment drifted to an arbitrary third value "v3"
	// must downgrade Origin to OriginModified even if CreationAttributionVerified=true.
	createdBase, _ := ownership.NewSettledCreated("actor-a", lkal.String("v1"), 1)
	pendingUpdate, _ := ownership.BeginSetUpdate(createdBase, "b-1", lkal.String("v2"), 2)
	storePending, _ := ownership.NewStore().Put("res-1", p, pendingUpdate)
	driftedSnap := observation.NewSnapshot()
	driftedSnap.Set(model.MustParsePath("/app"), observation.PresentRecord())
	driftedSnap.Set(p, observation.MustPresentLeaf(lkal.String("v3-foreign")))

	res, err := protocol.ResolveAdminRecovery(protocol.AdminRecoveryRequest{
		CallerIsAdmin:               true,
		InFlightExecution:           false,
		Resource:                    "res-1",
		Path:                        p,
		Action:                      protocol.AdminActionAdoptCurrentObserved,
		CreationAttributionVerified: true,
		Store:                       storePending,
		Quarantine:                  qt,
		Snapshot:                    driftedSnap,
		Stabilize:                   provider.StabilizeResult{Outcome: provider.StabilizeDurable},
	})
	if err != nil {
		t.Fatalf("unexpected error in ResolveAdminRecovery: %v", err)
	}
	aligned, ok := res.NextStore.Get("res-1", p)
	if !ok || aligned.Origin != ownership.OriginModified {
		t.Fatalf("expected OriginModified when aligning to arbitrary third value, got %+v", aligned)
	}

	// RollbackDurableIntentInStore must fail if target path is missing or settled instead of silently skipping.
	intent := &protocol.DurableIntent{
		BatchID: "b-missing",
		EnvironmentOps: []plan.EnvironmentOp{{
			Path:     p,
			Op:       ownership.PendingOpSet,
			Expected: observation.MustPresentLeaf(lkal.String("v1")),
			Target:   lkal.String("v2"),
		}},
	}
	if _, err := protocol.RollbackDurableIntentInStore(store, "res-1", "actor-a", intent); err == nil {
		t.Fatal("expected RollbackDurableIntentInStore to fail when target entry is not Pending")
	}
}
