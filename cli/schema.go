package cli

// SchemaVersion is the normative JSON schema version for kisage CLI and MCP outputs (ADR 0005 §2).
const SchemaVersion = "1"

// Canonical mode names in JSON output (ADR 0005 §2).
const (
	ModeNamePlan    = "Plan"
	ModeNameApply   = "Apply"
	ModeNamePrune   = "Prune"
	ModeNameExplain = "Explain"
	ModeNameAdopt   = "Adopt"
	ModeNameRelease = "Release"
)

// DiagnosticView represents an item in `diagnostics[]` sorted by path and code order (ADR 0005 §2).
type DiagnosticView struct {
	Code       string `json:"code"`
	Path       string `json:"path,omitempty"`
	Reason     string `json:"reason"`
	NextAction string `json:"nextAction,omitempty"`
}

// RecoveryItemView represents a single recovery candidate or unresolved Pending item in `recovery`.
type RecoveryItemView struct {
	Path        string           `json:"path"`
	BatchID     string           `json:"batchId,omitempty"`
	Op          string           `json:"op,omitempty"`
	Kind        string           `json:"kind"`
	Authorized  bool             `json:"authorized"`
	Persisted   bool             `json:"persisted"`
	Diagnostics []DiagnosticView `json:"diagnostics"`
	Expected    string           `json:"expected,omitempty"`
	Target      string           `json:"target,omitempty"`
}

// RecoveryView represents the `recovery` phase output separated from `normalExecution` (ADR 0005 §2).
type RecoveryView struct {
	Status                string             `json:"status"`
	ConditionalAssumption bool               `json:"conditionalAssumption,omitempty"`
	RequiresStabilize     bool               `json:"requiresStabilize"`
	StabilizeOutcome      string             `json:"stabilizeOutcome,omitempty"`
	CommitStatus          string             `json:"commitStatus,omitempty"`
	StoreOutcomeUnknown   bool               `json:"storeOutcomeUnknown,omitempty"`
	Persisted             bool               `json:"persisted"`
	PersistedCount        int                `json:"persistedCount"`
	UnresolvedCount       int                `json:"unresolvedCount"`
	Candidates            []RecoveryItemView `json:"candidates"`
	Unresolved            []RecoveryItemView `json:"unresolved"`
	Diagnostics           []DiagnosticView   `json:"diagnostics"`
}

// NormalExecutionView represents the `normalExecution` phase output separated from `recovery` (ADR 0005 §2).
type NormalExecutionView struct {
	Status                      string           `json:"status"`
	AllOrNothingSuppressed      bool             `json:"allOrNothingSuppressed"`
	SuppressedCount             int              `json:"suppressedCount"`
	EnvironmentOpsCount         int              `json:"environmentOpsCount"`
	MetadataCandidatesCount     int              `json:"metadataCandidatesCount"`
	DeferredCount               int              `json:"deferredCount"`
	IntentSaved                 bool             `json:"intentSaved"`
	ReceiptOutcome              string           `json:"receiptOutcome,omitempty"`
	StabilizeOutcome            string           `json:"stabilizeOutcome,omitempty"`
	CommitStatus                string           `json:"commitStatus,omitempty"`
	ContractViolation           bool             `json:"contractViolation"`
	Quarantined                 bool             `json:"quarantined"`
	EnvironmentPossiblyModified bool             `json:"environmentPossiblyModified"`
	StoreOutcomeUnknown         bool             `json:"storeOutcomeUnknown"`
	AppliedEnvCount             int              `json:"appliedEnvCount"`
	CommittedMetadataCount      int              `json:"committedMetadataCount"`
	RemainingPendingCount       int              `json:"remainingPendingCount"`
	Diagnostics                 []DiagnosticView `json:"diagnostics"`
}

// PathResultView represents an item in `pathResults[]` (ADR 0005 §2).
type PathResultView struct {
	Path          string           `json:"path"`
	Membership    string           `json:"membership"`
	StateClass    string           `json:"stateClass"`
	Selection     string           `json:"selection"`
	Decision      string           `json:"decision"`
	Action        string           `json:"action,omitempty"`
	RequiredPerms string           `json:"requiredPermissions,omitempty"`
	GrantedPerms  string           `json:"grantedPermissions"`
	PrimaryCode   string           `json:"primaryCode,omitempty"`
	Diagnostics   []DiagnosticView `json:"diagnostics"`
	Observed      string           `json:"observed,omitempty"`
	DesiredValue  string           `json:"desiredValue,omitempty"`
}

// ExecutionCountsView represents `summary.counts` across recovery and normalExecution (ADR 0005 §2).
type ExecutionCountsView struct {
	RecoveredPersisted          int  `json:"recoveredPersisted"`
	RecoveryUnresolved          int  `json:"recoveryUnresolved"`
	EnvironmentApplied          int  `json:"environmentApplied"`
	MetadataCommitted           int  `json:"metadataCommitted"`
	RemainingPending            int  `json:"remainingPending"`
	EnvironmentPossiblyModified bool `json:"environmentPossiblyModified"`
	Suppressed                  int  `json:"suppressed"`
	Deferred                    int  `json:"deferred"`
	PruneRequired               int  `json:"pruneRequired"`
	Conflicts                   int  `json:"conflicts"`
	ObservationUnavailable      int  `json:"observationUnavailable"`
	PolicyDenied                int  `json:"policyDenied"`
}

// SummaryView represents `summary` for singular commands (`apply`, `prune`, `explain`, `adopt`, `release`) (ADR 0005 §2).
type SummaryView struct {
	Execution   string              `json:"execution"`
	Convergence string              `json:"convergence"`
	Counts      ExecutionCountsView `json:"counts"`
	Diagnostics []DiagnosticView    `json:"diagnostics"`
}

// ModePlanSummaryView represents `plans.apply.summary` and `plans.prune.summary` in `plan` output (ADR 0005 §2).
type ModePlanSummaryView struct {
	Execution   string              `json:"execution"`
	Counts      ExecutionCountsView `json:"counts"`
	Diagnostics []DiagnosticView    `json:"diagnostics"`
}

// ModePlanView represents a single mode branch (`plans.apply` or `plans.prune`) in `plan` output (ADR 0005 §2).
type ModePlanView struct {
	NormalExecution NormalExecutionView `json:"normalExecution"`
	PathResults     []PathResultView    `json:"pathResults"`
	Summary         ModePlanSummaryView `json:"summary"`
}

// DualModePlansView holds the independent `apply` and `prune` plans in `plan` output (ADR 0005 §2).
type DualModePlansView struct {
	Apply ModePlanView `json:"apply"`
	Prune ModePlanView `json:"prune"`
}

// PlanTopSummaryView represents the top-level `summary` of `plan`, which contains ONLY `execution` and `convergence` (ADR 0005 §2).
type PlanTopSummaryView struct {
	Execution   string `json:"execution"`
	Convergence string `json:"convergence"`
}

// PlanDocument is the normative JSON output schema for `kisage plan` / `kisage_plan` (ADR 0005 §2).
type PlanDocument struct {
	SchemaVersion         string             `json:"schemaVersion"`
	Mode                  string             `json:"mode"`
	Resource              string             `json:"resource"`
	DeclarationDigest     string             `json:"declarationDigest"`
	ConditionalOnRecovery bool               `json:"conditionalOnRecovery"`
	Recovery              RecoveryView       `json:"recovery"`
	Plans                 DualModePlansView  `json:"plans"`
	Summary               PlanTopSummaryView `json:"summary"`
}

// ExplanationView holds the path-specific inspection details for `kisage explain` / `kisage_explain` (ADR 0005 §1–2).
type ExplanationView struct {
	Path               string         `json:"path"`
	Declared           bool           `json:"declared"`
	DesiredValue       string         `json:"desiredValue,omitempty"`
	Observed           string         `json:"observed"`
	OwnerManager       string         `json:"ownerManager,omitempty"`
	OwnerOrigin        string         `json:"ownerOrigin,omitempty"`
	HasPending         bool           `json:"hasPending"`
	OwnerRevision      uint64         `json:"ownerRevision"`
	ConditionToken     string         `json:"conditionToken,omitempty"`
	GrantedPermissions string         `json:"grantedPermissions"`
	Apply              PathResultView `json:"apply"`
	Prune              PathResultView `json:"prune"`
	ApplyExecution     string         `json:"applyExecution"`
	PruneExecution     string         `json:"pruneExecution"`
}

// ResolutionView holds the explicit resolution details for `kisage adopt` and `kisage release` (ADR 0003 §3, ADR 0005 §1–2).
type ResolutionView struct {
	Action              string `json:"action"`
	Path                string `json:"path"`
	DeclaredInFile      bool   `json:"declaredInFile"`
	RemanageOnNextApply bool   `json:"remanageOnNextApply"`
	PreviousRevision    uint64 `json:"previousRevision"`
	NextRevision        uint64 `json:"nextRevision"`
	Persisted           bool   `json:"persisted"`
}

// CommandDocument is the normative singular JSON output schema for `apply`, `prune`, `explain`, `adopt`, and `release` (ADR 0005 §2).
type CommandDocument struct {
	SchemaVersion     string              `json:"schemaVersion"`
	Mode              string              `json:"mode"`
	Resource          string              `json:"resource"`
	DeclarationDigest string              `json:"declarationDigest"`
	Recovery          RecoveryView        `json:"recovery"`
	NormalExecution   NormalExecutionView `json:"normalExecution"`
	PathResults       []PathResultView    `json:"pathResults"`
	Explanation       *ExplanationView    `json:"explanation,omitempty"`
	Resolution        *ResolutionView     `json:"resolution,omitempty"`
	Summary           SummaryView         `json:"summary"`
}
