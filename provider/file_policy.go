package provider

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

// Common Linux network filesystem magic numbers rejected by local file providers (ADR 0004 §5).
const (
	fsMagicNFS  int64 = 0x6969
	fsMagicSMB  int64 = 0x517b
	fsMagicSMB2 int64 = 0xfe534d42
	fsMagicCIFS int64 = 0xff534d42
	fsMagicFUSE int64 = 0x65735546
	fsMagicAFS  int64 = 0x5346414f
	fsMagicCeph int64 = 0x00c36400
	fsMagic9P   int64 = 0x01021997
)

// FileMetadata captures the physical identity, file mode, owner, filesystem type, and extended
// attribute status of a target file and its path components (ADR 0004 §5, Counterexamples #17, #33).
type FileMetadata struct {
	Physical            ownership.PhysicalIdentity
	Mode                os.FileMode
	UID                 uint32
	GID                 uint32
	FilesystemMagic     int64
	ParentDeviceID      uint64
	ParentInode         uint64
	RootDeviceID        uint64
	ParentWritableOther bool
	HasSymlinkComponent bool
	HasSpecialComponent bool
	UnsupportedXattrs   []string
}

// ValidateDeclarationTargetDistinct checks that the declaration file and target file are distinct
// both by canonical path and by physical (DeviceID, Inode) identity (ADR 0004 §5).
func ValidateDeclarationTargetDistinct(
	declPath string,
	declPhysical ownership.PhysicalIdentity,
	targetPath string,
	targetPhysical ownership.PhysicalIdentity,
) error {
	if declPath != "" && targetPath != "" && filepath.Clean(declPath) == filepath.Clean(targetPath) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("declaration file %q and target file %q must be distinct files", declPath, targetPath),
		}
	}
	if declPhysical.SamePhysicalFile(targetPhysical) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("declaration file and target file refer to the same physical inode (dev=%d, ino=%d)", targetPhysical.DeviceID, targetPhysical.Inode),
		}
	}
	return nil
}

// ValidateNetworkFilesystem Magic rejects known network/remote filesystems (ADR 0004 §5).
func ValidateLocalFilesystemMagic(magic int64) error {
	switch magic {
	case fsMagicNFS, fsMagicSMB, fsMagicSMB2, fsMagicCIFS, fsMagicFUSE, fsMagicAFS, fsMagicCeph, fsMagic9P:
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("network or remote filesystem (magic=0x%x) is not supported by local LKAL file provider", magic),
		}
	default:
		return nil
	}
}

// ValidateMountBoundary checks that ManagementRoot, the parent directory, and the target file
// all reside on the same filesystem device (no mount boundary crossing, ADR 0004 §5).
func ValidateMountBoundary(rootDev, parentDev, targetDev uint64) error {
	if rootDev == 0 || parentDev == 0 || targetDev == 0 {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "filesystem device IDs for root, parent directory, and target file must be non-zero",
		}
	}
	if rootDev != parentDev || parentDev != targetDev {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("mount boundary crossed between management root (dev=%d), parent directory (dev=%d), and target file (dev=%d)", rootDev, parentDev, targetDev),
		}
	}
	return nil
}

// ValidateTargetFileMetadata purely verifies all ADR 0004 §5 security and preservation contracts
// for a target file against its registered ResourceBinding (Counterexamples #17, #33):
//   - No symlink or special file (FIFO, device, socket) along any path component or at the leaf.
//   - Regular file mode (Mode.IsRegular()).
//   - Managed directory components are not writable by group/other users.
//   - Local filesystem (not network FS) and single mount boundary (Root == Parent == Target DeviceID).
//   - Registered device matches; inode replacement is permitted at the protected pathname. LinkCount == 1.
//   - No unsupported ACLs or extended attributes that would be lost across atomic file replacement.
func ValidateTargetFileMetadata(binding ownership.ResourceBinding, meta FileMetadata) error {
	if err := ownership.ValidateResourceBinding(binding); err != nil {
		return err
	}
	if meta.HasSymlinkComponent {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q target %q contains a symbolic link in its path components", binding.ID, binding.TargetPath),
		}
	}
	if meta.HasSpecialComponent || !meta.Mode.IsRegular() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q target %q must be a regular file with directory ancestors (mode=%v)", binding.ID, binding.TargetPath, meta.Mode),
		}
	}
	if meta.ParentWritableOther {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q has a managed directory writable by group or other users; untrusted directory modification is forbidden", binding.ID),
		}
	}
	if err := ValidateLocalFilesystemMagic(meta.FilesystemMagic); err != nil {
		return err
	}
	if err := ValidateMountBoundary(meta.RootDeviceID, meta.ParentDeviceID, meta.Physical.DeviceID); err != nil {
		return err
	}
	if err := meta.Physical.Validate(); err != nil {
		return err
	}
	if meta.Physical.LinkCount > 1 {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q target %q has hard link count %d; hard links are rejected", binding.ID, binding.TargetPath, meta.Physical.LinkCount),
		}
	}
	// The protected pathname is the durable binding. Atomic replacement changes
	// the inode, including across processes and restarts; the device stays pinned.
	if binding.Physical.DeviceID != meta.Physical.DeviceID {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q physical file identity changed: registered (dev=%d, ino=%d), opened handle is (dev=%d, ino=%d)", binding.ID, binding.Physical.DeviceID, binding.Physical.Inode, meta.Physical.DeviceID, meta.Physical.Inode),
		}
	}
	if meta.Mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return model.Diagnostic{Code: model.CodeInvalidInput, Reason: "special permission bits are unsupported by atomic replacement"}
	}
	if len(meta.UnsupportedXattrs) > 0 {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q target %q has unsupported ACL or extended attribute %q that cannot be preserved across atomic replacement", binding.ID, binding.TargetPath, meta.UnsupportedXattrs[0]),
		}
	}
	return nil
}
