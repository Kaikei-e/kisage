package structure_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/structure"
)

// TestCounterexample47_SetUnderObservedEmptyRecord verifies setting a leaf inside an observed empty Record (ADR 0001 §2).
func TestCounterexample47_SetUnderObservedEmptyRecord(t *testing.T) {
	f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\na {}\n"))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	pAB := model.MustParsePath("/a/b")
	if err := structure.ApplyLeafSet(f, pAB, lkal.Int(42)); err != nil {
		t.Fatalf("expected setting /a/b under observed empty Record /a to succeed, got %v", err)
	}
	got, err := f.Get("a/b")
	if err != nil || !lkal.Equal(got, lkal.Int(42)) {
		t.Fatalf("expected /a/b = 42, got %v (err=%v)", got, err)
	}
}

// TestCounterexample32_DeleteLastLeafPreservesEmptyIntermediateRecord verifies Counterexample #32 (ADR 0001 §2, §4):
// Deleting the last leaf inside an intermediate Record leaves the intermediate Record as an empty Present(Record)
// and never auto-cleans ancestors, nor triggers K102 on subsequent target observation.
func TestCounterexample32_DeleteLastLeafPreservesEmptyIntermediateRecord(t *testing.T) {
	testCases := []struct {
		name string
		src  string
	}{
		{
			name: "nested block syntax",
			src:  "@lkal \"0.1\"\na {\n  b {\n    c = 1\n  }\n}\n",
		},
		{
			name: "label compressed syntax",
			src:  "@lkal \"0.1\"\na \"b\" \"c\" = 1\n",
		},
		{
			name: "labeled block syntax",
			src:  "@lkal \"0.1\"\na \"b\" {\n  c = 1\n}\n",
		},
	}

	pA := model.MustParsePath("/a")
	pAB := model.MustParsePath("/a/b")
	pABC := model.MustParsePath("/a/b/c")

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := lkal.Parse("target.lkal", []byte(tc.src))
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}

			if err := structure.ApplyLeafDelete(f, pABC); err != nil {
				t.Fatalf("ApplyLeafDelete(/a/b/c) failed: %v", err)
			}

			snap, err := observation.ObserveFile(f, pABC)
			if err != nil {
				t.Fatalf("ObserveFile after delete failed: %v", err)
			}

			if !snap.Get(pABC).IsAbsent() {
				t.Fatalf("expected /a/b/c to be Absent after delete, got %s", snap.Get(pABC))
			}
			if !snap.Get(pAB).IsRecord() {
				t.Fatalf("expected intermediate /a/b to remain Present(Record), got %s", snap.Get(pAB))
			}
			if !snap.Get(pA).IsRecord() {
				t.Fatalf("expected intermediate /a to remain Present(Record), got %s", snap.Get(pA))
			}
		})
	}
}

func TestCounterexample01_ImplicitIntermediateRecordCreationAndFrameCondition(t *testing.T) {
	f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\nexisting {\n  keep = \"untouched\"\n}\n"))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	pNew := model.MustParsePath("/a/b/c")
	if err := structure.ApplyLeafSet(f, pNew, lkal.Int(7)); err != nil {
		t.Fatalf("ApplyLeafSet(/a/b/c, 7) failed: %v", err)
	}

	snap, err := observation.ObserveFile(f)
	if err != nil {
		t.Fatalf("ObserveFile failed: %v", err)
	}

	if !snap.Get(model.MustParsePath("/a")).IsRecord() {
		t.Fatalf("expected /a to be Present(Record)")
	}
	if !snap.Get(model.MustParsePath("/a/b")).IsRecord() {
		t.Fatalf("expected /a/b to be Present(Record)")
	}
	vNew, ok := snap.Get(pNew).LeafValue()
	if !ok || !model.EqualLeafValues(vNew, lkal.Int(7)) {
		t.Fatalf("expected /a/b/c = 7, got %v", vNew)
	}

	vKeep, ok := snap.Get(model.MustParsePath("/existing/keep")).LeafValue()
	if !ok || !model.EqualLeafValues(vKeep, lkal.String("untouched")) {
		t.Fatalf("expected /existing/keep to be preserved, got %v", vKeep)
	}
}

func TestApplyLeafSetCreatesBlockSyntaxForNewSection(t *testing.T) {
	f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\n\nlocal {\n  KRATOS_COOKIE_DOMAIN = \"\"\n}\n"))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if err := structure.ApplyLeafSet(f, model.MustParsePath("/feature_flags/AUTH_SHARED_SESSION_CACHE"), lkal.Bool(false)); err != nil {
		t.Fatalf("ApplyLeafSet 1 failed: %v", err)
	}
	if err := structure.ApplyLeafSet(f, model.MustParsePath("/feature_flags/KNOWLEDGE_HOME_ENABLE_LENS"), lkal.Bool(true)); err != nil {
		t.Fatalf("ApplyLeafSet 2 failed: %v", err)
	}
	want := "@lkal \"0.1\"\n\nlocal {\n  KRATOS_COOKIE_DOMAIN = \"\"\n}\nfeature_flags {\n  AUTH_SHARED_SESSION_CACHE = false\n  KNOWLEDGE_HOME_ENABLE_LENS = true\n}\n"
	if got := string(f.Format()); got != want {
		t.Fatalf("unexpected formatted output:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestVerifyFrameConditionViolations(t *testing.T) {
	before := lkal.Record{
		Fields: []lkal.Field{
			{
				Key: "a",
				Value: lkal.Record{
					Fields: []lkal.Field{
						{Key: "target", Value: lkal.Int(1)},
						{Key: "sibling", Value: lkal.Int(2)},
					},
				},
			},
			{
				Key:   "empty_rec",
				Value: lkal.Record{},
			},
		},
	}

	targetPath := model.MustParsePath("/a/target")

	t.Run("valid target update satisfies Frame Condition", func(t *testing.T) {
		after := lkal.Record{
			Fields: []lkal.Field{
				{
					Key: "a",
					Value: lkal.Record{
						Fields: []lkal.Field{
							{Key: "target", Value: lkal.Int(99)},
							{Key: "sibling", Value: lkal.Int(2)},
						},
					},
				},
				{
					Key:   "empty_rec",
					Value: lkal.Record{},
				},
			},
		}
		if err := structure.VerifyFrameCondition(before, after, targetPath); err != nil {
			t.Fatalf("expected Frame Condition to pass, got %v", err)
		}
	})

	t.Run("detects non-target sibling value mutation", func(t *testing.T) {
		after := lkal.Record{
			Fields: []lkal.Field{
				{
					Key: "a",
					Value: lkal.Record{
						Fields: []lkal.Field{
							{Key: "target", Value: lkal.Int(99)},
							{Key: "sibling", Value: lkal.Int(999)},
						},
					},
				},
				{
					Key:   "empty_rec",
					Value: lkal.Record{},
				},
			},
		}
		err := structure.VerifyFrameCondition(before, after, targetPath)
		diag := assertDiagnosticCode(t, err, model.CodeStructureConflict)
		if !diag.Path.Equal(model.MustParsePath("/a/sibling")) {
			t.Fatalf("expected violation at /a/sibling, got %s", diag.Path)
		}
	})

	t.Run("detects auto-cleaned existing Record violation", func(t *testing.T) {
		after := lkal.Record{
			Fields: []lkal.Field{
				{
					Key: "a",
					Value: lkal.Record{
						Fields: []lkal.Field{
							{Key: "target", Value: lkal.Int(1)},
							{Key: "sibling", Value: lkal.Int(2)},
						},
					},
				},
			},
		}
		err := structure.VerifyFrameCondition(before, after, targetPath)
		diag := assertDiagnosticCode(t, err, model.CodeStructureConflict)
		if !diag.Path.Equal(model.MustParsePath("/empty_rec")) {
			t.Fatalf("expected violation at /empty_rec, got %s", diag.Path)
		}
	})
}
