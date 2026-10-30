package protocol

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

const repositoryFile = "ownership.json"
const runtimeHaltFile = "runtime-halted"

// FileStoreFaultHooks identify actual file durability boundaries. An error
// after rename is always Indeterminate; earlier errors leave the old state.
type FileStoreFaultHooks struct {
	BeforeWrite         func(StoreCommitStage) error
	BeforeSync          func(StoreCommitStage) error
	BeforeRename        func(StoreCommitStage) error
	AfterRename         func(StoreCommitStage) error
	BeforeDirectorySync func(StoreCommitStage) error
}

// FileStoreRepository atomically persists ownership (including revision
// tombstones and Pending) and quarantine in one checksummed, versioned file.
// Callers hold the shared store lock across Load/Save, as ExecuteReconcile does.
// Construction never silently initializes missing or corrupt state.
type FileStoreRepository struct {
	mu      sync.Mutex
	root    *os.Root
	dir     string
	haltErr error
	Hooks   FileStoreFaultHooks
}

type repositoryEnvelope struct {
	Version int             `json:"version"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}
type repositoryPayload struct {
	Generation uint64             `json:"generation"`
	Store      *ownership.Store   `json:"store"`
	Quarantine []QuarantineRecord `json:"quarantine"`
}

// A failed halt-marker write cannot authorize a fresh repository in this process.
// Restart after such a failure still requires the external host to fail closed.
var haltedRepositoryPaths sync.Map

func openRepositoryRoot(dir string) (*FileStoreRepository, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, fmt.Errorf("store directory must be a clean absolute path")
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if canonical != dir {
		return nil, fmt.Errorf("store directory must not contain symlinks")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || st.Uid != uint32(os.Geteuid()) {
			err = fmt.Errorf("store directory must be owned by the runtime and accessible only to its owner")
		}
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	r := &FileStoreRepository{root: root, dir: dir}
	if err := r.checkHalted(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return r, nil
}

// InitializeFileStoreRepository is an explicit, one-time administrative action.
// dir must already exist with mode 0700; existing state is never overwritten.
func InitializeFileStoreRepository(dir string, initial *ownership.Store) (*FileStoreRepository, error) {
	r, err := openRepositoryRoot(dir)
	if err != nil {
		return nil, err
	}
	if initial == nil {
		initial = ownership.NewStore()
	}
	data, err := encodeRepository(repositoryPayload{Generation: 1, Store: initial})
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	f, err := r.root.OpenFile(repositoryFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = r.syncDirectory()
	}
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

func OpenFileStoreRepository(dir string) (*FileStoreRepository, error) {
	r, err := openRepositoryRoot(dir)
	if err != nil {
		return nil, err
	}
	if _, err = r.readState(false); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

func (r *FileStoreRepository) Close() error { return r.root.Close() }

func (r *FileStoreRepository) checkHalted() error {
	if r.haltErr != nil {
		return r.haltErr
	}
	if _, ok := haltedRepositoryPaths.Load(r.dir); ok {
		return ErrRuntimeHalted
	}
	if _, err := r.root.Lstat(runtimeHaltFile); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return ErrRuntimeHalted
	}
	return nil
}

func (r *FileStoreRepository) HaltRuntime(cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.haltErr = fmt.Errorf("%w: %v", ErrRuntimeHalted, cause)
	haltedRepositoryPaths.Store(r.dir, true)
	f, err := r.root.OpenFile(runtimeHaltFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, _ = f.Write([]byte(r.haltErr.Error()))
		_ = f.Sync()
		_ = f.Close()
		_ = r.syncDirectory()
	}
}

func (r *FileStoreRepository) LoadStore() (*ownership.Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.readState(true)
	if err != nil {
		return nil, err
	}
	return doc.Store, nil
}
func (r *FileStoreRepository) LoadQuarantine() (*QuarantineTable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.readState(true)
	if err != nil {
		return nil, err
	}
	return quarantineFromRecords(doc.Quarantine)
}

func (r *FileStoreRepository) SaveStore(stage StoreCommitStage, next *ownership.Store) CommitResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.readState(true)
	if err != nil {
		return storeFailure(CommitFailed, err)
	}
	if next == nil {
		return storeFailure(CommitFailed, fmt.Errorf("cannot save nil store"))
	}
	doc.Store = next
	return r.save(stage, doc)
}
func (r *FileStoreRepository) SaveQuarantine(next *QuarantineTable) CommitResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.readState(true)
	if err != nil {
		return storeFailure(CommitFailed, err)
	}
	if next == nil {
		return storeFailure(CommitFailed, fmt.Errorf("cannot save nil quarantine table"))
	}
	doc.Quarantine = nil
	// Resource ordering is canonical across process restarts.
	for _, res := range next.Resources() {
		rec, _ := next.Get(res)
		doc.Quarantine = append(doc.Quarantine, rec)
	}
	return r.save(StageQuarantineSave, doc)
}

// ReadOnlyState reads both tables for plan/explain without fsync or writes.
// Visibility is not a durability claim; virtual recovery remains conditional.
func (r *FileStoreRepository) ReadOnlyState() (*ownership.Store, *QuarantineTable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.readState(false)
	if err != nil {
		return nil, nil, err
	}
	qt, err := quarantineFromRecords(doc.Quarantine)
	return doc.Store, qt, err
}

func (r *FileStoreRepository) readState(durable bool) (repositoryPayload, error) {
	if err := r.checkHalted(); err != nil {
		return repositoryPayload{}, err
	}
	dir, err := r.root.Open(".")
	if err != nil {
		return repositoryPayload{}, err
	}
	defer dir.Close()
	fd, err := syscall.Openat(int(dir.Fd()), repositoryFile, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return repositoryPayload{}, err
	}
	f := os.NewFile(uintptr(fd), repositoryFile)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return repositoryPayload{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return repositoryPayload{}, fmt.Errorf("unsafe ownership store file")
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return repositoryPayload{}, err
	}
	var envelope repositoryEnvelope
	if err := decodeStrict(data, &envelope); err != nil {
		return repositoryPayload{}, err
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Version != 1 || envelope.SHA256 != hex.EncodeToString(sum[:]) {
		return repositoryPayload{}, fmt.Errorf("invalid ownership store version or checksum")
	}
	var doc repositoryPayload
	if err := decodeStrict(envelope.Payload, &doc); err != nil {
		return repositoryPayload{}, err
	}
	if doc.Store == nil || doc.Generation == 0 {
		return repositoryPayload{}, fmt.Errorf("missing store or generation")
	}
	if _, err := quarantineFromRecords(doc.Quarantine); err != nil {
		return repositoryPayload{}, err
	}
	// Reload resolves a previous uncertain rename only after the observed file
	// and directory are durable, rather than relying on OS-cache visibility.
	if durable {
		if err := f.Sync(); err != nil {
			return repositoryPayload{}, err
		}
		if err := dir.Sync(); err != nil {
			return repositoryPayload{}, err
		}
	}
	return doc, nil
}

func (r *FileStoreRepository) save(stage StoreCommitStage, doc repositoryPayload) CommitResult {
	if doc.Generation == math.MaxUint64 {
		return storeFailure(CommitFailed, fmt.Errorf("store generation exhausted"))
	}
	doc.Generation++
	data, err := encodeRepository(doc)
	if err != nil {
		return storeFailure(CommitFailed, err)
	}
	if err := runStoreHook(r.Hooks.BeforeWrite, stage); err != nil {
		return storeFailure(CommitFailed, err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return storeFailure(CommitFailed, err)
	}
	name := ".ownership-tmp-" + hex.EncodeToString(random[:])
	f, err := r.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return storeFailure(CommitFailed, err)
	}
	defer f.Close()
	defer r.root.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = runStoreHook(r.Hooks.BeforeSync, stage)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = runStoreHook(r.Hooks.BeforeRename, stage)
	}
	if err != nil {
		return storeFailure(CommitFailed, err)
	}
	if err := r.root.Rename(name, repositoryFile); err != nil {
		return storeFailure(CommitIndeterminate, err)
	}
	if err := runStoreHook(r.Hooks.AfterRename, stage); err != nil {
		return storeFailure(CommitIndeterminate, err)
	}
	if err := runStoreHook(r.Hooks.BeforeDirectorySync, stage); err != nil {
		return storeFailure(CommitIndeterminate, err)
	}
	if err := r.syncDirectory(); err != nil {
		return storeFailure(CommitIndeterminate, err)
	}
	return CommitResult{Status: CommitSucceeded}
}

func encodeRepository(doc repositoryPayload) ([]byte, error) {
	if doc.Store == nil {
		return nil, fmt.Errorf("missing ownership store")
	}
	if err := doc.Store.Validate(); err != nil {
		return nil, err
	}
	if _, err := quarantineFromRecords(doc.Quarantine); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	return json.Marshal(repositoryEnvelope{Version: 1, SHA256: hex.EncodeToString(sum[:]), Payload: payload})
}
func decodeStrict(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing repository data")
	}
	return nil
}
func quarantineFromRecords(records []QuarantineRecord) (*QuarantineTable, error) {
	qt := NewQuarantineTable()
	for _, rec := range records {
		if qt.IsQuarantined(rec.Resource) {
			return nil, fmt.Errorf("duplicate resource quarantine")
		}
		var err error
		qt, err = qt.Quarantine(rec)
		if err != nil {
			return nil, err
		}
	}
	return qt, nil
}
func (r *FileStoreRepository) syncDirectory() error {
	dir, err := r.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func runStoreHook(hook func(StoreCommitStage) error, stage StoreCommitStage) error {
	if hook != nil {
		return hook(stage)
	}
	return nil
}
func storeFailure(status CommitStatus, err error) CommitResult {
	code := model.CodeInvalidInput
	if status == CommitIndeterminate {
		code = model.CodeIndeterminateOutcome
	}
	return CommitResult{Status: status, Diagnostics: []model.Diagnostic{{Code: code, Reason: "ownership persistence failed: " + err.Error(), Cause: err}}}
}
