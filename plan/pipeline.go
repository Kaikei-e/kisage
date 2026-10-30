package plan

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

// ExecutionStatus represents the planning-phase execution summary (ADR 0005 §2).
type ExecutionStatus uint8

const (
	// ExecNoOp indicates neither recovery nor the active mode has any pending work, deferred work, or blockers.
	ExecNoOp ExecutionStatus = iota
	// ExecPlanned indicates there are executable candidates, recovery candidates, or deferred actions without blockers.
	ExecPlanned
	// ExecBlocked indicates at least one Blocker, PolicyDenied, unresolved recovery, or resource quarantine halted normal execution.
	ExecBlocked
)

// String returns the canonical name of ExecutionStatus.
func (s ExecutionStatus) String() string {
	switch s {
	case ExecNoOp:
		return "NoOp"
	case ExecPlanned:
		return "Planned"
	case ExecBlocked:
		return "Blocked"
	default:
		return "InvalidExecutionStatus"
	}
}

// Result holds the complete output of the 5-stage pure planning function Plan(d, observed, σ, Scope, Actor, Mode) (ADR 0003 §1).
type Result struct {
	Mode                  Mode
	Resource              ownership.ResourceID
	Actor                 ownership.ActorID
	Quarantined           bool
	ConditionalOnRecovery bool
	Recovery              *RecoveryReport
	Classified            []ClassifiedPath
	Selected              []SelectedPath
	Authorized            []AuthorizedPath
	Gate                  GateOutcome
	Execution             ExecutionStatus
	Convergence           ConvergenceStatus
	Diagnostics           []model.Diagnostic
}

// ApplyToStore computes a new *ownership.Store reflecting the virtual recovery updates and,
// if Gate is not Blocked, all planned OwnershipUpdates for this Mode.
// The caller's original Store is never mutated. If Gate is Blocked due to unresolved recovery
// or quarantine, ApplyToStore returns a diagnostic error.
func (r *Result) ApplyToStore() (*ownership.Store, error) {
	if r == nil {
		return nil, model.Diagnostic{Code: model.CodeInvalidInput, Reason: "plan result must not be nil"}
	}
	if r.Quarantined {
		return nil, quarantineDiagnostic(r.Resource)
	}
	if r.Recovery != nil && r.Recovery.HasUnresolved() {
		return nil, r.Recovery.Diagnostics[0]
	}
	if r.Gate.Blocked {
		if len(r.Gate.Diagnostics) > 0 {
			return nil, r.Gate.Diagnostics[0]
		}
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "cannot apply blocked plan to store",
		}
	}
	next := r.Recovery.VirtualStore.Clone()
	var err error
	for _, u := range r.Gate.OwnershipUpdates {
		if u.After == nil {
			next, err = next.Delete(r.Resource, u.Path)
		} else {
			next, err = next.Put(r.Resource, u.Path, *u.After)
		}
		if err != nil {
			return nil, err
		}
	}
	return next, nil
}

// PreviewResult holds the read-only `plan` preview across both Apply and Prune modes (ADR 0003 §1, ADR 0005 §1–2, Counterexample #41).
//
// Invariants enforced by PlanPreview:
//   - PureRecover runs once and reflects only Own-authorized recovery candidates in Recovery.VirtualStore.
//   - Apply and Prune are computed independently from the same Recovery.VirtualStore (never simulating Apply -> Prune sequentially).
//   - No environment writes, file replacements, fsync, or store commits are performed.
type PreviewResult struct {
	Resource    ownership.ResourceID
	Actor       ownership.ActorID
	Quarantined bool
	Recovery    *RecoveryReport
	Apply       *Result
	Prune       *Result
	Execution   ExecutionStatus
	Convergence ConvergenceStatus
}

// DetailedExitCode returns the normative `plan -detailed-exitcode` status (ADR 0005 §2):
//   - 1 if either Apply or Prune is Blocked (Blocker, PolicyDenied, unresolved recovery, or quarantine)
//   - 2 if not Blocked and either mode has candidates, recovery candidates, or Deferred actions
//   - 0 if both modes are NoOp
func (p *PreviewResult) DetailedExitCode() int {
	if p == nil || p.Execution == ExecBlocked {
		return 1
	}
	if p.Execution == ExecPlanned {
		return 2
	}
	return 0
}

// Plan executes the 5-stage pure planning pipeline:
//
//	PureRecover → Classify → SelectMode → Authorize → Gate
//
// for a single Mode (ModeApply or ModePrune) (ADR 0003 §1).
func Plan(in Input, mode Mode) (*Result, error) {
	if err := ValidateInput(in); err != nil {
		return nil, err
	}
	if err := validateMode(mode); err != nil {
		return nil, err
	}
	recovery, err := recoverForPlanning(in)
	if err != nil {
		return nil, err
	}
	return planWithRecovery(in, mode, recovery)
}

// PlanPreview computes the read-only preview for both ModeApply and ModePrune independently
// from the same post-PureRecover virtual store (ADR 0003 §1, Counterexample #41).
func PlanPreview(in Input) (*PreviewResult, error) {
	if err := ValidateInput(in); err != nil {
		return nil, err
	}
	recovery, err := recoverForPlanning(in)
	if err != nil {
		return nil, err
	}
	applyRes, err := planWithRecovery(in, ModeApply, recovery)
	if err != nil {
		return nil, err
	}
	pruneRes, err := planWithRecovery(in, ModePrune, recovery)
	if err != nil {
		return nil, err
	}

	exec := combineExecutionStatus(applyRes.Execution, pruneRes.Execution)
	return &PreviewResult{
		Resource:    in.Resource,
		Actor:       in.Actor,
		Quarantined: in.Quarantined,
		Recovery:    recovery,
		Apply:       applyRes,
		Prune:       pruneRes,
		Execution:   exec,
		Convergence: applyRes.Convergence,
	}, nil
}

// Quarantine forbids normal recovery, including virtual commits in previews.
func recoverForPlanning(in Input) (*RecoveryReport, error) {
	if in.Quarantined {
		return &RecoveryReport{VirtualStore: in.Store.Clone()}, nil
	}
	return PureRecover(in.Store, in.Resource, in.Actor, in.Snapshot, in.Scope)
}

func planWithRecovery(in Input, mode Mode, recovery *RecoveryReport) (*Result, error) {
	classified, err := Classify(in.Declaration, in.Snapshot, recovery.VirtualStore, in.Resource, in.Actor)
	if err != nil {
		return nil, err
	}
	selected, err := SelectMode(classified, mode)
	if err != nil {
		return nil, err
	}
	authorized, err := Authorize(selected, in.Scope)
	if err != nil {
		return nil, err
	}
	gate := Gate(authorized, in.Actor, recovery.HasUnresolved(), in.Quarantined)

	var diags []model.Diagnostic
	if in.Quarantined {
		diags = append(diags, quarantineDiagnostic(in.Resource))
	}
	diags = append(diags, recovery.Diagnostics...)
	diags = append(diags, gate.Diagnostics...)
	model.SortDiagnostics(diags)
	diags = deduplicateDiagnostics(diags)

	exec := evaluatePlanExecutionStatus(recovery, gate)
	conv := EvaluateConvergence(in.Declaration, in.Snapshot, recovery.VirtualStore, in.Resource, in.Actor, in.Quarantined)

	return &Result{
		Mode:                  mode,
		Resource:              in.Resource,
		Actor:                 in.Actor,
		Quarantined:           in.Quarantined,
		ConditionalOnRecovery: recovery.HasCandidates(),
		Recovery:              recovery,
		Classified:            classified,
		Selected:              selected,
		Authorized:            authorized,
		Gate:                  gate,
		Execution:             exec,
		Convergence:           conv,
		Diagnostics:           diags,
	}, nil
}

func evaluatePlanExecutionStatus(recovery *RecoveryReport, gate GateOutcome) ExecutionStatus {
	if gate.Blocked {
		return ExecBlocked
	}
	if recovery.HasCandidates() || gate.HasWork() || len(gate.DeferredActions) > 0 {
		return ExecPlanned
	}
	return ExecNoOp
}

func combineExecutionStatus(a, b ExecutionStatus) ExecutionStatus {
	if a == ExecBlocked || b == ExecBlocked {
		return ExecBlocked
	}
	if a == ExecPlanned || b == ExecPlanned {
		return ExecPlanned
	}
	return ExecNoOp
}

func deduplicateDiagnostics(sorted []model.Diagnostic) []model.Diagnostic {
	if len(sorted) <= 1 {
		return sorted
	}
	return slices.CompactFunc(sorted, func(a, b model.Diagnostic) bool {
		return model.CompareDiagnostics(a, b) == 0 && a.NextAction == b.NextAction
	})
}
