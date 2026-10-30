package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// DeclarationBinding binds a canonical declaration FILE path to a protected ResourceID
// and Scope policy in the host environment (ADR 0002 §4, ADR 0005 §1).
// Declarations themselves never grant permissions or select arbitrary target files.
type DeclarationBinding struct {
	DeclarationFile string
	Resource        ownership.ResourceID
	Scope           scope.Scope
}

// Validate checks that b has a clean absolute DeclarationFile, a valid ResourceID, and a valid Scope.
func (b DeclarationBinding) Validate() error {
	if b.DeclarationFile == "" || !filepath.IsAbs(b.DeclarationFile) || filepath.Clean(b.DeclarationFile) != b.DeclarationFile {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("declaration binding path %q must be a clean absolute path", b.DeclarationFile),
		}
	}
	if err := ownership.ValidateResourceID(b.Resource); err != nil {
		return err
	}
	return b.Scope.Validate()
}

// ResolvedContext holds the verified host-bound execution context for a single CLI or MCP call.
type ResolvedContext struct {
	Actor           ownership.ActorID
	DeclarationFile string
	ResourceBinding ownership.ResourceBinding
	Scope           scope.Scope
}

// HostEnvironment holds the Root-of-Trust host bindings for CLI and MCP execution (ADR 0002 §4, ADR 0005 §1, Counterexample #16).
type HostEnvironment struct {
	hostActor        ownership.ActorID
	registry         *ownership.ResourceRegistry
	declBindings     map[string]DeclarationBinding
	provider         provider.Provider
	storeRepo        protocol.StoreRepository
	locks            provider.LockManager
	batchIDGenerator func() string
	snapshotLoader   func(filePath string) (*DeclarationSnapshot, error)
}

// HostEnvironmentConfig configures a HostEnvironment.
type HostEnvironmentConfig struct {
	HostActor           ownership.ActorID
	Registry            *ownership.ResourceRegistry
	DeclarationBindings []DeclarationBinding
	Provider            provider.Provider
	StoreRepo           protocol.StoreRepository
	Locks               provider.LockManager
	BatchIDGenerator    func() string
	SnapshotLoader      func(filePath string) (*DeclarationSnapshot, error)
}

// NewHostEnvironment validates and constructs an immutable HostEnvironment (ADR 0002 §4, ADR 0004 §5, ADR 0005 §1).
func NewHostEnvironment(cfg HostEnvironmentConfig) (*HostEnvironment, error) {
	if err := ownership.ValidateActorID(cfg.HostActor); err != nil {
		return nil, err
	}
	if cfg.Registry == nil || cfg.Provider == nil || cfg.StoreRepo == nil || cfg.Locks == nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "HostEnvironment requires non-nil Registry, Provider, StoreRepo, and Locks",
		}
	}
	bindingsMap := make(map[string]DeclarationBinding, len(cfg.DeclarationBindings))
	seenResources := make(map[ownership.ResourceID]string, len(cfg.DeclarationBindings))
	allResources := cfg.Registry.Bindings()
	for _, db := range cfg.DeclarationBindings {
		if err := db.Validate(); err != nil {
			return nil, err
		}
		resBinding, ok := cfg.Registry.Get(db.Resource)
		if !ok {
			return nil, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("declaration %q references unregistered resource %q", db.DeclarationFile, db.Resource),
			}
		}
		if db.DeclarationFile == resBinding.TargetPath {
			return nil, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("declaration file %q must be distinct from target file %q", db.DeclarationFile, resBinding.TargetPath),
			}
		}
		for _, rb := range allResources {
			if db.DeclarationFile == rb.TargetPath {
				return nil, model.Diagnostic{
					Code:   model.CodeInvalidInput,
					Reason: fmt.Sprintf("declaration file %q must be distinct from target file %q", db.DeclarationFile, rb.TargetPath),
				}
			}
		}
		if prevFile, dup := seenResources[db.Resource]; dup {
			return nil, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("duplicate declaration binding for resource %q (%q and %q)", db.Resource, prevFile, db.DeclarationFile),
			}
		}
		if _, dup := bindingsMap[db.DeclarationFile]; dup {
			return nil, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("duplicate registration for declaration file %q", db.DeclarationFile),
			}
		}
		seenResources[db.Resource] = db.DeclarationFile
		bindingsMap[db.DeclarationFile] = db
	}
	gen := cfg.BatchIDGenerator
	if gen == nil {
		gen = defaultBatchID
	}
	loader := cfg.SnapshotLoader
	if loader == nil {
		loader = ReadDeclarationSnapshot
	}
	return &HostEnvironment{
		hostActor:        cfg.HostActor,
		registry:         cfg.Registry,
		declBindings:     bindingsMap,
		provider:         cfg.Provider,
		storeRepo:        cfg.StoreRepo,
		locks:            cfg.Locks,
		batchIDGenerator: gen,
		snapshotLoader:   loader,
	}, nil
}

// HostActor returns the host-bound ActorID.
func (h *HostEnvironment) HostActor() ownership.ActorID {
	if h == nil {
		return ""
	}
	return h.hostActor
}

// ResolveExecutionContext binds the call to the host-configured ActorID (ignoring any untrusted
// CLI `--actor` or MCP `"actor"` argument per Counterexample #16) and looks up the protected
// Resource and Scope registration for declFile (ADR 0005 §1).
func (h *HostEnvironment) ResolveExecutionContext(declFile string, untrustedActorClaim string) (ResolvedContext, error) {
	if h == nil {
		return ResolvedContext{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "host environment must not be nil",
		}
	}
	// Counterexample #16: Never trust self-declared actor arguments; always use the host binding.
	actor, err := ownership.ResolveActor(h.hostActor, untrustedActorClaim)
	if err != nil {
		return ResolvedContext{}, err
	}
	clean, err := validateDeclarationFilePath(declFile)
	if err != nil {
		return ResolvedContext{}, err
	}
	db, ok := h.declBindings[clean]
	if !ok {
		return ResolvedContext{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("declaration file %q is not registered in the host environment", clean),
		}
	}
	resBinding, ok := h.registry.Get(db.Resource)
	if !ok {
		return ResolvedContext{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q bound to %q is not registered", db.Resource, clean),
		}
	}
	return ResolvedContext{
		Actor:           actor,
		DeclarationFile: clean,
		ResourceBinding: resBinding,
		Scope:           db.Scope,
	}, nil
}

func (h *HostEnvironment) loadSnapshotAndVerifyDistinct(ctx ResolvedContext) (*DeclarationSnapshot, error) {
	snap, err := h.snapshotLoader(ctx.DeclarationFile)
	if snap != nil && snap.HasPhysical {
		if distErr := provider.ValidateDeclarationTargetDistinct(
			ctx.DeclarationFile,
			snap.Physical,
			ctx.ResourceBinding.TargetPath,
			ctx.ResourceBinding.Physical,
		); distErr != nil {
			return snap, distErr
		}
		if h.registry != nil {
			for _, rb := range h.registry.Bindings() {
				if distErr := provider.ValidateDeclarationTargetDistinct(
					ctx.DeclarationFile,
					snap.Physical,
					rb.TargetPath,
					rb.Physical,
				); distErr != nil {
					return snap, distErr
				}
			}
		}
	}
	return snap, err
}

func defaultBatchID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "batch-default"
	}
	return "batch-" + hex.EncodeToString(b[:])
}
