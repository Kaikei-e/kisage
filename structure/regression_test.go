package structure_test

import (
	"bytes"
	"strings"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/structure"
)

// Counterexample #1 also requires preservation of Opaque siblings (ADR 0001).
// In particular, equal diagnostic reasons do not imply equal opaque contents.
func TestCounterexample01_OpaqueSiblingPreservation(t *testing.T) {
	opaque := lkal.List{lkal.Int(1), lkal.String("two")}
	before := lkal.Record{Fields: []lkal.Field{
		{Key: "a", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "target", Value: lkal.Int(1)},
			{Key: "opaque", Value: opaque},
		}}},
	}}
	p := model.MustParsePath("/a/target")
	for _, tc := range []struct {
		name    string
		value   lkal.Value
		present bool
		wantErr bool
	}{
		{"unchanged", lkal.List{lkal.Int(1), lkal.String("two")}, true, false},
		{"deleted", nil, false, true},
		{"changed with same opaque reason", lkal.List{lkal.Int(999), lkal.String("other")}, true, true},
		{"changed opaque kind", nil, true, true},
		{"replaced by leaf", lkal.Int(2), true, true},
		{"replaced by empty Record", lkal.Record{}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := []lkal.Field{{Key: "target", Value: lkal.Int(2)}}
			if tc.present {
				fields = append(fields, lkal.Field{Key: "opaque", Value: tc.value})
			}
			after := lkal.Record{Fields: []lkal.Field{{Key: "a", Value: lkal.Record{Fields: fields}}}}
			err := structure.VerifyFrameCondition(before, after, p)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unchanged Opaque sibling rejected: %v", err)
				}
				return
			}
			diag := assertDiagnosticCode(t, err, model.CodeStructureConflict)
			if !diag.Path.Equal(model.MustParsePath("/a/opaque")) {
				t.Fatalf("expected violation at /a/opaque, got %s", diag.Path)
			}
		})
	}

	t.Run("unexpected opaque sibling", func(t *testing.T) {
		empty := lkal.Record{Fields: []lkal.Field{{Key: "target", Value: lkal.Int(1)}}}
		after := lkal.Record{Fields: []lkal.Field{
			{Key: "target", Value: lkal.Int(2)}, {Key: "opaque", Value: opaque},
		}}
		assertDiagnosticCode(t, structure.VerifyFrameCondition(empty, after, model.MustParsePath("/target")), model.CodeStructureConflict)
	})

	t.Run("unchanged nil opaque sibling", func(t *testing.T) {
		nilValue := lkal.Record{Fields: []lkal.Field{{Key: "opaque", Value: nil}}}
		if err := structure.VerifyFrameCondition(nilValue, nilValue); err != nil {
			t.Fatalf("unchanged nil Opaque sibling rejected: %v", err)
		}
	})

	t.Run("unchanged unsupported Go value", func(t *testing.T) {
		var unsupported *lkal.String
		rec := lkal.Record{Fields: []lkal.Field{{Key: "opaque", Value: unsupported}}}
		if err := structure.VerifyFrameCondition(rec, rec); err != nil {
			t.Fatalf("unchanged unsupported Opaque sibling rejected: %v", err)
		}
	})
}

func TestInvalidSetPathDoesNotMutateFile(t *testing.T) {
	tooDeep := make(model.Path, 1002)
	for i := range tooDeep {
		tooDeep[i] = "a"
	}
	for _, tc := range []struct {
		name string
		path model.Path
	}{
		{"reserved target", model.MustParsePath(`/new/"$reserved"`)},
		{"reserved ancestor", model.MustParsePath(`/new/"$reserved"/leaf`)},
		{"invalid UTF-8", model.Path{"new", string([]byte{0xff})}},
		{"excessive depth", tooDeep},
		{"root", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\n### keep this comment\nkeep = 1\n"))
			if err != nil {
				t.Fatal(err)
			}
			before := f.Format()
			snap, err := observation.ObserveFile(f, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnosticCode(t, structure.ValidateSet(snap, tc.path, lkal.Int(2)), model.CodeInvalidInput)
			assertDiagnosticCode(t, structure.ApplyLeafSet(f, tc.path, lkal.Int(2)), model.CodeInvalidInput)
			if !bytes.Equal(before, f.Format()) {
				t.Fatal("rejected Set changed the file")
			}
			assertDiagnosticCode(t, structure.ValidateDelete(snap, tc.path), model.CodeInvalidInput)
			assertDiagnosticCode(t, structure.ApplyLeafDelete(f, tc.path), model.CodeInvalidInput)
			if !bytes.Equal(before, f.Format()) {
				t.Fatal("rejected Delete changed the file")
			}
		})
	}
}

// Counterexample #1: both leaf creation and deletion must preserve siblings,
// including when shared ancestors are represented by LKAL label compression.
func TestCounterexample01_LabelCompressedSiblingPreservation(t *testing.T) {
	f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\n### keep the sibling comment\na \"branch\" \"keep\" = 7\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := model.MustParsePath("/a/branch/new/deep/leaf")
	if err := structure.ApplyLeafSet(f, p, lkal.Int(2)); err != nil {
		t.Fatal(err)
	}
	got, err := f.Get(p.LKALPath())
	if err != nil || !lkal.Equal(got, lkal.Int(2)) {
		t.Fatalf("created leaf = %v, err = %v", got, err)
	}
	if err := structure.ApplyLeafDelete(f, p); err != nil {
		t.Fatal(err)
	}
	got, err = f.Get("a/branch/keep")
	if err != nil || !lkal.Equal(got, lkal.Int(7)) {
		t.Fatalf("sibling was lost or changed: %v, err = %v", got, err)
	}
	if !strings.Contains(string(f.Format()), "### keep the sibling comment") {
		t.Fatal("sibling comment was lost")
	}
	snap, err := observation.ObserveFile(f, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, ancestor := range p.Ancestors() {
		if !snap.Get(ancestor).IsRecord() {
			t.Fatalf("ancestor %s was not preserved as Record", ancestor)
		}
	}
}

func TestApplyLeafSetAtLKALDepthLimit(t *testing.T) {
	f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := make(model.Path, 1001) // 1000 intermediate Records and one leaf.
	for i := range p {
		p[i] = "a"
	}
	if err := structure.ApplyLeafSet(f, p, lkal.Int(1)); err != nil {
		t.Fatalf("valid leaf at depth limit rejected: %v", err)
	}
	reparsed, err := lkal.Parse("roundtrip.lkal", f.Format())
	if err != nil {
		t.Fatalf("result cannot be parsed by LKAL: %v", err)
	}
	got, err := reparsed.Get(p.LKALPath())
	if err != nil || !lkal.Equal(got, lkal.Int(1)) {
		t.Fatalf("leaf at depth limit = %v, err = %v", got, err)
	}
}
