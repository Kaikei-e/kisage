package ownership

import (
	"fmt"
	"math"
	"slices"

	"github.com/Kaikei-e/kisage/model"
)

type storedRecord struct {
	path  model.Path
	entry OwnerEntry
}

type resourceTable struct {
	entries   map[string]storedRecord
	revisions map[string]uint64
}

// Store represents the serialized shared ownership store σ across all Actors (ADR 0002 §1–2).
//
// Invariants enforced by Store:
//   - Keys are (ResourceID, PathSegments).
//   - Every stored OwnerEntry is structurally valid (Origin=None only when Pending != nil, etc.).
//   - Single-owner exclusivity: within any ResourceID, no two entries may exist at overlapping
//     paths (same path or proper ancestor/descendant), including unsettled Pending reservations.
//   - Every path (including unowned and previously released paths) has a monotonic uint64 Revision
//     so stale --owner-revision tokens are rejected even after unowned/owned/unowned cycles (Counterexample #38).
type Store struct {
	byResource map[ResourceID]*resourceTable
}

// NewStore creates an empty shared ownership Store σ.
func NewStore() *Store {
	return &Store{
		byResource: make(map[ResourceID]*resourceTable),
	}
}

// Clone returns a deep defensive copy of s.
func (s *Store) Clone() *Store {
	out := NewStore()
	if s == nil {
		return out
	}
	for resID, tbl := range s.byResource {
		newTbl := &resourceTable{
			entries:   make(map[string]storedRecord, len(tbl.entries)),
			revisions: make(map[string]uint64, len(tbl.revisions)),
		}
		for k, rec := range tbl.entries {
			newTbl.entries[k] = storedRecord{
				path:  rec.path.Clone(),
				entry: rec.entry.Clone(),
			}
		}
		for k, rev := range tbl.revisions {
			newTbl.revisions[k] = rev
		}
		out.byResource[resID] = newTbl
	}
	return out
}

// Get returns the OwnerEntry for (resID, p) if present in σ.
func (s *Store) Get(resID ResourceID, p model.Path) (OwnerEntry, bool) {
	if s == nil || len(p) == 0 {
		return OwnerEntry{}, false
	}
	tbl, ok := s.byResource[resID]
	if !ok {
		return OwnerEntry{}, false
	}
	rec, ok := tbl.entries[p.String()]
	if !ok {
		return OwnerEntry{}, false
	}
	return rec.entry.Clone(), true
}

// Has reports whether an active ownership entry (settled or Pending) exists at (resID, p).
func (s *Store) Has(resID ResourceID, p model.Path) bool {
	_, ok := s.Get(resID, p)
	return ok
}

// Revision returns the current ownership revision for (resID, p).
// Unowned paths that have never been modified have revision 0; paths that were modified or
// released retain their monotonically incremented revision (ADR 0003 §3, Counterexample #38).
func (s *Store) Revision(resID ResourceID, p model.Path) uint64 {
	if s == nil || len(p) == 0 {
		return 0
	}
	tbl, ok := s.byResource[resID]
	if !ok {
		return 0
	}
	return tbl.revisions[p.String()]
}

// VerifyRevision checks that the current ownership revision at (resID, p) equals expectedRev.
func (s *Store) VerifyRevision(resID ResourceID, p model.Path, expectedRev uint64) error {
	if err := ValidateResourceID(resID); err != nil {
		return err
	}
	if err := model.ValidateLeafPath(p); err != nil {
		return err
	}
	actual := s.Revision(resID, p)
	if actual != expectedRev {
		return model.Diagnostic{
			Code:       model.CodeOwnershipConflict,
			Path:       p.Clone(),
			Reason:     fmt.Sprintf("owner revision mismatch for %s: expected %d, current is %d", p.String(), expectedRev, actual),
			NextAction: "re-run explain to obtain the current --owner-revision before retrying",
		}
	}
	return nil
}

// Put returns a new Store with entry recorded at (resID, p) after enforcing all ADR 0002 invariants:
//   - Valid ResourceID, leaf Path, and OwnerEntry (stored with canonical LKAL values).
//   - No proper ancestor or descendant path q != p in resID already has an ownership entry.
//   - If an entry already exists at p for a different Manager, Put rejects the overwrite with K201
//     (cross-Actor replacement requires explicit release first, ADR 0002 §1, Counterexample #30).
//   - Automatically advances the path revision to at least currentRev + 1.
//     Rejects changes once currentRev reaches uint64's maximum; never wraps or reuses a revision.
func (s *Store) Put(resID ResourceID, p model.Path, entry OwnerEntry) (*Store, error) {
	if err := ValidateResourceID(resID); err != nil {
		return nil, err
	}
	if err := model.ValidateLeafPath(p); err != nil {
		return nil, err
	}
	entry, err := entry.Normalize()
	if err != nil {
		return nil, attachPath(err, p)
	}
	if err := s.checkPutExclusivity(resID, p, entry.Manager); err != nil {
		return nil, err
	}

	nextRev, err := incrementRevision(s.Revision(resID, p), p)
	if err != nil {
		return nil, err
	}
	if entry.Revision > nextRev {
		nextRev = entry.Revision
	}
	if entry.HasPending() && nextRev == math.MaxUint64 {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   p.Clone(),
			Reason: "ownership revision exhausted: pending entry requires headroom for settlement",
		}
	}
	next := s.Clone()
	tbl := next.ensureTable(resID)
	key := p.String()
	entry.Revision = nextRev
	tbl.entries[key] = storedRecord{
		path:  p.Clone(),
		entry: entry,
	}
	tbl.revisions[key] = nextRev
	return next, nil
}

// Delete returns a new Store with any ownership entry at (resID, p) removed and the path's
// revision incremented by 1. If no entry exists at (resID, p), Delete returns a clone unchanged.
// A present entry at uint64's maximum revision is retained and an error is returned.
func (s *Store) Delete(resID ResourceID, p model.Path) (*Store, error) {
	if err := ValidateResourceID(resID); err != nil {
		return nil, err
	}
	if err := model.ValidateLeafPath(p); err != nil {
		return nil, err
	}
	next := s.Clone()
	tbl, ok := next.byResource[resID]
	if !ok {
		return next, nil
	}
	key := p.String()
	if _, exists := tbl.entries[key]; !exists {
		return next, nil
	}
	nextRev, err := incrementRevision(tbl.revisions[key], p)
	if err != nil {
		return nil, err
	}
	delete(tbl.entries, key)
	tbl.revisions[key] = nextRev
	return next, nil
}

func incrementRevision(current uint64, p model.Path) (uint64, error) {
	if current == math.MaxUint64 {
		return 0, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   p.Clone(),
			Reason: "ownership revision exhausted: cannot advance beyond uint64 maximum",
		}
	}
	return current + 1, nil
}

// Paths returns all owned (settled or Pending) paths in resID in canonical segment order.
func (s *Store) Paths(resID ResourceID) []model.Path {
	if s == nil {
		return nil
	}
	tbl, ok := s.byResource[resID]
	if !ok || len(tbl.entries) == 0 {
		return nil
	}
	out := make([]model.Path, 0, len(tbl.entries))
	for _, rec := range tbl.entries {
		out = append(out, rec.path.Clone())
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

// Resources returns all ResourceIDs present in s in sorted order.
func (s *Store) Resources() []ResourceID {
	if s == nil || len(s.byResource) == 0 {
		return nil
	}
	out := make([]ResourceID, 0, len(s.byResource))
	for resID := range s.byResource {
		out = append(out, resID)
	}
	slices.Sort(out)
	return out
}

// Validate checks the structural and exclusivity invariants of all records in s (ADR 0002 §1–2, ADR 0003 §1).
func (s *Store) Validate() error {
	if s == nil {
		return nil
	}
	for _, resID := range s.Resources() {
		if err := ValidateResourceID(resID); err != nil {
			return err
		}
		if err := s.validateResourceTable(resID, s.byResource[resID]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) validateResourceTable(resID ResourceID, tbl *resourceTable) error {
	for key, rev := range tbl.revisions {
		p, err := model.ParsePath(key)
		if err != nil {
			return err
		}
		if err := model.ValidateLeafPath(p); err != nil {
			return err
		}
		if rev == 0 {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   p.Clone(),
				Reason: "stored ownership revision must be positive",
			}
		}
	}
	paths := s.Paths(resID)
	for i, p := range paths {
		if err := model.ValidateLeafPath(p); err != nil {
			return err
		}
		key := p.String()
		rec := tbl.entries[key]
		if rec.entry.Revision == 0 || tbl.revisions[key] != rec.entry.Revision {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   p.Clone(),
				Reason: fmt.Sprintf("corrupted store in resource %q: entry revision %d does not match table revision %d", resID, rec.entry.Revision, tbl.revisions[key]),
			}
		}
		if err := rec.entry.Validate(); err != nil {
			return attachPath(err, p)
		}
		for j := i + 1; j < len(paths); j++ {
			q := paths[j]
			if p.Overlaps(q) {
				other := tbl.entries[q.String()]
				return model.Diagnostic{
					Code:   model.CodeOwnershipConflict,
					Path:   p.Clone(),
					Reason: fmt.Sprintf("corrupted store in resource %q: overlapping entries %s (owner %q) and %s (owner %q)", resID, p.String(), rec.entry.Manager, q.String(), other.entry.Manager),
				}
			}
		}
	}
	return nil
}

func (s *Store) checkPutExclusivity(resID ResourceID, p model.Path, manager ActorID) error {
	for _, conflict := range FindOverlaps(s, resID, p) {
		if conflict.Relation == OverlapSamePath {
			if conflict.ExistingEntry.Manager != manager {
				return conflict.ToDiagnostic()
			}
			continue
		}
		return conflict.ToDiagnostic()
	}
	return nil
}

func (s *Store) ensureTable(resID ResourceID) *resourceTable {
	tbl, ok := s.byResource[resID]
	if !ok {
		tbl = &resourceTable{
			entries:   make(map[string]storedRecord),
			revisions: make(map[string]uint64),
		}
		s.byResource[resID] = tbl
	}
	return tbl
}

func attachPath(err error, p model.Path) error {
	if d, ok := err.(model.Diagnostic); ok {
		return d.WithPath(p)
	}
	return err
}
