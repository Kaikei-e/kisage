package provider_test

import (
	"os"
	"path/filepath"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/provider"
)

func TestSecurityRegression_GC02_GC11_PreApplyDriftDetectionAndSymlinkLockRejection(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(rootDir, "target.lkal")
	initialContent := "@lkal \"0.1\"\napp {\n  a = \"v1\"\n  b = \"keep\"\n}\n"
	if err := os.WriteFile(targetPath, []byte(initialContent), 0o600); err != nil {
		t.Fatal(err)
	}
	phys, err := provider.InspectPhysicalIdentity(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ownership.NewResourceRegistry(ownership.ResourceBinding{
		ID:             "res-1",
		Provider:       ownership.ProviderLKALFile,
		ManagementRoot: rootDir,
		TargetPath:     targetPath,
		StoreID:        "store-1",
		Physical:       phys,
	})
	if err != nil {
		t.Fatal(err)
	}
	prov, err := provider.NewLKALFileProvider(reg)
	if err != nil {
		t.Fatal(err)
	}

	pathA := model.MustParsePath("/app/a")
	if _, err := prov.Observe("res-1", pathA); err != nil {
		t.Fatal(err)
	}

	// Simulate external modification to an unqueried sibling between Observe and ApplyBatch.
	driftedContent := "@lkal \"0.1\"\napp {\n  a = \"v1\"\n  b = \"externally-modified\"\n}\n"
	if err := os.WriteFile(targetPath, []byte(driftedContent), 0o600); err != nil {
		t.Fatal(err)
	}

	receipt := prov.ApplyBatch("res-1", "batch-drift", []plan.EnvironmentOp{{
		Path:     pathA,
		Op:       ownership.PendingOpSet,
		Expected: observation.MustPresentLeaf(lkal.String("v1")),
		Target:   lkal.String("v2"),
	}})
	if receipt.Outcome != provider.ReceiptNotApplied {
		t.Fatalf("expected ReceiptNotApplied on pre-apply external file drift, got %+v", receipt)
	}

	// GC-11: Symlinked lock directory must be rejected.
	realLockDir := t.TempDir()
	symlinkParent := t.TempDir()
	symlinkLockDir := filepath.Join(symlinkParent, "locks-link")
	if err := os.Symlink(realLockDir, symlinkLockDir); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.NewFileLockManager(symlinkLockDir); err == nil {
		t.Fatal("expected NewFileLockManager to reject symlinked lock directory")
	}
}
