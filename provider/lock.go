package provider

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

// LockKind identifies the hierarchy level of a lock in ADR 0004 §2 (SharedStore σ -> Resource).
type LockKind uint8

const (
	// LockKindInvalid is the zero value for an uninitialized LockKind.
	LockKindInvalid LockKind = iota
	// LockKindStore represents the shared ownership store lock (σ), acquired first.
	LockKindStore
	// LockKindResource represents the per-Resource lock, acquired second after σ.
	LockKindResource
)

// String returns the canonical name of LockKind.
func (k LockKind) String() string {
	switch k {
	case LockKindStore:
		return "SharedStore"
	case LockKindResource:
		return "Resource"
	default:
		return "InvalidLockKind"
	}
}

// ValidateLockOrder purely verifies that locks are acquired strictly in the canonical
// SharedStore (σ) -> Resource order and that the lock file path is distinct from the
// replaced target file path (ADR 0004 §2, §5, Counterexample #42).
func ValidateLockOrder(order []LockKind, lockFilePath, targetFilePath string) error {
	if len(order) != 2 || order[0] != LockKindStore || order[1] != LockKindResource {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("invalid lock acquisition order %v: must strictly acquire [SharedStore, Resource]", order),
		}
	}
	if lockFilePath != "" && targetFilePath != "" && filepath.Clean(lockFilePath) == filepath.Clean(targetFilePath) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("lock file %q must be separate from replaced target file %q", lockFilePath, targetFilePath),
		}
	}
	return nil
}

// LockLease represents an active held lock pair (SharedStore σ -> Resource).
type LockLease interface {
	Unlock() error
}

// LockManager acquires the canonical SharedStore σ -> Resource lock pair before a critical section.
type LockManager interface {
	Acquire(storeID ownership.StoreID, resID ownership.ResourceID) (LockLease, error)
}

// FileLockManager implements LockManager using flock(2) on stable lock files stored in a protected
// lock directory completely separate from the replaced target files (ADR 0004 §2, §5, Counterexample #42).
type FileLockManager struct {
	lockDir string
	mu      sync.Mutex
	inProc  map[string]*sync.Mutex
}

// NewFileLockManager creates a FileLockManager rooted at lockDir.
func NewFileLockManager(lockDir string) (*FileLockManager, error) {
	if lockDir == "" || !filepath.IsAbs(lockDir) || filepath.Clean(lockDir) != lockDir {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "lock directory must be a clean, non-empty absolute path",
		}
	}
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to initialize lock directory %q: %v", lockDir, err),
			Cause:  err,
		}
	}
	canonical, err := filepath.EvalSymlinks(lockDir)
	if err != nil || canonical != lockDir {
		return nil, fmt.Errorf("lock directory must not contain symlinks")
	}
	info, err := os.Lstat(lockDir)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || st.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("lock directory must be owned by the runtime and accessible only to its owner")
	}
	return &FileLockManager{
		lockDir: lockDir,
		inProc:  make(map[string]*sync.Mutex),
	}, nil
}

// Acquire locks SharedStore (σ) first and Resource second (ADR 0004 §2).
func (m *FileLockManager) Acquire(storeID ownership.StoreID, resID ownership.ResourceID) (LockLease, error) {
	if err := ownership.ValidateStoreID(storeID); err != nil {
		return nil, err
	}
	if err := ownership.ValidateResourceID(resID); err != nil {
		return nil, err
	}
	if err := ValidateLockOrder([]LockKind{LockKindStore, LockKindResource}, "", ""); err != nil {
		return nil, err
	}

	// IDs are arbitrary identifiers, never filesystem paths.
	storeKey := fmt.Sprintf("store_%x.lock", sha256.Sum256([]byte(storeID)))
	resKey := fmt.Sprintf("resource_%x.lock", sha256.Sum256([]byte(resID)))

	storeMu := m.mutexFor(storeKey)
	resMu := m.mutexFor(resKey)

	// Step 1: Lock SharedStore (σ).
	storeMu.Lock()
	storeFile, err := lockStableFile(filepath.Join(m.lockDir, storeKey))
	if err != nil {
		storeMu.Unlock()
		return nil, err
	}

	// Step 2: Lock Resource.
	resMu.Lock()
	resFile, err := lockStableFile(filepath.Join(m.lockDir, resKey))
	if err != nil {
		resMu.Unlock()
		_ = unlockAndCloseFile(storeFile)
		storeMu.Unlock()
		return nil, err
	}

	return &fileLockLease{
		storeFile: storeFile,
		resFile:   resFile,
		storeMu:   storeMu,
		resMu:     resMu,
	}, nil
}

func (m *FileLockManager) mutexFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.inProc[key]
	if !ok {
		mu = &sync.Mutex{}
		m.inProc[key] = mu
	}
	return mu
}

func lockStableFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to open lock file %q: %v", path, err),
			Cause:  err,
		}
	}
	f := os.NewFile(uintptr(fd), path)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Mode&0o077 != 0 || st.Uid != uint32(os.Geteuid()) {
		_ = f.Close()
		return nil, fmt.Errorf("lock path must be a protected regular file with a single link")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to flock %q: %v", path, err),
			Cause:  err,
		}
	}
	return f, nil
}

func unlockAndCloseFile(f *os.File) error {
	if f == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

type fileLockLease struct {
	once      sync.Once
	storeFile *os.File
	resFile   *os.File
	storeMu   *sync.Mutex
	resMu     *sync.Mutex
	unlockErr error
}

func (l *fileLockLease) Unlock() error {
	l.once.Do(func() {
		err1 := unlockAndCloseFile(l.resFile)
		l.resMu.Unlock()
		err2 := unlockAndCloseFile(l.storeFile)
		l.storeMu.Unlock()
		if err1 != nil {
			l.unlockErr = err1
		} else {
			l.unlockErr = err2
		}
	})
	return l.unlockErr
}
