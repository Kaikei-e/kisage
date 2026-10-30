package provider_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/provider"
)

// TestCounterexamples17_26_33_35_42_43_LKALFileProviderAndSecurityBoundaries verifies ADR 0004 §5
// and Counterexamples #17, #26, #33, #35, #42, #43.
func TestCounterexamples17_26_33_35_42_43_LKALFileProviderAndSecurityBoundaries(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	if err := os.Chmod(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(rootDir, "config.lkal")
	declPath := filepath.Join(rootDir, "decl.lkal")

	initialContent := []byte("@lkal \"0.1\"\n### keep comment\nserver {\n  host = \"localhost\"\n  port = 8080\n}\n")
	if err := os.WriteFile(targetPath, initialContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(declPath, []byte("@lkal \"0.1\"\nserver {\n  port = 9090\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	targetPhys, err := provider.InspectPhysicalIdentity(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	declPhys, err := provider.InspectPhysicalIdentity(declPath)
	if err != nil {
		t.Fatal(err)
	}

	// ADR 0004 §5: Declaration file and target file must be distinct.
	if err := provider.ValidateDeclarationTargetDistinct(declPath, declPhys, targetPath, targetPhys); err != nil {
		t.Fatalf("expected distinct files to pass: %v", err)
	}
	if err := provider.ValidateDeclarationTargetDistinct(targetPath, targetPhys, targetPath, targetPhys); err == nil {
		t.Fatalf("expected identical declaration and target file to be rejected")
	}

	binding := ownership.ResourceBinding{
		ID:             "res-config",
		Provider:       ownership.ProviderLKALFile,
		ManagementRoot: rootDir,
		TargetPath:     targetPath,
		StoreID:        "store-main",
		Physical:       targetPhys,
	}
	reg, err := ownership.NewResourceRegistry(binding)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := provider.NewLKALFileProvider(reg)
	if err != nil {
		t.Fatal(err)
	}

	pPort := model.MustParsePath("/server/port")
	pHost := model.MustParsePath("/server/host")
	snap, err := prov.Observe("res-config", pPort, pHost)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := snap.Get(pPort).LeafValue(); !ok || !model.EqualLeafValues(v, lkal.Int(8080)) {
		t.Fatalf("expected initial /server/port = 8080, got %v", snap.Get(pPort))
	}

	// Counterexample #35: Inject fault before Root.Rename -> ReceiptNotApplied and target file untouched.
	prov.Hooks.BeforeRename = func(resID ownership.ResourceID, batchID string) error {
		return errors.New("simulated pre-rename failure")
	}
	ops := []plan.EnvironmentOp{{
		Path:     pPort,
		Op:       ownership.PendingOpSet,
		Expected: observation.MustPresentLeaf(lkal.Int(8080)),
		Target:   lkal.Int(9090),
	}}
	preRenameReceipt := prov.ApplyBatch("res-config", "b-pre-fail", ops)
	prov.Hooks.BeforeRename = nil
	if preRenameReceipt.Outcome != provider.ReceiptNotApplied {
		t.Fatalf("Counterexample #35 failed: pre-rename error must return ReceiptNotApplied, got %+v", preRenameReceipt)
	}

	// Counterexample #26 & #43: Inject fault after Root.Rename and before parent directory fsync -> ReceiptIndeterminate!
	prov.Hooks.AfterRenameBeforeDirSync = func(resID ownership.ResourceID, batchID string) error {
		return errors.New("simulated parent dir fsync failure after rename")
	}
	indetReceipt := prov.ApplyBatch("res-config", "b-dirsync-fail", ops)
	prov.Hooks.AfterRenameBeforeDirSync = nil
	if indetReceipt.Outcome != provider.ReceiptIndeterminate || len(indetReceipt.Diagnostics) == 0 || indetReceipt.Diagnostics[0].Code != model.CodeIndeterminateOutcome {
		t.Fatalf("Counterexample #26 failed: post-rename dir fsync failure must return ReceiptIndeterminate (K502), got %+v", indetReceipt)
	}

	// Counterexample #43: Target file now has 9090 on disk after the interrupted rename; Observe and Stabilize succeed during recovery.
	snapAfterRename, err := prov.Observe("res-config", pPort, pHost)
	if err != nil {
		t.Fatalf("expected Observe after interrupted rename to succeed: %v", err)
	}
	stabRes := prov.Stabilize("res-config", snapAfterRename, pPort, pHost)
	if stabRes.Outcome != provider.StabilizeDurable {
		t.Fatalf("Counterexample #43 failed: expected StabilizeDurable on recovery, got %+v", stabRes)
	}

	// Verify file mode (0640) was preserved across Root.Rename replacement.
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected file mode 0640 to be preserved, got %o", info.Mode().Perm())
	}

	// Counterexample #42: Lock ordering validation and separate stable lock file survival across Rename.
	if err := provider.ValidateLockOrder([]provider.LockKind{provider.LockKindResource, provider.LockKindStore}, "", ""); err == nil {
		t.Fatalf("expected reversed lock order [Resource, SharedStore] to be rejected")
	}
	lockMgr, err := provider.NewFileLockManager(lockDir)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := lockMgr.Acquire("store-main", "res-config")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// TestCounterexample17_33_SymlinkFIFOAndHardlinkRejection verifies ADR 0004 §5
// and Counterexamples #17 and #33 (rejecting symlinks, FIFOs, and hard links).
func TestCounterexample17_33_SymlinkFIFOAndHardlinkRejection(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(rootDir, "real.lkal")
	if err := os.WriteFile(realFile, []byte("@lkal \"0.1\"\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Symlink leaf rejection (#17).
	symlinkLeaf := filepath.Join(rootDir, "link.lkal")
	if err := os.Symlink(realFile, symlinkLeaf); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.InspectLocalFile(rootDir, symlinkLeaf); err == nil {
		t.Fatalf("Counterexample #17 failed: expected symlink leaf to be rejected")
	}

	// 2. Symlink directory component rejection (#17).
	subDir := filepath.Join(rootDir, "sub")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(subDir, "cfg.lkal")
	if err := os.WriteFile(subFile, []byte("@lkal \"0.1\"\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(rootDir, "sublink")
	if err := os.Symlink(subDir, symlinkDir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.InspectLocalFile(rootDir, filepath.Join(symlinkDir, "cfg.lkal")); err == nil {
		t.Fatalf("Counterexample #17 failed: expected symlink directory component to be rejected")
	}

	// 3. FIFO special file rejection (#17).
	fifoPath := filepath.Join(rootDir, "pipe.lkal")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.InspectLocalFile(rootDir, fifoPath); err == nil {
		t.Fatalf("Counterexample #17 failed: expected FIFO special file to be rejected")
	}

	// 4. Hard link rejection (#33).
	physBefore, err := provider.InspectPhysicalIdentity(realFile)
	if err != nil {
		t.Fatal(err)
	}
	binding := ownership.ResourceBinding{
		ID:             "res-real",
		Provider:       ownership.ProviderLKALFile,
		ManagementRoot: rootDir,
		TargetPath:     realFile,
		StoreID:        "store-1",
		Physical:       physBefore,
	}
	hardLink := filepath.Join(rootDir, "hardlink.lkal")
	if err := os.Link(realFile, hardLink); err != nil {
		t.Fatal(err)
	}
	metaWithHardlink, _, err := provider.InspectLocalFile(rootDir, realFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateTargetFileMetadata(binding, metaWithHardlink); err == nil {
		t.Fatalf("Counterexample #33 failed: expected file with hard link (LinkCount=2) to be rejected")
	}
}
