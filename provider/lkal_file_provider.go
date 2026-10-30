package provider

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
)

const atSymlinkNoFollow = 0x100

func fstatAtNoFollow(dirFD int, name string, st *syscall.Stat_t) error {
	nameBytes, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(
		syscall.SYS_NEWFSTATAT,
		uintptr(dirFD),
		uintptr(unsafe.Pointer(nameBytes)),
		uintptr(unsafe.Pointer(st)),
		uintptr(atSymlinkNoFollow),
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func unsafeSlicePtr(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

// FaultHooks allows tests to deterministically inject faults or simulate process crashes
// at each durability boundary of ApplyBatch and Stabilize (ADR 0004 §2–§3, Counterexamples #26, #35, #43).
type FaultHooks struct {
	BeforeTempWrite          func(resID ownership.ResourceID, batchID string) error
	BeforeTempSync           func(resID ownership.ResourceID, batchID string) error
	BeforeRename             func(resID ownership.ResourceID, batchID string) error
	AfterRenameBeforeDirSync func(resID ownership.ResourceID, batchID string) error
	OnStabilizeSync          func(resID ownership.ResourceID) error
	// MutatePatchedBytesForTest allows conformance tests to simulate a buggy Provider that
	// alters a written leaf or sibling value while returning Applied (Counterexamples #18, #45).
	MutatePatchedBytesForTest func(resID ownership.ResourceID, batchID string, patched []byte) []byte
}

// LKALFileProvider implements Provider for local Linux regular LKAL files using os.OpenRoot
// and Root.Rename (ADR 0004 §5, Counterexamples #1, #4, #13, #17, #18, #26, #28, #32, #33, #35, #43, #45).
type LKALFileProvider struct {
	mu           sync.RWMutex
	bindings     map[ownership.ResourceID]ownership.ResourceBinding
	lastObserved map[ownership.ResourceID][32]byte
	Hooks        FaultHooks
}

// NewLKALFileProvider constructs an LKALFileProvider from a validated ResourceRegistry.
func NewLKALFileProvider(reg *ownership.ResourceRegistry) (*LKALFileProvider, error) {
	if reg == nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "resource registry must not be nil",
		}
	}
	bindings := make(map[ownership.ResourceID]ownership.ResourceBinding)
	for _, b := range reg.Bindings() {
		if err := ownership.ValidateResourceBinding(b); err != nil {
			return nil, err
		}
		bindings[b.ID] = b
	}
	return &LKALFileProvider{
		bindings:     bindings,
		lastObserved: make(map[ownership.ResourceID][32]byte),
	}, nil
}

// Observe inspects the target LKAL file for resID (verifying path components, physical identity,
// and regular file invariants) and returns an observation Snapshot for paths (ADR 0004 §3, §5).
func (p *LKALFileProvider) Observe(resID ownership.ResourceID, paths ...model.Path) (*observation.Snapshot, error) {
	_, _, rawBytes, f, err := p.readAndValidateTargetWithBytes(resID)
	if err != nil {
		return nil, err
	}
	snap, err := observation.ObserveFile(f, paths...)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.lastObserved[resID] = sha256.Sum256(rawBytes)
	p.mu.Unlock()
	return snap, nil
}

// ApplyBatch re-reads the target file, re-verifies all Expected and structural conditions,
// writes the patched LKAL content to a same-directory temp file, fsyncs the temp file,
// atomically replaces the target via os.Root.Rename, and fsyncs the parent directory (ADR 0004 §2, §5).
//
// Invariants:
//   - Any precondition failure or pre-rename error returns ReceiptNotApplied with zero target modification.
//   - Any error during or after Root.Rename (such as parent directory fsync failure) returns ReceiptIndeterminate (Counterexample #26).
func (p *LKALFileProvider) ApplyBatch(resID ownership.ResourceID, batchID string, ops []plan.EnvironmentOp) Receipt {
	binding, meta, rawBytes, err := p.readRawAndValidate(resID)
	if err != nil {
		return receiptFromError(batchID, nil, err)
	}
	p.mu.RLock()
	lastDigest, hasLast := p.lastObserved[resID]
	p.mu.RUnlock()
	if hasLast && sha256.Sum256(rawBytes) != lastDigest {
		return Receipt{
			BatchID: batchID,
			Outcome: ReceiptNotApplied,
			Diagnostics: []model.Diagnostic{{
				Code:       model.CodeExternalDrift,
				Reason:     fmt.Sprintf("target file %q drifted between pre-apply observation and ApplyBatch", binding.TargetPath),
				NextAction: "re-run operation to plan against the latest environment state",
			}},
		}
	}
	patched, _, receipt := ApplyBatchToBytes(binding.TargetPath, rawBytes, batchID, ops)
	if receipt.Outcome != ReceiptApplied {
		return receipt
	}
	if p.Hooks.MutatePatchedBytesForTest != nil {
		patched = p.Hooks.MutatePatchedBytesForTest(resID, batchID, patched)
	}
	res := p.replaceAndSyncWithRoot(binding, meta, batchID, patched)
	if res.Outcome == ReceiptApplied || res.Outcome == ReceiptIndeterminate {
		p.mu.Lock()
		delete(p.lastObserved, resID)
		p.mu.Unlock()
	}
	return res
}

// Stabilize fsyncs the target regular file and its parent directory, then re-observes the file
// and verifies that the snapshot matches expected (ADR 0004 §2–§3, Counterexamples #43, #50, #57).
func (p *LKALFileProvider) Stabilize(
	resID ownership.ResourceID,
	expected *observation.Snapshot,
	paths ...model.Path,
) StabilizeResult {
	binding, ok := p.getBinding(resID)
	if !ok {
		return StabilizeResult{
			Outcome: StabilizeIndeterminate,
			Diagnostics: []model.Diagnostic{{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("unregistered resource %q", resID),
			}},
		}
	}
	syncErr := p.syncFileAndParent(binding)
	if syncErr == nil && p.Hooks.OnStabilizeSync != nil {
		syncErr = p.Hooks.OnStabilizeSync(resID)
	}
	if syncErr != nil {
		return EvaluateStabilize(expected, nil, paths, syncErr)
	}
	queryPaths := collectStabilizePaths(expected, observation.NewSnapshot(), paths)
	currentSnap, obsErr := p.Observe(resID, queryPaths...)
	if obsErr != nil {
		return EvaluateStabilize(expected, nil, paths, obsErr)
	}
	return EvaluateStabilize(expected, currentSnap, paths, nil)
}

func (p *LKALFileProvider) readAndValidateTargetWithBytes(resID ownership.ResourceID) (ownership.ResourceBinding, FileMetadata, []byte, *lkal.File, error) {
	binding, meta, rawBytes, err := p.readRawAndValidate(resID)
	if err != nil {
		return ownership.ResourceBinding{}, FileMetadata{}, nil, nil, err
	}
	f, err := lkal.Parse(binding.TargetPath, rawBytes)
	if err != nil {
		return binding, meta, nil, nil, model.Diagnostic{
			Code:   model.CodeObservationUnavailable,
			Reason: fmt.Sprintf("failed to parse LKAL file %q: %v", binding.TargetPath, err),
			Cause:  err,
		}
	}
	return binding, meta, rawBytes, f, nil
}

func (p *LKALFileProvider) readRawAndValidate(resID ownership.ResourceID) (ownership.ResourceBinding, FileMetadata, []byte, error) {
	binding, ok := p.getBinding(resID)
	if !ok {
		return ownership.ResourceBinding{}, FileMetadata{}, nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("unregistered resource %q", resID),
		}
	}
	meta, rawBytes, err := InspectLocalFile(binding.ManagementRoot, binding.TargetPath)
	if err != nil {
		return binding, meta, nil, err
	}
	if err := ValidateTargetFileMetadata(binding, meta); err != nil {
		return binding, meta, nil, err
	}
	return binding, meta, rawBytes, nil
}

func (p *LKALFileProvider) getBinding(resID ownership.ResourceID) (ownership.ResourceBinding, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	b, ok := p.bindings[resID]
	return b, ok
}
