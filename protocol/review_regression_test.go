package protocol_test

import (
	"bytes"
	"errors"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// #45: failed quarantine persistence poisons the runtime before another call
// can recover the correctly written target and forget the damaged sibling.
func TestReviewFailedQuarantineStopsSubsequentCalls(t *testing.T) {
	for _, status := range []protocol.CommitStatus{protocol.CommitFailed, protocol.CommitIndeterminate} {
		t.Run(status.String(), func(t *testing.T) {
			_, p, locks := setupFileProvider(t, "@lkal \"0.1\"\nsibling = 7\n")
			repo := protocol.NewMemoryStoreRepository(nil, nil)
			repo.OnSaveQuarantine = func(*protocol.QuarantineTable) *protocol.CommitResult { return &protocol.CommitResult{Status: status} }
			p.Hooks.MutatePatchedBytesForTest = func(_ ownership.ResourceID, _ string, b []byte) []byte {
				return bytes.ReplaceAll(b, []byte("sibling = 7"), []byte("sibling = 8"))
			}
			req := protocol.ReconcileRequest{StoreID: testStoreID, Resource: testResID, Actor: actorA, Mode: plan.ModeApply, BatchID: "halt", Declaration: mustDecl(t, "fresh = 2\n"), Scope: fullScope(t), Provider: p, StoreRepo: repo, Locks: locks}
			first, err := protocol.ExecuteReconcile(req)
			if err != nil {
				t.Fatal(err)
			}
			if !first.NormalExecution.HaltRuntime || !first.NormalExecution.ContractViolation {
				t.Fatalf("not halted: %+v", first.NormalExecution)
			}
			p.Hooks.MutatePatchedBytesForTest = nil
			for _, actor := range []ownership.ActorID{actorA, actorB} {
				req.Actor = actor
				if _, err := protocol.ExecuteReconcile(req); !errors.Is(err, protocol.ErrRuntimeHalted) {
					t.Fatalf("actor %s resumed after failed quarantine: %v", actor, err)
				}
			}
			_, err = protocol.ExecuteExplicitResolution(protocol.ExplicitResolutionExecutionRequest{StoreID: testStoreID, Resource: testResID, Actor: actorA, Action: scope.ActionExplicitRelease, Path: model.MustParsePath("/fresh"), Declaration: req.Declaration, Scope: req.Scope, Provider: p, StoreRepo: repo, Locks: locks})
			if !errors.Is(err, protocol.ErrRuntimeHalted) {
				t.Fatalf("explicit resolution resumed halted runtime: %v", err)
			}
		})
	}
}

type failingObserver struct {
	provider.Provider
	count, failAt int
	applies       int
}

func (p *failingObserver) Observe(res ownership.ResourceID, paths ...model.Path) (*observation.Snapshot, error) {
	p.count++
	if p.count == p.failAt {
		return nil, errors.New("injected observation failure")
	}
	return p.Provider.Observe(res, paths...)
}
func (p *failingObserver) ApplyBatch(res ownership.ResourceID, id string, ops []plan.EnvironmentOp) provider.Receipt {
	p.applies++
	return p.Provider.ApplyBatch(res, id, ops)
}

func TestReviewFinalObservationFailureCannotClaimConvergence(t *testing.T) {
	_, p, locks := setupFileProvider(t, "@lkal \"0.1\"\nx = 1\n")
	e, _ := ownership.NewSettledModified(actorA, lkal.Int(1), 1)
	store, _ := ownership.NewStore().Put(testResID, model.MustParsePath("/x"), e)
	wrapper := &failingObserver{Provider: p, failAt: 2}
	r, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{StoreID: testStoreID, Resource: testResID, Actor: actorA, Mode: plan.ModeApply, BatchID: "noop", Declaration: mustDecl(t, "x = 1\n"), Scope: fullScope(t), Provider: wrapper, StoreRepo: protocol.NewMemoryStoreRepository(store, nil), Locks: locks})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Convergence != plan.ConvergenceUnknown || r.Summary.Counts.ObservationUnavailable != 1 {
		t.Fatalf("final failure hidden: %+v", r.Summary)
	}
}

// #4, #31: an unavailable pre-apply snapshot must suppress all environment ops.
func TestReviewPreApplyObservationFailureDoesNotReuseOldSnapshot(t *testing.T) {
	_, p, locks := setupFileProvider(t, "@lkal \"0.1\"\nx = 1\n")
	wrapper := &failingObserver{Provider: p, failAt: 2}
	repo := protocol.NewMemoryStoreRepository(nil, nil)
	r, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{StoreID: testStoreID, Resource: testResID, Actor: actorA, Mode: plan.ModeApply, BatchID: "precheck", Declaration: mustDecl(t, "fresh = 2\n"), Scope: fullScope(t), Provider: wrapper, StoreRepo: repo, Locks: locks})
	if err != nil {
		t.Fatal(err)
	}
	if wrapper.applies != 0 || r.Summary.Execution != protocol.SummaryBlocked {
		t.Fatalf("applied stale observations: calls=%d summary=%+v", wrapper.applies, r.Summary)
	}
	store, err := repo.LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Paths(testResID)) != 0 {
		t.Fatal("pending not rolled back")
	}
}
