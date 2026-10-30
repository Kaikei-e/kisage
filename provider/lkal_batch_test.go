package provider_test

import (
	"strings"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// TestCounterexamples01_04_13_28_32_PureLKALBatchAndComments verifies ADR 0004 §2, §5
// and Counterexamples #1, #4, #13, #28, #32.
func TestCounterexamples01_04_13_28_32_PureLKALBatchAndComments(t *testing.T) {
	initialSrc := []byte(`@lkal "0.1"
### Header comment must survive
app {
  ### Sibling comment must survive
  sibling = "keep-me"
  update_me = 10
}
nested {
  inner {
    last_leaf = "remove-me"
  }
}
`)

	pUpdate := model.MustParsePath("/app/update_me")
	pCreate := model.MustParsePath("/app/created")
	pDelete := model.MustParsePath("/nested/inner/last_leaf")

	ops := []plan.EnvironmentOp{
		{
			Path:          pCreate,
			Op:            ownership.PendingOpSet,
			Action:        scope.ActionSet,
			RequiredPerms: scope.NewPermissionSet(scope.PermSet, scope.PermOwn),
			Expected:      observation.AbsentObserved(),
			Target:        lkal.String("hello"),
		},
		{
			Path:          pUpdate,
			Op:            ownership.PendingOpSet,
			Action:        scope.ActionSet,
			RequiredPerms: scope.NewPermissionSet(scope.PermSet, scope.PermOwn),
			Expected:      observation.MustPresentLeaf(lkal.Int(10)),
			Target:        lkal.Int(20),
			Previous: &ownership.SettledState{
				Origin:      ownership.OriginCreated,
				LastApplied: lkal.Int(10),
			},
		},
		{
			Path:          pDelete,
			Op:            ownership.PendingOpDelete,
			Action:        scope.ActionDelete,
			RequiredPerms: scope.NewPermissionSet(scope.PermDelete, scope.PermOwn),
			Expected:      observation.MustPresentLeaf(lkal.String("remove-me")),
			Previous: &ownership.SettledState{
				Origin:      ownership.OriginCreated,
				LastApplied: lkal.String("remove-me"),
			},
		},
	}

	patchedBytes, patchedFile, receipt := provider.ApplyBatchToBytes("target.lkal", initialSrc, "batch-1", ops)
	if receipt.Outcome != provider.ReceiptApplied || patchedFile == nil {
		t.Fatalf("expected ReceiptApplied, got %+v", receipt)
	}

	// Counterexample #1 & ADR 0004 §5: Comments on unchanged items and sibling leaf values are preserved.
	patchedText := string(patchedBytes)
	if !strings.Contains(patchedText, "### Header comment must survive") || !strings.Contains(patchedText, "### Sibling comment must survive") {
		t.Fatalf("expected comments to be preserved in patched LKAL output:\n%s", patchedText)
	}

	afterSnap, err := observation.ObserveFile(patchedFile, pCreate, pUpdate, pDelete, model.MustParsePath("/app/sibling"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := afterSnap.Get(model.MustParsePath("/app/sibling")).LeafValue(); !ok || !model.EqualLeafValues(v, lkal.String("keep-me")) {
		t.Fatalf("Counterexample #1 failed: sibling value not preserved: %v", afterSnap.Get(model.MustParsePath("/app/sibling")))
	}
	// Counterexample #32: Deleting the last leaf inside /nested/inner preserves empty intermediate Records /nested and /nested/inner.
	if !afterSnap.Get(pDelete).IsAbsent() || !afterSnap.Get(model.MustParsePath("/nested/inner")).IsRecord() || !afterSnap.Get(model.MustParsePath("/nested")).IsRecord() {
		t.Fatalf("Counterexample #32 failed: expected /nested/inner to remain Present(Record) and leaf Absent, got %v", afterSnap.Get(model.MustParsePath("/nested/inner")))
	}

	// Counterexample #13: Passing the exact same ops (Expected != Target) a second time without re-planning must return ReceiptNotApplied.
	secondBytes, _, secondReceipt := provider.ApplyBatchToBytes("target.lkal", patchedBytes, "batch-2", ops)
	if secondReceipt.Outcome != provider.ReceiptNotApplied || secondBytes != nil {
		t.Fatalf("Counterexample #13 failed: expected ReceiptNotApplied on 2nd un-replanned call, got %+v", secondReceipt)
	}

	// Counterexample #4 & #28: Multi-leaf batch where one leaf's Expected mismatches fails all-or-nothing with ReceiptNotApplied.
	badOps := []plan.EnvironmentOp{
		ops[0],
		{
			Path:     pUpdate,
			Op:       ownership.PendingOpSet,
			Action:   scope.ActionSet,
			Expected: observation.MustPresentLeaf(lkal.Int(999)), // mismatch
			Target:   lkal.Int(30),
		},
	}
	badBytes, _, badReceipt := provider.ApplyBatchToBytes("target.lkal", initialSrc, "batch-bad", badOps)
	if badReceipt.Outcome != provider.ReceiptNotApplied || badBytes != nil {
		t.Fatalf("Counterexamples #4 & #28 failed: expected ReceiptNotApplied on partial precondition mismatch, got %+v", badReceipt)
	}
}

// TestCounterexamples18_45_VerifyAppliedBatchContractAndStabilize verifies ADR 0004 §2 Step 5
// and Counterexamples #18, #32, #43, #45.
func TestCounterexamples18_45_VerifyAppliedBatchContractAndStabilize(t *testing.T) {
	pSet := model.MustParsePath("/app/port")
	pDel := model.MustParsePath("/app/old")
	pSib := model.MustParsePath("/app/sibling")
	pAnc := model.MustParsePath("/app")

	beforeSnap := observation.NewSnapshot()
	beforeSnap.Set(pAnc, observation.PresentRecord())
	beforeSnap.Set(pSet, observation.AbsentObserved())
	beforeSnap.Set(pDel, observation.MustPresentLeaf(lkal.String("bye")))
	beforeSnap.Set(pSib, observation.MustPresentLeaf(lkal.Int(100)))

	ops := []plan.EnvironmentOp{
		{
			Path:     pSet,
			Op:       ownership.PendingOpSet,
			Expected: observation.AbsentObserved(),
			Target:   lkal.Int(8080),
		},
		{
			Path:     pDel,
			Op:       ownership.PendingOpDelete,
			Expected: observation.MustPresentLeaf(lkal.String("bye")),
		},
	}

	validAfter := observation.NewSnapshot()
	validAfter.Set(pAnc, observation.PresentRecord())
	validAfter.Set(pSet, observation.MustPresentLeaf(lkal.Int(8080)))
	validAfter.Set(pDel, observation.AbsentObserved())
	validAfter.Set(pSib, observation.MustPresentLeaf(lkal.Int(100)))

	if diags := provider.VerifyAppliedBatchContract(beforeSnap, validAfter, ops); len(diags) != 0 {
		t.Fatalf("expected valid contract check to pass, got %+v", diags)
	}

	// Counterexample #18: Provider normalized/transformed Set(8080) to 8081 -> K501 ProviderContractViolation.
	mutatedTarget := observation.NewSnapshot()
	mutatedTarget.Set(pAnc, observation.PresentRecord())
	mutatedTarget.Set(pSet, observation.MustPresentLeaf(lkal.Int(8081)))
	mutatedTarget.Set(pDel, observation.AbsentObserved())
	mutatedTarget.Set(pSib, observation.MustPresentLeaf(lkal.Int(100)))
	if diags := provider.VerifyAppliedBatchContract(beforeSnap, mutatedTarget, ops); len(diags) == 0 || diags[0].Code != model.CodeProviderContractViolation {
		t.Fatalf("Counterexample #18 failed: expected K501 on transformed target value, got %+v", diags)
	}

	// Counterexample #45: Provider corrupted sibling value /app/sibling -> K501 ProviderContractViolation.
	corruptedSibling := observation.NewSnapshot()
	corruptedSibling.Set(pAnc, observation.PresentRecord())
	corruptedSibling.Set(pSet, observation.MustPresentLeaf(lkal.Int(8080)))
	corruptedSibling.Set(pDel, observation.AbsentObserved())
	corruptedSibling.Set(pSib, observation.MustPresentLeaf(lkal.Int(999)))
	if diags := provider.VerifyAppliedBatchContract(beforeSnap, corruptedSibling, ops); len(diags) == 0 || diags[0].Code != model.CodeProviderContractViolation {
		t.Fatalf("Counterexample #45 failed: expected K501 on sibling corruption, got %+v", diags)
	}

	// EvaluateStabilize checks: DurableSnapshot, Conflict, and Indeterminate.
	stabOK := provider.EvaluateStabilize(validAfter, validAfter, []model.Path{pSet, pSib}, nil)
	if stabOK.Outcome != provider.StabilizeDurable {
		t.Fatalf("expected StabilizeDurable, got %+v", stabOK)
	}
	stabConflict := provider.EvaluateStabilize(validAfter, corruptedSibling, []model.Path{pSet, pSib}, nil)
	if stabConflict.Outcome != provider.StabilizeConflict {
		t.Fatalf("expected StabilizeConflict, got %+v", stabConflict)
	}
}
