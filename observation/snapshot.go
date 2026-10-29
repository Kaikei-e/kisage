package observation

import (
	"fmt"
	"slices"
	"unicode/utf8"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
)

type snapshotEntry struct {
	path     model.Path
	observed Observed
}

// Snapshot stores path observations o(p) for a target resource.
// Any path not explicitly recorded in Snapshot returns Unknown when queried via Get,
// enforcing ADR 0001 §3 (missing observations must never default to Absent).
type Snapshot struct {
	byKey map[string]snapshotEntry
}

// NewSnapshot creates an empty observation Snapshot.
func NewSnapshot() *Snapshot {
	return &Snapshot{
		byKey: make(map[string]snapshotEntry),
	}
}

// Set records an observation for path p in the Snapshot.
// Root or empty paths are ignored.
func (s *Snapshot) Set(p model.Path, obs Observed) {
	if s == nil || len(p) == 0 {
		return
	}
	if s.byKey == nil {
		s.byKey = make(map[string]snapshotEntry)
	}
	s.byKey[p.String()] = snapshotEntry{
		path:     p.Clone(),
		observed: normalizeObserved(obs),
	}
}

// Has reports whether an explicit observation entry exists for p.
func (s *Snapshot) Has(p model.Path) bool {
	if s == nil || len(p) == 0 {
		return false
	}
	_, ok := s.byKey[p.String()]
	return ok
}

// Get returns the recorded observation o(p) for path p.
// If p is not recorded in the Snapshot, Get returns UnknownObserved rather than Absent (ADR 0001 §3).
func (s *Snapshot) Get(p model.Path) Observed {
	if len(p) == 0 {
		return UnknownObserved("root path is not a leaf or intermediate target path")
	}
	if s == nil {
		return UnknownObserved(fmt.Sprintf("missing observation for path %s (nil snapshot)", p.String()))
	}
	entry, ok := s.byKey[p.String()]
	if !ok {
		return UnknownObserved(fmt.Sprintf("missing observation for path %s", p.String()))
	}
	return entry.observed
}

// Paths returns all paths explicitly recorded in the Snapshot in canonical segment order.
func (s *Snapshot) Paths() []model.Path {
	if s == nil || len(s.byKey) == 0 {
		return nil
	}
	out := make([]model.Path, 0, len(s.byKey))
	for _, entry := range s.byKey {
		out = append(out, entry.path.Clone())
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

// ObserveFile observes a parsed *lkal.File as a target environment and populates a Snapshot
// covering all nodes present in f plus Absent observations for any requested queryPaths
// (and their proper ancestors) that do not exist in f.
//
// In accordance with ADR 0001 §2, empty Records present in the observed target environment
// are recorded as Present(Record) and are never rejected with K102.
func ObserveFile(f *lkal.File, queryPaths ...model.Path) (*Snapshot, error) {
	if f == nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "nil LKAL file",
		}
	}
	return ObserveRecord(f.Data(), queryPaths...), nil
}

// ObserveRecord observes an lkal.Record as a target environment and populates a Snapshot.
// Every existing Record (including empty Records) is recorded as Present(Record), every valid
// leaf value is recorded as Present(Leaf(v)), and any unrepresentable value is recorded as
// Present(Opaque(reason)). For each path in queryPaths and its proper ancestors, if the path
// does not exist in rec, it is explicitly recorded as Absent.
func ObserveRecord(rec lkal.Record, queryPaths ...model.Path) *Snapshot {
	snap := NewSnapshot()
	for _, entry := range collectRecordObservations(rec, nil) {
		snap.Set(entry.path, entry.observed)
	}
	markAbsentQueryPaths(snap, queryPaths)
	return snap
}

func collectRecordObservations(rec lkal.Record, prefix model.Path) []snapshotEntry {
	var out []snapshotEntry
	for _, field := range rec.Fields {
		if !utf8.ValidString(field.Key) {
			continue
		}
		childPath := prefix.Append(field.Key)
		out = append(out, observeRecordField(field.Value, childPath)...)
	}
	return out
}

func observeRecordField(val lkal.Value, path model.Path) []snapshotEntry {
	if val == nil {
		return []snapshotEntry{{path: path, observed: PresentOpaque("nil value in target record")}}
	}
	if subRec, ok := val.(lkal.Record); ok {
		// Observed empty Records in the target environment are valid Present(Record)
		// nodes and must not trigger K102 (ADR 0001 §2, Counterexamples #47 and #32).
		entries := []snapshotEntry{{path: path, observed: PresentRecord()}}
		return append(entries, collectRecordObservations(subRec, path)...)
	}
	normVal, reason := model.ValidateAndNormalizeLeaf(val)
	if reason != "" {
		return []snapshotEntry{{path: path, observed: PresentOpaque(reason)}}
	}
	return []snapshotEntry{{
		path:     path,
		observed: PresentObserved(Node{kind: NodeLeaf, leaf: normVal}),
	}}
}

func markAbsentQueryPaths(snap *Snapshot, queryPaths []model.Path) {
	for _, qp := range queryPaths {
		if len(qp) == 0 {
			continue
		}
		for _, anc := range qp.Ancestors() {
			if !snap.Has(anc) {
				snap.Set(anc, AbsentObserved())
			}
		}
		if !snap.Has(qp) {
			snap.Set(qp, AbsentObserved())
		}
	}
}
