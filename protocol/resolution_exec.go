package protocol

import (
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// ExplicitResolutionExecutionRequest holds the inputs to ExecuteExplicitResolution (`adopt` / `release`).
type ExplicitResolutionExecutionRequest struct {
	StoreID          ownership.StoreID
	Resource         ownership.ResourceID
	Actor            ownership.ActorID
	Action           scope.ActionKind
	Path             model.Path
	ExpectedToken    plan.ConditionToken
	ExpectedRevision uint64
	Declaration      *projection.Declaration
	Scope            scope.Scope
	Provider         provider.Provider
	StoreRepo        StoreRepository
	Locks            provider.LockManager
}

// ExecuteExplicitResolution runs `kisage adopt` or `kisage release` under the canonical
// SharedStore (σ) -> Resource lock order, re-verifying TOKEN, REV, Scope, and ownership in-lock,
// executing Provider.Stabilize, and committing atomically (ADR 0003 §3, ADR 0004 §2, Counterexamples #36–#38, #50).
func ExecuteExplicitResolution(req ExplicitResolutionExecutionRequest) (*ExplicitResolutionResult, error) {
	if err := ownership.ValidateStoreID(req.StoreID); err != nil {
		return nil, err
	}
	if req.Locks == nil || req.StoreRepo == nil || req.Provider == nil {
		return nil, model.Diagnostic{Code: model.CodeInvalidInput, Reason: "Locks, StoreRepo, and Provider must not be nil"}
	}
	if req.Action != scope.ActionExplicitAdopt && req.Action != scope.ActionExplicitRelease {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: "explicit resolution action must be ExplicitAdopt or ExplicitRelease",
		}
	}
	lease, err := req.Locks.Acquire(req.StoreID, req.Resource)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lease.Unlock() }()

	store, qt, err := loadStateInLock(req.StoreRepo)
	if err != nil {
		return nil, err
	}
	queryPaths := collectQueryPaths(req.Declaration, store, req.Resource, req.Actor)
	queryPaths = appendUniquePath(queryPaths, req.Path)
	snap, err := req.Provider.Observe(req.Resource, queryPaths...)
	if err != nil {
		return nil, err
	}
	initiallyQuarantined := qt.IsQuarantined(req.Resource)

	planReq := plan.ResolutionRequest{
		Resource:         req.Resource,
		Actor:            req.Actor,
		Path:             req.Path,
		ExpectedToken:    req.ExpectedToken,
		ExpectedRevision: req.ExpectedRevision,
		Declaration:      req.Declaration,
		Snapshot:         snap,
		Store:            store,
		Scope:            req.Scope,
		Quarantined:      initiallyQuarantined,
	}

	var resPlan *plan.ResolutionPlan
	var planErr error
	if req.Action == scope.ActionExplicitAdopt {
		resPlan, planErr = plan.PlanAdopt(planReq)
	} else {
		resPlan, planErr = plan.PlanRelease(planReq)
	}

	var stab provider.StabilizeResult
	var commit CommitResult
	if planErr == nil && resPlan != nil {
		stab = req.Provider.Stabilize(req.Resource, snap, req.Path)
		if stab.Outcome == provider.StabilizeDurable {
			commit = req.StoreRepo.SaveStore(StageExplicitResolutionCommit, resPlan.NextStore)
		}
	}

	out := EvaluateExplicitResolutionStage(ExplicitResolutionStageInput{
		PlanResult:           resPlan,
		PlanErr:              planErr,
		Stabilize:            stab,
		StoreCommit:          commit,
		BeforeStore:          store,
		Declaration:          req.Declaration,
		Snapshot:             snap,
		Resource:             req.Resource,
		Actor:                req.Actor,
		InitiallyQuarantined: initiallyQuarantined,
	})
	return &out, nil
}

// ReadOnlyStateReader is an optional interface implemented by StoreRepository implementations
// that can load the visible store and quarantine state for read-only `plan` and `explain`
// without performing fsync or writes (ADR 0004 §5.2, ADR 0005 §1).
type ReadOnlyStateReader interface {
	ReadOnlyState() (*ownership.Store, *QuarantineTable, error)
}

// LoadReadOnlyState loads the visible store and quarantine table for read-only `plan` and `explain`
// without writes or fsync when repo implements ReadOnlyStateReader.
func LoadReadOnlyState(repo StoreRepository) (*ownership.Store, *QuarantineTable, error) {
	if ro, ok := repo.(ReadOnlyStateReader); ok {
		store, qt, err := ro.ReadOnlyState()
		if err != nil {
			return nil, nil, err
		}
		if store == nil {
			store = ownership.NewStore()
		}
		if qt == nil {
			qt = NewQuarantineTable()
		}
		return store, qt, nil
	}
	return loadStateInLock(repo)
}

func appendUniquePath(paths []model.Path, target model.Path) []model.Path {
	for _, p := range paths {
		if p.Equal(target) {
			return paths
		}
	}
	return append(paths, target.Clone())
}
