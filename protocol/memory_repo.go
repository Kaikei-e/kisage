package protocol

import (
	"fmt"
	"sync"

	"github.com/Kaikei-e/kisage/ownership"
)

// MemoryStoreRepository is an in-memory implementation of StoreRepository with stage-specific
// fault injection hooks for deterministic protocol and counterexample testing (ADR 0004 §2–§3, Counterexamples #35, #44, #45, #50, #58, #59, #60).
type MemoryStoreRepository struct {
	mu                 sync.RWMutex
	store              *ownership.Store
	quarantine         *QuarantineTable
	haltErr            error
	OnSaveStore        func(stage StoreCommitStage, next *ownership.Store) *CommitResult
	OnSaveQuarantine   func(next *QuarantineTable) *CommitResult
	SavedStagesHistory []StoreCommitStage
}

// NewMemoryStoreRepository constructs a MemoryStoreRepository initialized with initialStore and initialQuarantine.
func NewMemoryStoreRepository(initialStore *ownership.Store, initialQuarantine *QuarantineTable) *MemoryStoreRepository {
	if initialStore == nil {
		initialStore = ownership.NewStore()
	}
	if initialQuarantine == nil {
		initialQuarantine = NewQuarantineTable()
	}
	return &MemoryStoreRepository{
		store:      initialStore.Clone(),
		quarantine: initialQuarantine.Clone(),
	}
}

// LoadStore returns a defensive clone of the current shared ownership store σ.
func (r *MemoryStoreRepository) LoadStore() (*ownership.Store, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.haltErr != nil {
		return nil, r.haltErr
	}
	return r.store.Clone(), nil
}

// SaveStore atomically updates the stored ownership store σ unless overridden by OnSaveStore.
func (r *MemoryStoreRepository) SaveStore(stage StoreCommitStage, next *ownership.Store) CommitResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.haltErr != nil {
		return CommitResult{Status: CommitFailed}
	}
	r.SavedStagesHistory = append(r.SavedStagesHistory, stage)
	if r.OnSaveStore != nil {
		if custom := r.OnSaveStore(stage, next); custom != nil {
			if custom.Status == CommitSucceeded {
				r.store = next.Clone()
			}
			return *custom
		}
	}
	r.store = next.Clone()
	return CommitResult{Status: CommitSucceeded}
}

// LoadQuarantine returns a defensive clone of the current QuarantineTable.
func (r *MemoryStoreRepository) LoadQuarantine() (*QuarantineTable, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.haltErr != nil {
		return nil, r.haltErr
	}
	return r.quarantine.Clone(), nil
}

// SaveQuarantine atomically updates the QuarantineTable unless overridden by OnSaveQuarantine.
func (r *MemoryStoreRepository) SaveQuarantine(next *QuarantineTable) CommitResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.haltErr != nil {
		return CommitResult{Status: CommitFailed}
	}
	if r.OnSaveQuarantine != nil {
		if custom := r.OnSaveQuarantine(next); custom != nil {
			if custom.Status == CommitSucceeded {
				r.quarantine = next.Clone()
			}
			return *custom
		}
	}
	r.quarantine = next.Clone()
	return CommitResult{Status: CommitSucceeded}
}

func (r *MemoryStoreRepository) HaltRuntime(cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.haltErr = fmt.Errorf("%w: %v", ErrRuntimeHalted, cause)
}

// ReadOnlyState returns defensive clones of the current store and quarantine table for read-only previews.
func (r *MemoryStoreRepository) ReadOnlyState() (*ownership.Store, *QuarantineTable, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.haltErr != nil {
		return nil, nil, r.haltErr
	}
	return r.store.Clone(), r.quarantine.Clone(), nil
}
