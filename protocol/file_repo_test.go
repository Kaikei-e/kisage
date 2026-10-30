package protocol_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
)

func newDiskRepository(t *testing.T) (string, *protocol.FileStoreRepository) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	repo, err := protocol.InitializeFileStoreRepository(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return dir, repo
}
func diskEntry(t *testing.T) *ownership.Store {
	t.Helper()
	e, err := ownership.NewPendingCreate(actorA, "batch", lkal.String("target"), 1)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ownership.NewStore().Put(testResID, model.MustParsePath("/fresh"), e)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// #28, #35, #43, #60: actual write/fsync/rename boundaries preserve either
// the complete old or complete new store; post-rename errors stay uncertain.
func TestFileRepositoryDurabilityBoundaries(t *testing.T) {
	for _, phase := range []string{"write", "file-sync", "rename", "after-rename", "directory-sync"} {
		t.Run(phase, func(t *testing.T) {
			dir, repo := newDiskRepository(t)
			fail := func(protocol.StoreCommitStage) error { return errors.New("injected persistence failure") }
			switch phase {
			case "write":
				repo.Hooks.BeforeWrite = fail
			case "file-sync":
				repo.Hooks.BeforeSync = fail
			case "rename":
				repo.Hooks.BeforeRename = fail
			case "after-rename":
				repo.Hooks.AfterRename = fail
			case "directory-sync":
				repo.Hooks.BeforeDirectorySync = fail
			}
			result := repo.SaveStore(protocol.StageIntentSave, diskEntry(t))
			postRename := phase == "after-rename" || phase == "directory-sync"
			want := protocol.CommitFailed
			if postRename {
				want = protocol.CommitIndeterminate
			}
			if result.Status != want {
				t.Fatalf("got %s want %s", result.Status, want)
			}
			fresh, err := protocol.OpenFileStoreRepository(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			store, err := fresh.LoadStore()
			if err != nil {
				t.Fatal(err)
			}
			if store.Has(testResID, model.MustParsePath("/fresh")) != postRename {
				t.Fatal("reload did not recover complete old/new version")
			}
		})
	}
}

func TestFileRepositoryQuarantineAndHaltSurviveReopen(t *testing.T) {
	dir, repo := newDiskRepository(t)
	qt, err := protocol.NewQuarantineTable().Quarantine(protocol.QuarantineRecord{Resource: testResID, BatchID: "bad", Reason: "contract violation", Diagnostics: []model.Diagnostic{{Code: model.CodeProviderContractViolation, Path: model.MustParsePath("/fresh"), Reason: "wrong value"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result := repo.SaveQuarantine(qt); result.Status != protocol.CommitSucceeded {
		t.Fatalf("quarantine save: %+v", result)
	}
	if result := repo.SaveStore(protocol.StageIntentSave, diskEntry(t)); result.Status != protocol.CommitSucceeded {
		t.Fatalf("intent save: %+v", result)
	}
	fresh, err := protocol.OpenFileStoreRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	reloaded, err := fresh.LoadQuarantine()
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsQuarantined(testResID) {
		t.Fatal("store save discarded quarantine")
	}
	repo.HaltRuntime(errors.New("quarantine persistence failed"))
	if _, err := fresh.LoadStore(); !errors.Is(err, protocol.ErrRuntimeHalted) {
		t.Fatalf("other repository continued: %v", err)
	}
	if _, err := protocol.OpenFileStoreRepository(dir); !errors.Is(err, protocol.ErrRuntimeHalted) {
		t.Fatalf("halted repository reopened: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime-halted")); err != nil {
		t.Fatal("durable halt marker missing:", err)
	}
}

func TestFileRepositoryRejectsMissingCorruptAndUnsafeState(t *testing.T) {
	if r, err := protocol.OpenFileStoreRepository(t.TempDir()); err == nil {
		r.Close()
		t.Fatal("missing store silently initialized")
	}
	dir, repo := newDiskRepository(t)
	if r, err := protocol.InitializeFileStoreRepository(dir, nil); err == nil {
		r.Close()
		t.Fatal("initialization overwrote existing store")
	}
	if err := os.WriteFile(filepath.Join(dir, "ownership.json"), []byte(`{"version":1,"payload":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadStore(); err == nil {
		t.Fatal("corrupt state loaded")
	}
	if r, err := protocol.OpenFileStoreRepository(dir); err == nil {
		r.Close()
		t.Fatal("corrupt state reopened")
	}
	dir2, repo2 := newDiskRepository(t)
	if err := os.Rename(filepath.Join(dir2, "ownership.json"), filepath.Join(dir2, "original.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("original.json", filepath.Join(dir2, "ownership.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo2.LoadStore(); err == nil {
		t.Fatal("symlink store accepted")
	}
}

// #35, #43: SIGKILL discards provider/repository memory, but an on-disk
// Pending is recovered with Stabilize and conservative Modified provenance.
// This checks process crashes; it does not simulate power-loss persistence.
func TestFileRepositoryProcessCrashRecovery(t *testing.T) {
	if phase := os.Getenv("KISAGE_REVIEW_CRASH_PHASE"); phase != "" {
		root := os.Getenv("KISAGE_REVIEW_ROOT")
		target := filepath.Join(root, "target.lkal")
		phys, err := provider.InspectPhysicalIdentity(target)
		if err != nil {
			t.Fatal(err)
		}
		reg, err := ownership.NewResourceRegistry(ownership.ResourceBinding{ID: testResID, Provider: ownership.ProviderLKALFile, ManagementRoot: root, TargetPath: target, StoreID: testStoreID, Physical: phys})
		if err != nil {
			t.Fatal(err)
		}
		p, err := provider.NewLKALFileProvider(reg)
		if err != nil {
			t.Fatal(err)
		}
		repo, err := protocol.OpenFileStoreRepository(filepath.Join(root, "store"))
		if err != nil {
			t.Fatal(err)
		}
		kill := func() {
			if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
				panic(err)
			}
		}
		switch phase {
		case "intent":
			p.Hooks.BeforeTempWrite = func(ownership.ResourceID, string) error { kill(); return nil }
		case "environment-rename":
			p.Hooks.AfterRenameBeforeDirSync = func(ownership.ResourceID, string) error { kill(); return nil }
		case "ownership-rename":
			repo.Hooks.AfterRename = func(stage protocol.StoreCommitStage) error {
				if stage == protocol.StageFinalBatchCommit {
					kill()
				}
				return nil
			}
		}
		locks, err := provider.NewFileLockManager(filepath.Join(root, "locks"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = protocol.ExecuteReconcile(protocol.ReconcileRequest{StoreID: testStoreID, Resource: testResID, Actor: actorA, Mode: plan.ModeApply, BatchID: "child", Declaration: mustDecl(t, "fresh = 2\n"), Scope: fullScope(t), Provider: p, StoreRepo: repo, Locks: locks})
		t.Fatalf("crash hook did not stop child: %v", err)
	}
	for _, phase := range []string{"intent", "environment-rename", "ownership-rename"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "target.lkal")
			if err := os.WriteFile(target, []byte("@lkal \"0.1\"\nsibling = 7\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			phys, err := provider.InspectPhysicalIdentity(target)
			if err != nil {
				t.Fatal(err)
			}
			reg, err := ownership.NewResourceRegistry(ownership.ResourceBinding{ID: testResID, Provider: ownership.ProviderLKALFile, ManagementRoot: root, TargetPath: target, StoreID: testStoreID, Physical: phys})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "store"), 0o700); err != nil {
				t.Fatal(err)
			}
			repo, err := protocol.InitializeFileStoreRepository(filepath.Join(root, "store"), nil)
			if err != nil {
				t.Fatal(err)
			}
			repo.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(exe, "-test.run=^TestFileRepositoryProcessCrashRecovery$")
			child.Env = append(os.Environ(), "KISAGE_REVIEW_CRASH_PHASE="+phase, "KISAGE_REVIEW_ROOT="+root)
			out, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child did not crash: %v %s", err, out)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("unexpected child exit: %v %s", err, out)
			}
			// Reuse the original registration, with no in-process rename information.
			p, err := provider.NewLKALFileProvider(reg)
			if err != nil {
				t.Fatal(err)
			}
			repo, err = protocol.OpenFileStoreRepository(filepath.Join(root, "store"))
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			locks, err := provider.NewFileLockManager(filepath.Join(root, "locks"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{StoreID: testStoreID, Resource: testResID, Actor: actorA, Mode: plan.ModeApply, BatchID: "parent", Declaration: mustDecl(t, "fresh = 2\n"), Scope: fullScope(t), Provider: p, StoreRepo: repo, Locks: locks})
			if err != nil {
				t.Fatal(err)
			}
			if result.Summary.Convergence != plan.ConvergenceConverged {
				t.Fatalf("did not recover: %+v", result.Summary)
			}
			store, err := repo.LoadStore()
			if err != nil {
				t.Fatal(err)
			}
			entry, ok := store.Get(testResID, model.MustParsePath("/fresh"))
			if !ok || !entry.IsSettled() {
				t.Fatal("pending not resolved")
			}
			want := ownership.OriginCreated
			if phase == "environment-rename" {
				want = ownership.OriginModified
			}
			if entry.Origin != want {
				t.Fatalf("creation attribution: got %s want %s", entry.Origin, want)
			}
		})
	}
}
