package provider

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

// MaxInspectedFileBytes bounds the maximum regular file size read into memory by InspectLocalFile.
const MaxInspectedFileBytes = 64 << 20

// InspectPhysicalIdentity inspects an existing regular file at path without following symlinks
// and returns its PhysicalIdentity (DeviceID, Inode, LinkCount).
func InspectPhysicalIdentity(path string) (ownership.PhysicalIdentity, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return ownership.PhysicalIdentity{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("path %q must be an absolute path", path),
		}
	}
	meta, _, err := InspectLocalFile(filepath.Dir(clean), clean)
	if err != nil {
		return ownership.PhysicalIdentity{}, err
	}
	return meta.Physical, nil
}

// InspectLocalFile traverses each component from rootPath to targetPath using O_NOFOLLOW | O_NONBLOCK
// and open file descriptor fstat/fstatfs checks to prevent symlink, FIFO/device/socket, and TOCTOU
// substitution attacks (ADR 0004 §5, Counterexample #17).
func InspectLocalFile(rootPath, targetPath string) (FileMetadata, []byte, error) {
	rel, err := validateRelativeTarget(rootPath, targetPath)
	if err != nil {
		return FileMetadata{}, nil, err
	}

	rootFD, rootStat, fsMagic, err := openRootDirectory(rootPath)
	if err != nil {
		return FileMetadata{}, nil, err
	}
	defer syscall.Close(rootFD)

	meta := FileMetadata{
		RootDeviceID:        uint64(rootStat.Dev),
		FilesystemMagic:     fsMagic,
		ParentWritableOther: rootStat.Mode&0o022 != 0,
	}

	parts := strings.Split(rel, string(filepath.Separator))
	parentFD, parentStat, compErr := walkDirectoryComponents(rootFD, parts[:len(parts)-1], &meta)
	if compErr != nil {
		return meta, nil, compErr
	}
	if parentFD != rootFD {
		defer syscall.Close(parentFD)
	}
	meta.ParentDeviceID = uint64(parentStat.Dev)
	meta.ParentInode = uint64(parentStat.Ino)
	meta.ParentWritableOther = meta.ParentWritableOther || parentStat.Mode&0o022 != 0

	leafName := parts[len(parts)-1]
	data, leafErr := inspectAndReadLeafFile(parentFD, leafName, &meta)
	if leafErr != nil {
		return meta, nil, leafErr
	}
	return meta, data, nil
}

func validateRelativeTarget(rootPath, targetPath string) (string, error) {
	if !filepath.IsAbs(rootPath) || !filepath.IsAbs(targetPath) {
		return "", model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "management root and target path must be absolute paths",
		}
	}
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target %q must reside strictly inside management root %q", targetPath, rootPath),
		}
	}
	return rel, nil
}

func openRootDirectory(rootPath string) (int, syscall.Stat_t, int64, error) {
	// O_NOFOLLOW on an absolute path protects only its final component. Walk
	// from / so symlinks before ManagementRoot are rejected as well.
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	fd, err := syscall.Open("/", flags, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, 0, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to open management root %q: %v", rootPath, err),
			Cause:  err,
		}
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(rootPath), "/"), "/") {
		if part == "" {
			continue
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		_ = syscall.Close(fd)
		if err != nil {
			return -1, syscall.Stat_t{}, 0, err
		}
		fd = next
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return -1, syscall.Stat_t{}, 0, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to fstat management root %q: %v", rootPath, err),
			Cause:  err,
		}
	}
	var stfs syscall.Statfs_t
	if err := syscall.Fstatfs(fd, &stfs); err != nil {
		_ = syscall.Close(fd)
		return -1, syscall.Stat_t{}, 0, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to fstatfs management root %q: %v", rootPath, err),
			Cause:  err,
		}
	}
	return fd, st, int64(stfs.Type), nil
}

func walkDirectoryComponents(rootFD int, dirs []string, meta *FileMetadata) (int, syscall.Stat_t, error) {
	curFD := rootFD
	var curStat syscall.Stat_t
	if err := syscall.Fstat(curFD, &curStat); err != nil {
		return -1, syscall.Stat_t{}, err
	}
	for _, name := range dirs {
		if err := classifyEntryBeforeOpen(curFD, name, true, meta); err != nil {
			if curFD != rootFD {
				_ = syscall.Close(curFD)
			}
			return -1, syscall.Stat_t{}, err
		}
		nextFD, err := syscall.Openat(curFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if curFD != rootFD {
			_ = syscall.Close(curFD)
		}
		if err != nil {
			meta.HasSymlinkComponent = true
			return -1, syscall.Stat_t{}, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("directory component %q could not be safely opened with O_NOFOLLOW: %v", name, err),
				Cause:  err,
			}
		}
		if err := syscall.Fstat(nextFD, &curStat); err != nil {
			_ = syscall.Close(nextFD)
			return -1, syscall.Stat_t{}, err
		}
		if uint64(curStat.Dev) != meta.RootDeviceID {
			_ = syscall.Close(nextFD)
			return -1, syscall.Stat_t{}, fmt.Errorf("managed directory crosses a mount boundary")
		}
		meta.ParentWritableOther = meta.ParentWritableOther || curStat.Mode&0o022 != 0
		curFD = nextFD
	}
	return curFD, curStat, nil
}

func inspectAndReadLeafFile(parentFD int, leafName string, meta *FileMetadata) ([]byte, error) {
	if err := classifyEntryBeforeOpen(parentFD, leafName, false, meta); err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(parentFD, leafName, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target file %q could not be opened safely with O_NOFOLLOW|O_NONBLOCK: %v", leafName, err),
			Cause:  err,
		}
	}
	f := os.NewFile(uintptr(fd), leafName)
	defer f.Close()

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to fstat open handle for %q: %v", leafName, err),
			Cause:  err,
		}
	}
	if (st.Mode & syscall.S_IFMT) != syscall.S_IFREG {
		meta.HasSpecialComponent = true
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target %q opened handle is not a regular file (mode=0%o)", leafName, st.Mode),
		}
	}

	meta.Physical = ownership.PhysicalIdentity{
		DeviceID:  uint64(st.Dev),
		Inode:     uint64(st.Ino),
		LinkCount: uint64(st.Nlink),
	}
	meta.Mode = fileModeFromUnix(st.Mode)
	meta.UID = st.Uid
	meta.GID = st.Gid
	meta.Size = st.Size
	meta.Mtime = st.Mtim
	if st.Size < 0 || st.Size > MaxInspectedFileBytes {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target file %q size %d exceeds maximum allowed size (%d bytes)", leafName, st.Size, MaxInspectedFileBytes),
		}
	}
	attrs, err := listUnsupportedXattrs(fd)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect target extended attributes: %w", err)
	}
	meta.UnsupportedXattrs = attrs

	data, err := io.ReadAll(io.LimitReader(f, MaxInspectedFileBytes+1))
	if err != nil {
		return nil, model.Diagnostic{
			Code:   model.CodeObservationUnavailable,
			Reason: fmt.Sprintf("failed to read target file %q: %v", leafName, err),
			Cause:  err,
		}
	}
	if int64(len(data)) > MaxInspectedFileBytes {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target file %q exceeds maximum allowed size (%d bytes)", leafName, MaxInspectedFileBytes),
		}
	}
	return data, nil
}

func classifyEntryBeforeOpen(dirFD int, name string, wantDir bool, meta *FileMetadata) error {
	var st syscall.Stat_t
	if err := fstatAtNoFollow(dirFD, name, &st); err != nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("path component %q does not exist or cannot be inspected: %v", name, err),
			Cause:  err,
		}
	}
	ftype := st.Mode & syscall.S_IFMT
	if ftype == syscall.S_IFLNK {
		meta.HasSymlinkComponent = true
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("path component %q is a symbolic link; symlinks are rejected", name),
		}
	}
	if wantDir && ftype != syscall.S_IFDIR {
		meta.HasSpecialComponent = true
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("ancestor path component %q is not a directory", name),
		}
	}
	if !wantDir && ftype != syscall.S_IFREG {
		meta.HasSpecialComponent = true
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target path component %q is a special file or directory (mode=0%o); only regular files are supported", name, st.Mode),
		}
	}
	return nil
}

func fileModeFromUnix(raw uint32) fs.FileMode {
	mode := fs.FileMode(raw & 0o777)
	switch raw & syscall.S_IFMT {
	case syscall.S_IFDIR:
		mode |= fs.ModeDir
	case syscall.S_IFLNK:
		mode |= fs.ModeSymlink
	case syscall.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case syscall.S_IFSOCK:
		mode |= fs.ModeSocket
	case syscall.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case syscall.S_IFBLK:
		mode |= fs.ModeDevice
	}
	if raw&syscall.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if raw&syscall.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if raw&syscall.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

func listUnsupportedXattrs(fd int) ([]string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		size, _, errno := syscall.Syscall(syscall.SYS_FLISTXATTR, uintptr(fd), 0, 0)
		if errno == syscall.ENOTSUP {
			return nil, nil
		}
		if errno != 0 {
			return nil, errno
		}
		if size == 0 {
			return nil, nil
		}
		if size > 64*1024 {
			return []string{"<xattr-list-too-large>"}, nil
		}
		buf := make([]byte, int(size))
		n, _, errno := syscall.Syscall(syscall.SYS_FLISTXATTR, uintptr(fd), uintptr(unsafeSlicePtr(buf)), uintptr(len(buf)))
		if errno == syscall.ERANGE {
			continue
		}
		if errno != 0 {
			return nil, errno
		}
		var out []string
		for _, raw := range bytes.Split(buf[:n], []byte{0}) {
			if len(raw) > 0 {
				out = append(out, string(raw))
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("extended attribute list changed repeatedly")
}
