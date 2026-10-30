package protocol

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/provider"
)

func executeNormalStagesInLock(
	req ReconcileRequest,
	initialSnap *observation.Snapshot,
	recoveredStore *ownership.Store,
	quarantineTbl *QuarantineTable,
	planRes *plan.Result,
) (NormalPhaseResult, error) {
	if planRes.Gate.Blocked {
		return NormalPhaseResult{
			Status:      NormalBlocked,
			Store:       recoveredStore.Clone(),
			Diagnostics: slices.Clone(planRes.Gate.Diagnostics),
		}, nil
	}
	if !planRes.Gate.HasWork() {
		return NormalPhaseResult{
			Status: NormalNoOp,
			Store:  recoveredStore.Clone(),
		}, nil
	}

	batch, err := NewExecutionBatch(req.BatchID, planRes.Gate)
	if err != nil {
		return NormalPhaseResult{}, err
	}
	intent, hasIntent, err := ExtractDurableIntent(batch)
	if err != nil {
		return NormalPhaseResult{}, err
	}

	// Metadata-only path (EnvironmentOps is empty, MetadataCandidates is non-empty, Counterexample #57).
	if !hasIntent {
		return runMetadataOnlyPath(req, initialSnap, recoveredStore, batch)
	}
	return runEnvironmentBatchPath(req, initialSnap, recoveredStore, quarantineTbl, batch, intent)
}

func runMetadataOnlyPath(
	req ReconcileRequest,
	initialSnap *observation.Snapshot,
	recoveredStore *ownership.Store,
	batch ExecutionBatch,
) (NormalPhaseResult, error) {
	metaPaths := extractMetadataPaths(batch.MetadataCandidates)
	reObsSnap, obsErr := req.Provider.Observe(req.Resource, metaPaths...)
	if obsErr != nil {
		stab := provider.EvaluateStabilize(initialSnap, nil, metaPaths, obsErr)
		return EvaluateMetadataOnlyBatchStage(MetadataStageInput{
			Resource:     req.Resource,
			Actor:        req.Actor,
			Batch:        batch,
			InitialStore: recoveredStore,
			Stabilize:    stab,
		})
	}
	preCheckDiags := VerifyMetadataCandidatesInLock(reObsSnap, recoveredStore, req.Resource, req.Actor, req.Scope, batch.MetadataCandidates)
	var stab provider.StabilizeResult
	var commit CommitResult
	if len(preCheckDiags) == 0 {
		stab = req.Provider.Stabilize(req.Resource, reObsSnap, metaPaths...)
		if stab.Outcome == provider.StabilizeDurable {
			committedStore, err := CommitExecutionBatchToStore(recoveredStore, req.Resource, req.Actor, batch)
			if err != nil {
				return NormalPhaseResult{}, err
			}
			commit = req.StoreRepo.SaveStore(StageFinalBatchCommit, committedStore)
		}
	}
	return EvaluateMetadataOnlyBatchStage(MetadataStageInput{
		Resource:      req.Resource,
		Actor:         req.Actor,
		Batch:         batch,
		InitialStore:  recoveredStore,
		PreCheckDiags: preCheckDiags,
		Stabilize:     stab,
		FinalCommit:   commit,
	})
}

func runEnvironmentBatchPath(
	req ReconcileRequest,
	initialSnap *observation.Snapshot,
	recoveredStore *ownership.Store,
	quarantineTbl *QuarantineTable,
	batch ExecutionBatch,
	intent *DurableIntent,
) (NormalPhaseResult, error) {
	// Stage 3: Save DurableIntent only (ADR 0004 §2 Step 3, Counterexample #58).
	intentStore, err := ApplyDurableIntentToStore(recoveredStore, req.Resource, req.Actor, intent)
	if err != nil {
		return NormalPhaseResult{}, err
	}
	intentCommit := req.StoreRepo.SaveStore(StageIntentSave, intentStore)
	in := EnvironmentStageInput{
		Resource:       req.Resource,
		Actor:          req.Actor,
		Batch:          batch,
		Intent:         intent,
		PreIntentStore: recoveredStore,
		IntentStore:    intentStore,
		IntentCommit:   intentCommit,
		BeforeSnapshot: initialSnap,
	}
	if intentCommit.Status != CommitSucceeded {
		return EvaluateEnvironmentBatchStage(in)
	}

	// Stage 4: Re-verify Scope, ownership, and expectations in-lock, then call ApplyBatch.
	allBatchPaths := collectBatchPaths(batch)
	latestBeforeApply, obsErr := req.Provider.Observe(req.Resource, allBatchPaths...)
	if obsErr != nil || latestBeforeApply == nil {
		in.PreApplyCheckDiags = []model.Diagnostic{{Code: model.CodeObservationUnavailable, Reason: "pre-apply observation failed", Cause: obsErr}}
	} else {
		in.BeforeSnapshot = latestBeforeApply
		in.PreApplyCheckDiags = VerifyEnvironmentOpsInLock(in.BeforeSnapshot, intentStore, req.Resource, req.Actor, req.Scope, batch.BatchID, batch.EnvironmentOps)
		in.PreApplyCheckDiags = append(in.PreApplyCheckDiags, VerifyMetadataCandidatesInLock(in.BeforeSnapshot, intentStore, req.Resource, req.Actor, req.Scope, batch.MetadataCandidates)...)
	}
	if len(in.PreApplyCheckDiags) > 0 {
		rolledBack, rbErr := RollbackDurableIntentInStore(intentStore, req.Resource, req.Actor, intent)
		if rbErr != nil {
			return NormalPhaseResult{}, rbErr
		}
		in.RollbackCommit = req.StoreRepo.SaveStore(StageRollbackNotApplied, rolledBack)
		return EvaluateEnvironmentBatchStage(in)
	}

	in.Receipt = req.Provider.ApplyBatch(req.Resource, batch.BatchID, batch.EnvironmentOps)
	if in.Receipt.Outcome == provider.ReceiptNotApplied {
		rolledBack, rbErr := RollbackDurableIntentInStore(intentStore, req.Resource, req.Actor, intent)
		if rbErr != nil {
			return NormalPhaseResult{}, rbErr
		}
		in.RollbackCommit = req.StoreRepo.SaveStore(StageRollbackNotApplied, rolledBack)
		return EvaluateEnvironmentBatchStage(in)
	}
	if in.Receipt.Outcome != provider.ReceiptApplied {
		return EvaluateEnvironmentBatchStage(in)
	}

	// Stage 5: In-lock re-observation, contract verification, Stabilize, and atomic Commit.
	in.PostApplySnapshot, in.PostApplyObsErr = req.Provider.Observe(req.Resource, allBatchPaths...)
	if in.PostApplyObsErr != nil || in.PostApplySnapshot == nil {
		return EvaluateEnvironmentBatchStage(in)
	}
	if contractDiags := provider.VerifyAppliedBatchContract(in.BeforeSnapshot, in.PostApplySnapshot, batch.EnvironmentOps); len(contractDiags) > 0 {
		qRec, _ := BuildContractViolationQuarantine(req.Resource, batch.BatchID, contractDiags)
		nextQT, _ := quarantineTbl.Quarantine(qRec)
		in.QuarantineSaveCommit = req.StoreRepo.SaveQuarantine(nextQT)
		return EvaluateEnvironmentBatchStage(in)
	}

	in.PostMetadataDiags = VerifyMetadataCandidatesInLock(in.PostApplySnapshot, intentStore, req.Resource, req.Actor, req.Scope, batch.MetadataCandidates)
	if len(in.PostMetadataDiags) > 0 {
		return EvaluateEnvironmentBatchStage(in)
	}

	in.Stabilize = req.Provider.Stabilize(req.Resource, in.PostApplySnapshot, allBatchPaths...)
	if in.Stabilize.Outcome == provider.StabilizeDurable {
		committedStore, commitErr := CommitExecutionBatchToStore(intentStore, req.Resource, req.Actor, batch)
		if commitErr != nil {
			return NormalPhaseResult{}, commitErr
		}
		in.FinalCommit = req.StoreRepo.SaveStore(StageFinalBatchCommit, committedStore)
	}
	return EvaluateEnvironmentBatchStage(in)
}

func selectFinalSnapshot(req ReconcileRequest, normalRes NormalPhaseResult) (*observation.Snapshot, error) {
	queryPaths := collectQueryPaths(req.Declaration, normalRes.Store, req.Resource, req.Actor)
	snap, err := req.Provider.Observe(req.Resource, queryPaths...)
	if err == nil && snap != nil {
		return snap, nil
	}
	if err == nil {
		err = model.Diagnostic{Code: model.CodeObservationUnavailable, Reason: "final observation returned nil snapshot"}
	}
	return observation.NewSnapshot(), err
}

func validateReconcileRequest(req ReconcileRequest) error {
	if err := ownership.ValidateStoreID(req.StoreID); err != nil {
		return err
	}
	if err := ownership.ValidateResourceID(req.Resource); err != nil {
		return err
	}
	if err := ownership.ValidateActorID(req.Actor); err != nil {
		return err
	}
	if !req.Mode.IsValid() {
		return model.Diagnostic{Code: model.CodeInvalidInput, Reason: "invalid reconcile Mode"}
	}
	if req.Declaration == nil || req.Provider == nil || req.StoreRepo == nil || req.Locks == nil {
		return model.Diagnostic{Code: model.CodeInvalidInput, Reason: "Declaration, Provider, StoreRepo, and Locks must not be nil"}
	}
	return req.Scope.Validate()
}

func loadStateInLock(repo StoreRepository) (*ownership.Store, *QuarantineTable, error) {
	store, err := repo.LoadStore()
	if err != nil {
		return nil, nil, err
	}
	if store == nil {
		store = ownership.NewStore()
	}
	qt, err := repo.LoadQuarantine()
	if err != nil {
		return nil, nil, err
	}
	if qt == nil {
		qt = NewQuarantineTable()
	}
	return store, qt, nil
}

func collectQueryPaths(decl *projection.Declaration, store *ownership.Store, resID ownership.ResourceID, actor ownership.ActorID) []model.Path {
	seen := make(map[string]model.Path)
	if decl != nil {
		for _, p := range decl.Paths() {
			seen[p.String()] = p
		}
	}
	if store != nil {
		if sigmaA, err := ownership.ProjectActor(store, resID, actor); err == nil {
			for _, p := range sigmaA.Domain() {
				seen[p.String()] = p
			}
		}
	}
	out := make([]model.Path, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

func collectBatchPaths(batch ExecutionBatch) []model.Path {
	out := make([]model.Path, 0, len(batch.EnvironmentOps)+len(batch.MetadataCandidates))
	for _, op := range batch.EnvironmentOps {
		out = append(out, op.Path.Clone())
	}
	for _, mc := range batch.MetadataCandidates {
		out = append(out, mc.Path.Clone())
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

func extractMetadataPaths(candidates []plan.MetadataCandidate) []model.Path {
	out := make([]model.Path, len(candidates))
	for i, mc := range candidates {
		out[i] = mc.Path.Clone()
	}
	return out
}
