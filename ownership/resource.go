package ownership

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Kaikei-e/kisage/model"
)

// ProviderKind identifies the target resource provider implementation (ADR 0002 §1, ADR 0004 §5).
type ProviderKind string

const (
	// ProviderLKALFile is the single-file LKAL provider on a local filesystem.
	ProviderLKALFile ProviderKind = "lkal-file"
)

// ValidateProviderKind checks that kind is a non-empty valid provider identifier.
func ValidateProviderKind(kind ProviderKind) error {
	return validateIdentifier("provider kind", string(kind))
}

// PhysicalIdentity captures the physical filesystem identity of a target file (ADR 0002 §1, Counterexample #33).
type PhysicalIdentity struct {
	DeviceID  uint64
	Inode     uint64
	LinkCount uint64
}

// Validate checks that id has non-zero DeviceID, Inode, and LinkCount.
func (id PhysicalIdentity) Validate() error {
	if id.DeviceID == 0 || id.Inode == 0 {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "physical file identity requires non-zero DeviceID and Inode",
		}
	}
	if id.LinkCount == 0 {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "physical file identity requires LinkCount >= 1",
		}
	}
	return nil
}

// SamePhysicalFile reports whether id and other refer to the same physical inode on the same device.
func (id PhysicalIdentity) SamePhysicalFile(other PhysicalIdentity) bool {
	return id.DeviceID != 0 && id.Inode != 0 && id.DeviceID == other.DeviceID && id.Inode == other.Inode
}

// ResourceBinding binds a ResourceID to its ProviderKind, fixed ManagementRoot, TargetPath,
// shared ownership StoreID, and registration-time physical identity (ADR 0002 §1,
// Counterexample #33). The inode detects aliases at registration; the protected
// pathname and device bind the resource across atomic replacements (ADR 0004 §5.1).
type ResourceBinding struct {
	ID             ResourceID
	Provider       ProviderKind
	ManagementRoot string
	TargetPath     string
	StoreID        StoreID
	Physical       PhysicalIdentity
}

// ValidateResourceBinding validates a ResourceBinding against ADR 0002 §1 invariants:
//   - Valid ID, Provider, StoreID, and PhysicalIdentity.
//   - ManagementRoot and TargetPath are clean, absolute paths with TargetPath inside ManagementRoot.
//   - Single-file providers reject hard links (LinkCount > 1) with K101.
func ValidateResourceBinding(b ResourceBinding) error {
	if err := ValidateResourceID(b.ID); err != nil {
		return err
	}
	if err := ValidateProviderKind(b.Provider); err != nil {
		return err
	}
	if err := ValidateStoreID(b.StoreID); err != nil {
		return err
	}
	if err := validateRootAndTargetPaths(b.ManagementRoot, b.TargetPath); err != nil {
		return err
	}
	if err := b.Physical.Validate(); err != nil {
		return err
	}
	if b.Physical.LinkCount > 1 {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("resource %q target %q has hard link count %d; hard links are rejected", b.ID, b.TargetPath, b.Physical.LinkCount),
		}
	}
	return nil
}

func validateRootAndTargetPaths(root, target string) error {
	if root == "" || target == "" {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "management root and target path must not be empty",
		}
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(target) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "management root and target path must be absolute paths",
		}
	}
	if filepath.Clean(root) != root || filepath.Clean(target) != target {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "management root and target path must be canonical clean paths",
		}
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target path %q must reside strictly inside management root %q", target, root),
		}
	}
	return nil
}

// ResourceRegistry is an immutable registry of validated ResourceBindings that enforces
// physical target uniqueness across ResourceIDs, path aliases, and ownership stores (ADR 0002 §1, Counterexample #33).
type ResourceRegistry struct {
	bindings []ResourceBinding
}

// NewResourceRegistry constructs a validated ResourceRegistry from zero or more bindings.
func NewResourceRegistry(bindings ...ResourceBinding) (*ResourceRegistry, error) {
	reg := &ResourceRegistry{}
	var err error
	for _, b := range bindings {
		reg, err = reg.Register(b)
		if err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// Register returns a new ResourceRegistry with b added, or an error if b is invalid or
// duplicates an existing ResourceID, target path, or physical file (DeviceID, Inode).
func (r *ResourceRegistry) Register(b ResourceBinding) (*ResourceRegistry, error) {
	if err := ValidateResourceBinding(b); err != nil {
		return nil, err
	}
	var existing []ResourceBinding
	if r != nil {
		existing = r.bindings
	}
	for _, cur := range existing {
		if err := checkBindingCollision(cur, b); err != nil {
			return nil, err
		}
	}
	next := make([]ResourceBinding, len(existing)+1)
	copy(next, existing)
	next[len(existing)] = b
	slices.SortFunc(next, func(a, c ResourceBinding) int {
		if a.ID < c.ID {
			return -1
		}
		if a.ID > c.ID {
			return 1
		}
		return 0
	})
	return &ResourceRegistry{bindings: next}, nil
}

// Get looks up a ResourceBinding by ResourceID.
func (r *ResourceRegistry) Get(id ResourceID) (ResourceBinding, bool) {
	if r == nil {
		return ResourceBinding{}, false
	}
	for _, b := range r.bindings {
		if b.ID == id {
			return b, true
		}
	}
	return ResourceBinding{}, false
}

// Bindings returns a defensive copy of all registered bindings in ResourceID order.
func (r *ResourceRegistry) Bindings() []ResourceBinding {
	if r == nil || len(r.bindings) == 0 {
		return nil
	}
	return slices.Clone(r.bindings)
}

func checkBindingCollision(existing, candidate ResourceBinding) error {
	if existing.ID == candidate.ID {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("duplicate registration for resource ID %q", candidate.ID),
		}
	}
	if existing.TargetPath == candidate.TargetPath {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("target path %q is already bound to resource %q (store %q)", candidate.TargetPath, existing.ID, existing.StoreID),
		}
	}
	if existing.Physical.SamePhysicalFile(candidate.Physical) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("physical target (dev=%d, ino=%d) for resource %q aliases existing resource %q (store %q)", candidate.Physical.DeviceID, candidate.Physical.Inode, candidate.ID, existing.ID, existing.StoreID),
		}
	}
	return nil
}
