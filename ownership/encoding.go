package ownership

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

// Store encoding preserves LKAL types and unowned revision tombstones. Values
// are LKAL literals, rather than JSON numbers or untyped interface values.
type storeDocument struct {
	Version int        `json:"version"`
	Rows    []storeRow `json:"rows"`
}
type storeRow struct {
	Resource ResourceID    `json:"resource"`
	Path     model.Path    `json:"path"`
	Revision uint64        `json:"revision"`
	Entry    *encodedEntry `json:"entry,omitempty"`
}
type encodedEntry struct {
	Manager     ActorID         `json:"manager"`
	Origin      Origin          `json:"origin"`
	LastApplied string          `json:"lastApplied,omitempty"`
	Pending     *encodedPending `json:"pending,omitempty"`
}
type encodedPending struct {
	BatchID string    `json:"batchID"`
	Op      PendingOp `json:"op"`
	// Empty Expected means confirmed Absent. All other values are leaf literals.
	Expected string        `json:"expected,omitempty"`
	Target   string        `json:"target,omitempty"`
	Previous *encodedEntry `json:"previous,omitempty"`
}

func encodeLeaf(v lkal.Value) string {
	if v == nil {
		return ""
	}
	return string(lkal.FormatValue(v))
}
func decodeLeaf(src string) (lkal.Value, error) {
	if src == "" {
		return nil, nil
	}
	v, err := lkal.ParseValue([]byte(src))
	if err != nil {
		return nil, err
	}
	return model.ValidateLeafValue(v)
}

// MarshalJSON returns a deterministic, versioned ownership snapshot.
func (s *Store) MarshalJSON() ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("cannot encode nil ownership store")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	doc := storeDocument{Version: 1}
	for _, res := range s.Resources() {
		tbl := s.byResource[res]
		keys := make([]string, 0, len(tbl.revisions))
		for key := range tbl.revisions {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			path, err := model.ParsePath(key)
			if err != nil {
				return nil, err
			}
			row := storeRow{Resource: res, Path: path, Revision: tbl.revisions[key]}
			if rec, ok := tbl.entries[key]; ok {
				e := rec.entry
				row.Entry = &encodedEntry{Manager: e.Manager, Origin: e.Origin, LastApplied: encodeLeaf(e.LastApplied)}
				if p := e.Pending; p != nil {
					exp, _ := p.Expected.LeafValue()
					row.Entry.Pending = &encodedPending{BatchID: p.BatchID, Op: p.Op, Expected: encodeLeaf(exp), Target: encodeLeaf(p.Target)}
					if p.Previous != nil {
						row.Entry.Pending.Previous = &encodedEntry{Origin: p.Previous.Origin, LastApplied: encodeLeaf(p.Previous.LastApplied)}
					}
				}
			}
			doc.Rows = append(doc.Rows, row)
		}
	}
	return json.Marshal(doc)
}

// UnmarshalJSON rejects malformed snapshots instead of reconstructing history.
// The receiver changes only after every row and invariant has been validated.
func (s *Store) UnmarshalJSON(data []byte) error {
	var doc storeDocument
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing store data")
	}
	if doc.Version != 1 {
		return fmt.Errorf("unsupported ownership encoding version %d", doc.Version)
	}
	next := NewStore()
	for _, row := range doc.Rows {
		if err := ValidateResourceID(row.Resource); err != nil {
			return err
		}
		if err := model.ValidateLeafPath(row.Path); err != nil {
			return err
		}
		if row.Revision == 0 {
			return fmt.Errorf("stored revisions must be positive")
		}
		tbl := next.ensureTable(row.Resource)
		key := row.Path.String()
		if _, exists := tbl.revisions[key]; exists {
			return fmt.Errorf("duplicate ownership row %s", key)
		}
		tbl.revisions[key] = row.Revision
		if row.Entry == nil {
			continue
		}
		w := row.Entry
		last, err := decodeLeaf(w.LastApplied)
		if err != nil {
			return err
		}
		e := OwnerEntry{Manager: w.Manager, Origin: w.Origin, LastApplied: last, Revision: row.Revision}
		if w.Pending != nil {
			p := w.Pending
			expected, err := decodeLeaf(p.Expected)
			if err != nil {
				return err
			}
			target, err := decodeLeaf(p.Target)
			if err != nil {
				return err
			}
			e.Pending = &Pending{BatchID: p.BatchID, Op: p.Op, Target: target, Expected: observation.AbsentObserved()}
			if expected != nil {
				e.Pending.Expected = observation.MustPresentLeaf(expected)
			}
			if p.Previous != nil {
				if p.Previous.Manager != "" || p.Previous.Pending != nil {
					return fmt.Errorf("invalid previous entry encoding")
				}
				prev, err := decodeLeaf(p.Previous.LastApplied)
				if err != nil {
					return err
				}
				e.Pending.Previous = &SettledState{Origin: p.Previous.Origin, LastApplied: prev}
			}
		}
		e, err = e.Normalize()
		if err != nil {
			return err
		}
		tbl.entries[key] = storedRecord{path: row.Path.Clone(), entry: e}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*s = *next
	return nil
}
