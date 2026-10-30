package provider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

func (p *LKALFileProvider) replaceAndSyncWithRoot(
	binding ownership.ResourceBinding,
	meta FileMetadata,
	batchID string,
	patched []byte,
) Receipt {
	root, err := openPinnedParent(binding, meta)
	if err != nil {
		return receiptFromError(batchID, nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("os.OpenRoot(%q) failed: %v", binding.ManagementRoot, err),
			Cause:  err,
		})
	}
	defer root.Close()

	relTarget := filepath.Base(binding.TargetPath)
	relTemp, prepErr := p.prepareAndSyncTempFile(root, binding, meta, batchID, patched)
	if prepErr != nil {
		return receiptFromError(batchID, nil, prepErr)
	}

	if p.Hooks.BeforeRename != nil {
		if hookErr := p.Hooks.BeforeRename(binding.ID, batchID); hookErr != nil {
			_ = root.Remove(relTemp)
			return receiptFromError(batchID, nil, hookErr)
		}
	}

	if err := root.Rename(relTemp, relTarget); err != nil {
		_ = root.Remove(relTemp)
		return indeterminateReceipt(batchID, fmt.Sprintf("os.Root.Rename(%q -> %q) failed: %v", relTemp, relTarget, err), err)
	}

	if p.Hooks.AfterRenameBeforeDirSync != nil {
		if hookErr := p.Hooks.AfterRenameBeforeDirSync(binding.ID, batchID); hookErr != nil {
			return indeterminateReceipt(batchID, fmt.Sprintf("post-rename fault before parent directory fsync: %v", hookErr), hookErr)
		}
	}
	if err := syncRootDirectory(root); err != nil {
		return indeterminateReceipt(batchID, fmt.Sprintf("parent directory fsync failed after Root.Rename: %v", err), err)
	}

	return Receipt{
		BatchID: batchID,
		Outcome: ReceiptApplied,
	}
}

func (p *LKALFileProvider) prepareAndSyncTempFile(
	root *os.Root,
	binding ownership.ResourceBinding,
	meta FileMetadata,
	batchID string,
	patched []byte,
) (name string, err error) {
	if p.Hooks.BeforeTempWrite != nil {
		if err := p.Hooks.BeforeTempWrite(binding.ID, batchID); err != nil {
			return "", err
		}
	}

	var tf *os.File
	for attempt := 0; attempt < 10; attempt++ {
		var random [16]byte
		if _, err = rand.Read(random[:]); err != nil {
			return "", err
		}
		name = ".kisage-tmp-" + hex.EncodeToString(random[:])
		tf, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !os.IsExist(err) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	defer tf.Close()
	tempName := name
	// Only remove a temporary file that this call successfully created.
	defer func() {
		if err != nil {
			_ = root.Remove(tempName)
		}
	}()

	var st syscall.Stat_t
	if err := syscall.Fstat(int(tf.Fd()), &st); err != nil {
		return "", err
	}
	if uint64(st.Dev) != meta.Physical.DeviceID || (st.Mode&syscall.S_IFMT) != syscall.S_IFREG {
		return name, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("temporary file %q violated same-device or regular-file invariant", name),
		}
	}

	if _, err := tf.Write(patched); err != nil {
		return name, err
	}
	if st.Uid != meta.UID || st.Gid != meta.GID {
		if err := tf.Chown(int(meta.UID), int(meta.GID)); err != nil {
			return name, err
		}
	}
	if err := tf.Chmod(meta.Mode.Perm()); err != nil {
		return name, err
	}
	if p.Hooks.BeforeTempSync != nil {
		if err := p.Hooks.BeforeTempSync(binding.ID, batchID); err != nil {
			return name, err
		}
	}
	if err := tf.Sync(); err != nil {
		return name, err
	}
	return name, nil
}

func (p *LKALFileProvider) syncFileAndParent(binding ownership.ResourceBinding) error {
	meta, _, err := InspectLocalFile(binding.ManagementRoot, binding.TargetPath)
	if err != nil {
		return err
	}
	if err := ValidateTargetFileMetadata(binding, meta); err != nil {
		return err
	}
	root, err := openPinnedParent(binding, meta)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	fd, err := syscall.Openat(int(dir.Fd()), filepath.Base(binding.TargetPath), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return err
	}
	if (st.Mode&syscall.S_IFMT) != syscall.S_IFREG || uint64(st.Ino) != meta.Physical.Inode || uint64(st.Dev) != meta.Physical.DeviceID {
		_ = syscall.Close(fd)
		return fmt.Errorf("target %q is not a regular file", binding.TargetPath)
	}
	syncErr := syscall.Fsync(fd)
	_ = syscall.Close(fd)
	if syncErr != nil {
		return syncErr
	}
	return dir.Sync()
}

func syncDirectoryNoFollow(dirPath string) error {
	fd, err := syscall.Open(dirPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	syncErr := syscall.Fsync(fd)
	_ = syscall.Close(fd)
	return syncErr
}

func openPinnedParent(binding ownership.ResourceBinding, meta FileMetadata) (*os.Root, error) {
	root, err := os.OpenRoot(filepath.Dir(binding.TargetPath))
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint64(st.Dev) != meta.ParentDeviceID || uint64(st.Ino) != meta.ParentInode {
			err = fmt.Errorf("target parent changed during inspection")
		}
	}
	if err == nil {
		info, err = root.Lstat(filepath.Base(binding.TargetPath))
		if err == nil {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.Mode().IsRegular() || uint64(st.Dev) != meta.Physical.DeviceID || uint64(st.Ino) != meta.Physical.Inode || st.Nlink != 1 {
				err = fmt.Errorf("target changed during inspection")
			}
		}
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func syncRootDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func indeterminateReceipt(batchID, reason string, cause error) Receipt {
	return Receipt{
		BatchID: batchID,
		Outcome: ReceiptIndeterminate,
		Diagnostics: []model.Diagnostic{{
			Code:       model.CodeIndeterminateOutcome,
			Reason:     reason,
			NextAction: "re-observe and run recovery to determine whether the file replacement took effect",
			Cause:      cause,
		}},
	}
}
