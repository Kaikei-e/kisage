package projection

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
)

// LeafEntry pairs a projected leaf Path with its validated, canonical LKAL leaf Value.
type LeafEntry struct {
	Path  model.Path
	Value lkal.Value
}

// Declaration holds the validated leaf-path projection D = Paths(d) of an LKAL declaration document.
type Declaration struct {
	entries []LeafEntry
	byKey   map[string]int
}

// ParseDeclaration parses an LKAL source document and projects it into leaf paths D = Paths(d).
// Non-root empty Records in the declaration are rejected with K102 (EmptyRecordNotSupported),
// whereas an empty root document is permitted as D = ∅ (ADR 0001 §1–2).
func ParseDeclaration(name string, src []byte) (*Declaration, error) {
	f, err := lkal.Parse(name, src)
	if err != nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: err.Error(),
			Cause:  err,
		}
	}
	return ProjectFile(f)
}

// ProjectFile projects a parsed *lkal.File into leaf paths D = Paths(d).
func ProjectFile(f *lkal.File) (*Declaration, error) {
	if f == nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "nil LKAL file",
		}
	}
	return ProjectRecord(f.Data())
}

// ProjectRecord validates a programmatic or parsed lkal.Record against LKAL document validity
// rules and projects it into leaf paths D = Paths(d) (ADR 0001 §1–2).
func ProjectRecord(rec lkal.Record) (*Declaration, error) {
	entries, err := Paths(rec)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]int, len(entries))
	for i, e := range entries {
		byKey[e.Path.String()] = i
	}
	return &Declaration{
		entries: entries,
		byKey:   byKey,
	}, nil
}

// Paths computes the sorted leaf-path projection D = Paths(d) for a declaration Record d.
// Non-root empty Records are rejected with K102; an empty root Record yields an empty slice (D = ∅).
func Paths(rec lkal.Record) ([]LeafEntry, error) {
	entries, err := projectRecord(rec, nil, 0)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b LeafEntry) int {
		return a.Path.Compare(b.Path)
	})
	return entries, nil
}

// Len returns the number of projected leaf paths in d.
func (d *Declaration) Len() int {
	if d == nil {
		return 0
	}
	return len(d.entries)
}

// IsEmpty reports whether D = ∅ (an empty root declaration).
func (d *Declaration) IsEmpty() bool {
	return d.Len() == 0
}

// Paths returns the sorted leaf paths D = Paths(d).
func (d *Declaration) Paths() []model.Path {
	if d == nil || len(d.entries) == 0 {
		return nil
	}
	out := make([]model.Path, len(d.entries))
	for i, e := range d.entries {
		out[i] = e.Path.Clone()
	}
	return out
}

// Entries returns a defensive copy of the sorted (Path, Value) leaf entries in D = Paths(d).
func (d *Declaration) Entries() []LeafEntry {
	if d == nil || len(d.entries) == 0 {
		return nil
	}
	out := make([]LeafEntry, len(d.entries))
	for i, e := range d.entries {
		out[i] = LeafEntry{
			Path:  e.Path.Clone(),
			Value: model.CloneLeafValue(e.Value),
		}
	}
	return out
}

// Has reports whether p is in D = Paths(d).
func (d *Declaration) Has(p model.Path) bool {
	if d == nil || len(p) == 0 {
		return false
	}
	_, ok := d.byKey[p.String()]
	return ok
}

// Get returns the declared leaf value d(p) if p ∈ D.
func (d *Declaration) Get(p model.Path) (lkal.Value, bool) {
	if d == nil || len(p) == 0 {
		return nil, false
	}
	idx, ok := d.byKey[p.String()]
	if !ok {
		return nil, false
	}
	return model.CloneLeafValue(d.entries[idx].Value), true
}

func projectRecord(rec lkal.Record, prefix model.Path, depth int) ([]LeafEntry, error) {
	if err := checkRecordDepthAndEmpty(rec, prefix, depth); err != nil {
		return nil, err
	}
	if err := validateRecordFields(rec.Fields, prefix); err != nil {
		return nil, err
	}
	var out []LeafEntry
	for _, field := range rec.Fields {
		entries, err := projectField(field, prefix.Append(field.Key), depth)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

func checkRecordDepthAndEmpty(rec lkal.Record, prefix model.Path, depth int) error {
	if depth > model.MaxLKALBlockDepth {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   prefix.Clone(),
			Reason: "nest blocks at most 1000 levels deep",
		}
	}
	if depth > 0 && len(rec.Fields) == 0 {
		return model.Diagnostic{
			Code:   model.CodeEmptyRecordNotSupported,
			Path:   prefix.Clone(),
			Reason: "non-root empty Record is not supported in declarations",
		}
	}
	return nil
}

func validateRecordFields(fields []lkal.Field, prefix model.Path) error {
	seenKeys := make(map[string]bool, len(fields))
	for _, field := range fields {
		if !utf8.ValidString(field.Key) {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   prefix.Clone(),
				Reason: "record key contains invalid UTF-8",
			}
		}
		if strings.HasPrefix(field.Key, "$") {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   prefix.Append(field.Key),
				Reason: fmt.Sprintf("key %q must not start with $", field.Key),
			}
		}
		if seenKeys[field.Key] {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   prefix.Append(field.Key),
				Reason: fmt.Sprintf("duplicate key %q in Record", field.Key),
			}
		}
		seenKeys[field.Key] = true
	}
	return nil
}

func projectField(field lkal.Field, childPath model.Path, depth int) ([]LeafEntry, error) {
	if field.Value == nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   childPath,
			Reason: "value must not be nil",
		}
	}
	if subRec, ok := field.Value.(lkal.Record); ok {
		return projectRecord(subRec, childPath, depth+1)
	}
	normVal, reason := model.ValidateAndNormalizeLeaf(field.Value)
	if reason != "" {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   childPath,
			Reason: reason,
		}
	}
	return []LeafEntry{{Path: childPath, Value: normVal}}, nil
}
