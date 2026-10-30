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

// Clone returns a deep defensive copy of s.
func (s *Snapshot) Clone() *Snapshot {
	if s == nil {
		return nil
	}
	out := NewSnapshot()
	for k, entry := range s.byKey {
		out.byKey[k] = snapshotEntry{
			path:     entry.path.Clone(),
			observed: normalizeObserved(entry.observed),
		}
	}
	return out
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
		// If a duplicate key appears in the same record, purge any previously collected
		// entries at or below childPath so a Leaf/Opaque overwrite cannot leave stale
		// descendant entries in the Snapshot.
		out = purgePathAndDescendants(out, childPath)
		out = append(out, observeRecordField(field.Value, childPath)...)
	}
	return out
}

func purgePathAndDescendants(entries []snapshotEntry, p model.Path) []snapshotEntry {
	if len(entries) == 0 {
		return entries
	}
	filtered := entries[:0]
	for _, e := range entries {
		if e.path.Equal(p) || p.IsProperAncestorOf(e.path) {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

func observeRecordField(val lkal.Value, path model.Path) []snapshotEntry {
	if len(path)-1 > model.MaxLKALBlockDepth {
		return []snapshotEntry{{
			path:     path,
			observed: PresentObserved(OpaqueNodeWithWitness("nest blocks at most 1000 levels deep", "depth-exceeded")),
		}}
	}
	if val == nil {
		return []snapshotEntry{{
			path:     path,
			observed: PresentObserved(OpaqueNodeWithWitness("nil value in target record", "nil")),
		}}
	}
	if subRec, ok := val.(lkal.Record); ok {
		// Observed empty Records in the target environment are valid Present(Record)
		// nodes and must not trigger K102 (ADR 0001 §2, Counterexamples #47 and #32).
		entries := []snapshotEntry{{path: path, observed: PresentRecord()}}
		return append(entries, collectRecordObservations(subRec, path)...)
	}
	normVal, reason := model.ValidateAndNormalizeLeaf(val)
	if reason != "" {
		return []snapshotEntry{{
			path:     path,
			observed: PresentObserved(OpaqueNodeWithWitness(reason, buildOpaqueValueWitness(val, 0))),
		}}
	}
	return []snapshotEntry{{
		path:     path,
		observed: PresentObserved(Node{kind: NodeLeaf, leaf: normVal}),
	}}
}

func buildOpaqueValueWitness(val lkal.Value, depth int) string {
	if depth > 32 {
		return "<max-opaque-depth>"
	}
	if val == nil {
		return "nil"
	}
	switch v := val.(type) {
	case lkal.List:
		out := "List["
		for i, el := range v {
			if i > 0 {
				out += ","
			}
			out += buildOpaqueValueWitness(el, depth+1)
		}
		return out + "]"
	case lkal.Record:
		out := "Record{"
		for i, f := range v.Fields {
			if i > 0 {
				out += ","
			}
			out += fmt.Sprintf("%q:%s", f.Key, buildOpaqueValueWitness(f.Value, depth+1))
		}
		return out + "}"
	default:
		return fmt.Sprintf("%T(%#v)", val, val)
	}
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
