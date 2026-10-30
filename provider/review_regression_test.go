package provider_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/provider"
)

func reviewFile(t *testing.T) (string, *ownership.ResourceRegistry, *provider.LKALFileProvider) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "app.lkal")
	if err := os.WriteFile(target, []byte("@lkal \"0.1\"\nx = 1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	phys, err := provider.InspectPhysicalIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ownership.NewResourceRegistry(ownership.ResourceBinding{ID: "res", StoreID: "store", Provider: ownership.ProviderLKALFile, ManagementRoot: dir, TargetPath: target, Physical: phys})
	if err != nil {
		t.Fatal(err)
	}
	p, err := provider.NewLKALFileProvider(reg)
	if err != nil {
		t.Fatal(err)
	}
	return target, reg, p
}
func reviewOps() []plan.EnvironmentOp {
	return []plan.EnvironmentOp{{Path: model.MustParsePath("/x"), Op: ownership.PendingOpSet, Expected: observation.MustPresentLeaf(lkal.Int(1)), Target: lkal.Int(2)}}
}

// #7, #26, #40, #43: no process-local inode state may be required for recovery.
func TestReviewReplacementSurvivesProviderRestart(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			_, reg, p := reviewFile(t)
			waiting, err := provider.NewLKALFileProvider(reg)
			if err != nil {
				t.Fatal(err)
			}
			if interrupted {
				p.Hooks.AfterRenameBeforeDirSync = func(ownership.ResourceID, string) error { return errors.New("sync failure") }
			}
			r := p.ApplyBatch("res", "batch", reviewOps())
			want := provider.ReceiptApplied
			if interrupted {
				want = provider.ReceiptIndeterminate
			}
			if r.Outcome != want {
				t.Fatalf("receipt: %+v", r)
			}
			fresh, err := provider.NewLKALFileProvider(reg)
			if err != nil {
				t.Fatal(err)
			}
			for _, observer := range []*provider.LKALFileProvider{waiting, fresh} {
				snap, err := observer.Observe("res", model.MustParsePath("/x"))
				if err != nil {
					t.Fatal(err)
				}
				if !snap.Get(model.MustParsePath("/x")).Equal(observation.MustPresentLeaf(lkal.Int(2))) {
					t.Fatal("lost target")
				}
				if got := observer.Stabilize("res", snap); got.Outcome != provider.StabilizeDurable {
					t.Fatalf("stabilize: %+v", got)
				}
			}
		})
	}
}

// #1, #28: a temporary filename collision must never delete an existing file,
// even when the batch is NotApplied or its correlation ID contains separators.
func TestReviewTemporaryFilesNeverDeleteUnrelatedFiles(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			target, _, p := reviewFile(t)
			collision := target + ".kisage-tmp-collision"
			const content = "unmanaged file"
			if err := os.WriteFile(collision, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if fail {
				p.Hooks.BeforeTempWrite = func(ownership.ResourceID, string) error { return errors.New("write failed") }
			}
			r := p.ApplyBatch("res", "collision", reviewOps())
			want := provider.ReceiptApplied
			if fail {
				want = provider.ReceiptNotApplied
			}
			if r.Outcome != want {
				t.Fatalf("receipt: %+v", r)
			}
			b, err := os.ReadFile(collision)
			if err != nil || string(b) != content {
				t.Fatalf("unrelated file changed: %q %v", b, err)
			}
		})
	}
	target, _, p := reviewFile(t)
	sentinel := filepath.Join(filepath.Dir(target), "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := p.ApplyBatch("res", "../../sentinel", reviewOps()); r.Outcome != provider.ReceiptApplied {
		t.Fatalf("receipt: %+v", r)
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "keep" {
		t.Fatal("batch ID affected a filesystem path")
	}
}

// #17, #33: all unsupported xattrs, including lists larger than 1024 bytes,
// must cause rejection before replacement, preserving the original attributes.
func TestReviewLargeExtendedAttributeListFailsClosed(t *testing.T) {
	target, _, p := reviewFile(t)
	var names []string
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("user.%03d%s", i, strings.Repeat("a", 145))
		if err := syscall.Setxattr(target, name, []byte("preserve"), 0); err != nil {
			if err == syscall.ENOTSUP {
				t.Skip("filesystem does not support xattrs")
			}
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if r := p.ApplyBatch("res", "attrs", reviewOps()); r.Outcome != provider.ReceiptNotApplied {
		t.Fatalf("unsupported attributes accepted: %+v", r)
	}
	for _, name := range names {
		if _, err := syscall.Getxattr(target, name, nil); err != nil {
			t.Fatalf("lost attribute %s: %v", name, err)
		}
	}
}

func TestReviewReplacementPreservesOwnerAndMode(t *testing.T) {
	target, _, p := reviewFile(t)
	var before, after syscall.Stat_t
	if err := syscall.Stat(target, &before); err != nil {
		t.Fatal(err)
	}
	if r := p.ApplyBatch("res", "owner", reviewOps()); r.Outcome != provider.ReceiptApplied {
		t.Fatalf("receipt: %+v", r)
	}
	if err := syscall.Stat(target, &after); err != nil {
		t.Fatal(err)
	}
	if before.Uid != after.Uid || before.Gid != after.Gid || before.Mode != after.Mode {
		t.Fatalf("metadata changed: before=%+v after=%+v", before, after)
	}
}

func TestReviewReplacementPreservesSupplementaryGroup(t *testing.T) {
	target, _, p := reviewFile(t)
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	group := -1
	for _, candidate := range groups {
		if candidate != os.Getegid() {
			group = candidate
			break
		}
	}
	if group < 0 {
		t.Skip("no supplementary group available")
	}
	if err := os.Chown(target, os.Geteuid(), group); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) {
			t.Skipf("filesystem does not permit group setup: %v", err)
		}
		t.Fatal(err)
	}
	r := p.ApplyBatch("res", "group", reviewOps())
	if r.Outcome != provider.ReceiptApplied {
		t.Fatalf("could not preserve group: %+v", r)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(target, &st); err != nil {
		t.Fatal(err)
	}
	if st.Gid != uint32(group) {
		t.Fatalf("group changed: got %d want %d", st.Gid, group)
	}
}

func TestReviewSpecialModesAreRejected(t *testing.T) {
	target, _, p := reviewFile(t)
	if err := os.Chmod(target, 0o640|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if r := p.ApplyBatch("res", "mode", reviewOps()); r.Outcome != provider.ReceiptNotApplied {
		t.Fatalf("special mode was silently lost: %+v", r)
	}
}

func TestReviewSymlinkBeforeManagementRootRejected(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(real, "file.lkal")
	if err := os.WriteFile(target, []byte("@lkal \"0.1\"\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.InspectLocalFile(filepath.Join(link), filepath.Join(link, "file.lkal")); err == nil {
		t.Fatal("symlink management root accepted")
	}
	sub := filepath.Join(real, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.lkal"), []byte("@lkal \"0.1\"\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.InspectLocalFile(filepath.Join(link, "sub"), filepath.Join(link, "sub", "file.lkal")); err == nil {
		t.Fatal("symlink before management root accepted")
	}
}

func TestReviewLockIDsCannotEscapeProtectedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	locks, err := provider.NewFileLockManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := locks.Acquire("../../outside-store", "../../outside-resource")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Unlock(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("lock files escaped directory: %v", files)
	}
	for _, f := range files {
		if strings.Contains(f.Name(), "outside") {
			t.Fatal("raw identifier used as pathname")
		}
	}
}

func TestReviewWritableManagedAncestorRejected(t *testing.T) {
	target, reg, _ := reviewFile(t)
	if err := os.Chmod(filepath.Dir(target), 0o770); err != nil {
		t.Fatal(err)
	}
	p, err := provider.NewLKALFileProvider(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Observe("res", model.MustParsePath("/x")); err == nil {
		t.Fatal("group-writable management root accepted")
	}
}
