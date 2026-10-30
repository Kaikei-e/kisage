// Package host loads the protected Linux runtime configuration shared by CLI and MCP.
package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// Config is host policy, never part of an LKAL declaration or MCP arguments.
// One runtime serializes all its resources through one store and lock directory.
type Config struct {
	Version        int               `json:"version"`
	Actor          ownership.ActorID `json:"actor"`
	StoreID        ownership.StoreID `json:"storeId"`
	StoreDirectory string            `json:"storeDirectory"`
	LockDirectory  string            `json:"lockDirectory"`
	Resources      []ResourceConfig  `json:"resources"`
}

type ResourceConfig struct {
	ID              ownership.ResourceID `json:"id"`
	ManagementRoot  string               `json:"managementRoot"`
	TargetPath      string               `json:"targetPath"`
	DeviceID        uint64               `json:"deviceId"`
	DeclarationFile string               `json:"declarationFile"`
	Scope           []RuleConfig         `json:"scope"`
}

type RuleConfig struct {
	Path        string   `json:"path"`
	Match       string   `json:"match"`
	Permissions []string `json:"permissions"`
}

// LoadConfig rejects links, special files, unknown fields and unprotected policy.
// The host must also protect ancestor directories and the configuration path's
// injection source; Unix mode checks cannot isolate callers sharing its UID.
func LoadConfig(path string) (*Config, error) {
	if err := absolutePath(path); err != nil {
		return nil, err
	}
	meta, raw, err := provider.InspectLocalFile(filepath.Dir(path), path)
	if err != nil {
		return nil, err
	}
	if err := provider.ValidateLocalFilesystemMagic(meta.FilesystemMagic); err != nil {
		return nil, err
	}
	if meta.Physical.LinkCount != 1 || meta.Mode.Perm()&0o077 != 0 ||
		(meta.UID != uint32(os.Geteuid()) && meta.UID != 0) || meta.ParentWritableOther || len(meta.UnsupportedXattrs) > 0 {
		return nil, fmt.Errorf("host configuration must be an owner-only regular file owned by root or the runtime in a protected directory")
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing host configuration data")
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported host configuration version %d", cfg.Version)
	}
	if err := ownership.ValidateActorID(cfg.Actor); err != nil {
		return nil, err
	}
	if err := ownership.ValidateStoreID(cfg.StoreID); err != nil {
		return nil, err
	}
	for _, p := range []string{cfg.StoreDirectory, cfg.LockDirectory} {
		if err := absolutePath(p); err != nil {
			return nil, err
		}
	}
	if cfg.StoreDirectory == cfg.LockDirectory {
		return nil, fmt.Errorf("host configuration StoreDirectory and LockDirectory must be distinct")
	}
	if len(cfg.Resources) == 0 {
		return nil, fmt.Errorf("host configuration requires at least one resource")
	}
	return &cfg, nil
}

// Runtime owns the repository handle used by all invocations in a process.
type Runtime struct {
	Host *cli.HostEnvironment
	repo *protocol.FileStoreRepository
}

func (r *Runtime) Close() error { return r.repo.Close() }

// Open uses existing state only: missing or corrupt stores never become empty stores.
func Open(configPath string) (*Runtime, error) {
	cfg, reg, bindings, locks, err := prepare(configPath)
	if err != nil {
		return nil, err
	}
	repo, err := protocol.OpenFileStoreRepository(cfg.StoreDirectory)
	if err != nil {
		return nil, err
	}
	prov, err := provider.NewLKALFileProvider(reg)
	if err != nil {
		_ = repo.Close()
		return nil, err
	}
	env, err := cli.NewHostEnvironment(cli.HostEnvironmentConfig{HostActor: cfg.Actor, Registry: reg, DeclarationBindings: bindings, Provider: prov, StoreRepo: repo, Locks: locks})
	if err != nil {
		_ = repo.Close()
		return nil, err
	}
	return &Runtime{Host: env, repo: repo}, nil
}

// InitializeStore is the explicit one-time administrative setup operation. It
// uses the same shared-store lock as execution and never overwrites existing state.
func InitializeStore(configPath string) error {
	cfg, _, _, locks, err := prepare(configPath)
	if err != nil {
		return err
	}
	lease, err := locks.Acquire(cfg.StoreID, cfg.Resources[0].ID)
	if err != nil {
		return err
	}
	defer lease.Unlock()
	repo, err := protocol.InitializeFileStoreRepository(cfg.StoreDirectory, nil)
	if err != nil {
		return err
	}
	return repo.Close()
}

func prepare(path string) (*Config, *ownership.ResourceRegistry, []cli.DeclarationBinding, *provider.FileLockManager, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var resources []ownership.ResourceBinding
	var bindings []cli.DeclarationBinding
	for _, r := range cfg.Resources {
		physical, err := provider.InspectPhysicalIdentity(r.TargetPath)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if r.DeviceID == 0 || physical.DeviceID != r.DeviceID {
			return nil, nil, nil, nil, fmt.Errorf("resource %q device differs from protected registration", r.ID)
		}
		resources = append(resources, ownership.ResourceBinding{ID: r.ID, Provider: ownership.ProviderLKALFile, ManagementRoot: r.ManagementRoot, TargetPath: r.TargetPath, StoreID: cfg.StoreID, Physical: physical})
		sc, err := buildScope(r.Scope)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		binding := cli.DeclarationBinding{DeclarationFile: r.DeclarationFile, Resource: r.ID, Scope: sc}
		if err := binding.Validate(); err != nil {
			return nil, nil, nil, nil, err
		}
		bindings = append(bindings, binding)
	}
	reg, err := ownership.NewResourceRegistry(resources...)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// Startup of a read-only command must not create configuration directories.
	// Provisioning the lock/store directories is an explicit host setup step.
	if _, err := os.Lstat(cfg.LockDirectory); err != nil {
		return nil, nil, nil, nil, err
	}
	locks, err := provider.NewFileLockManager(cfg.LockDirectory)
	return cfg, reg, bindings, locks, err
}

func buildScope(config []RuleConfig) (scope.Scope, error) {
	var rules []scope.Rule
	for _, r := range config {
		var perms scope.PermissionSet
		for _, name := range r.Permissions {
			switch name {
			case "Set":
				perms |= scope.PermissionSet(scope.PermSet)
			case "Delete":
				perms |= scope.PermissionSet(scope.PermDelete)
			case "Own":
				perms |= scope.PermissionSet(scope.PermOwn)
			case "Resolve":
				perms |= scope.PermissionSet(scope.PermResolve)
			default:
				return scope.Scope{}, fmt.Errorf("unknown scope permission %q", name)
			}
		}
		var rule scope.Rule
		var err error
		if r.Match == "root" {
			if r.Path != "" && r.Path != "/" {
				return scope.Scope{}, fmt.Errorf("root scope rule must use path / or an empty path")
			}
			rule, err = scope.NewRootRule(perms)
		} else {
			var p model.Path
			p, err = model.ParsePath(r.Path)
			if err == nil {
				switch r.Match {
				case "exact":
					rule, err = scope.NewExactRule(p, perms)
				case "subtree":
					rule, err = scope.NewSubtreeRule(p, perms)
				default:
					err = fmt.Errorf("unknown scope match %q", r.Match)
				}
			}
		}
		if err != nil {
			return scope.Scope{}, err
		}
		rules = append(rules, rule)
	}
	return scope.New(rules...)
}

func absolutePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("host paths must be clean absolute paths: %q", path)
	}
	return nil
}
