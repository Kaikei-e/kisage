package projection_test

import (
	"errors"
	"strings"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/projection"
)

func assertDiagnosticCode(t *testing.T, err error, want model.DiagnosticCode) model.Diagnostic {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var diag model.Diagnostic
	if !errors.As(err, &diag) {
		t.Fatalf("expected model.Diagnostic (want %s), got %T: %v", want, err, err)
	}
	if diag.Code != want {
		t.Fatalf("expected diagnostic code %s, got %s (%v)", want, diag.Code, diag)
	}
	return diag
}

// TestCounterexample09_DeclarationEmptyRecordRule verifies the declaration side of Counterexample #9 (ADR 0001 §2):
//   - Non-root empty Record in declaration d is rejected with K102 (EmptyRecordNotSupported).
//   - Empty root declaration d is permitted as D = ∅.
func TestCounterexample09_DeclarationEmptyRecordRule(t *testing.T) {
	t.Run("non-root empty Record in declaration is rejected with K102", func(t *testing.T) {
		_, err := projection.ParseDeclaration("decl.lkal", []byte("@lkal \"0.1\"\na {}\n"))
		diag := assertDiagnosticCode(t, err, model.CodeEmptyRecordNotSupported)
		if !diag.Path.Equal(model.MustParsePath("/a")) {
			t.Fatalf("expected path /a, got %s", diag.Path)
		}
	})

	t.Run("nested empty Record via label syntax in declaration is rejected with K102", func(t *testing.T) {
		_, err := projection.ParseDeclaration("decl.lkal", []byte("@lkal \"0.1\"\nservice \"api\" {}\n"))
		diag := assertDiagnosticCode(t, err, model.CodeEmptyRecordNotSupported)
		if !diag.Path.Equal(model.MustParsePath("/service/api")) {
			t.Fatalf("expected path /service/api, got %s", diag.Path)
		}
	})

	t.Run("empty root declaration is permitted as D = empty set", func(t *testing.T) {
		decl, err := projection.ParseDeclaration("empty.lkal", []byte("@lkal \"0.1\"\n### comment only\n"))
		if err != nil {
			t.Fatalf("expected empty root declaration to succeed, got %v", err)
		}
		if !decl.IsEmpty() || decl.Len() != 0 || len(decl.Paths()) != 0 {
			t.Fatalf("expected D = ∅, got len=%d", decl.Len())
		}
	})
}

// TestCounterexample15_QuotedSegmentsProjection verifies Counterexample #15 on declaration projection.
func TestCounterexample15_QuotedSegmentsProjection(t *testing.T) {
	src := []byte(`@lkal "0.1"
"a/b" {
  c = 10
}
a {
  b {
    c = 20
  }
}
"a.b" {
  c = 30
}
`)
	decl, err := projection.ParseDeclaration("quoted.lkal", src)
	if err != nil {
		t.Fatalf("ParseDeclaration failed: %v", err)
	}
	if decl.Len() != 3 {
		t.Fatalf("expected 3 leaf paths, got %d", decl.Len())
	}

	v1, ok := decl.Get(model.MustParsePath(`/"a/b"/c`))
	if !ok || !model.EqualLeafValues(v1, lkal.Int(10)) {
		t.Fatalf("expected /\"a/b\"/c = 10, got %v (ok=%v)", v1, ok)
	}
	v2, ok := decl.Get(model.MustParsePath(`/a/b/c`))
	if !ok || !model.EqualLeafValues(v2, lkal.Int(20)) {
		t.Fatalf("expected /a/b/c = 20, got %v (ok=%v)", v2, ok)
	}
	v3, ok := decl.Get(model.MustParsePath(`/"a.b"/c`))
	if !ok || !model.EqualLeafValues(v3, lkal.Int(30)) {
		t.Fatalf("expected /\"a.b\"/c = 30, got %v (ok=%v)", v3, ok)
	}
}

// TestLKALBoundaryAndLeafValueSemantics verifies ADR 0001 §1:
//   - All 7 scalar types and List are supported as leaf values.
//   - List is an atomic ordered leaf value (not expanded by element index).
//   - EqualLeafValues delegates to lkal.Equal without implicit conversion.
func TestLKALBoundaryAndLeafValueSemantics(t *testing.T) {
	src := []byte(`@lkal "0.1"
app {
  name = "kisage"
  replicas = 3
  ratio = 0.50
  enabled = true
  mode = .rolling-update
  memory = 512Mi
  upstream = &services/backend
  ports = [8080, 8443]
  empty_tags = []
}
`)
	decl, err := projection.ParseDeclaration("app.lkal", src)
	if err != nil {
		t.Fatalf("ParseDeclaration failed: %v", err)
	}
	if decl.Len() != 9 {
		t.Fatalf("expected 9 projected leaf paths, got %d", decl.Len())
	}

	ratioVal, ok := decl.Get(model.MustParsePath("/app/ratio"))
	if !ok {
		t.Fatalf("missing /app/ratio")
	}
	if !model.EqualLeafValues(ratioVal, lkal.Decimal("0.5")) {
		t.Fatalf("expected /app/ratio to equal Decimal(0.5), got %v", ratioVal)
	}
	if model.EqualLeafValues(lkal.Int(1), lkal.Decimal("1.0")) {
		t.Fatalf("Int(1) and Decimal(1.0) must not be equal")
	}
	if model.EqualLeafValues(lkal.Quantity{Number: "1000", Unit: "m"}, lkal.Quantity{Number: "1", Unit: "km"}) {
		t.Fatalf("1000m and 1km must not be equal without unit conversion")
	}

	portsVal, ok := decl.Get(model.MustParsePath("/app/ports"))
	if !ok {
		t.Fatalf("missing /app/ports")
	}
	if !model.EqualLeafValues(portsVal, lkal.List{lkal.Int(8080), lkal.Int(8443)}) {
		t.Fatalf("expected [8080, 8443] to match")
	}
	if model.EqualLeafValues(portsVal, lkal.List{lkal.Int(8443), lkal.Int(8080)}) {
		t.Fatalf("reordered List [8443, 8080] must not equal [8080, 8443]")
	}

	emptyTags, ok := decl.Get(model.MustParsePath("/app/empty_tags"))
	if !ok || !model.EqualLeafValues(emptyTags, lkal.List{}) {
		t.Fatalf("expected /app/empty_tags = [], got %v (ok=%v)", emptyTags, ok)
	}
}

func TestProgrammaticBoundaryValidation(t *testing.T) {
	t.Run("rejects $ reserved key", func(t *testing.T) {
		rec := lkal.Record{
			Fields: []lkal.Field{
				{Key: "$schema", Value: lkal.String("v1")},
			},
		}
		_, err := projection.ProjectRecord(rec)
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects duplicate keys in programmatic Record", func(t *testing.T) {
		rec := lkal.Record{
			Fields: []lkal.Field{
				{Key: "a", Value: lkal.Int(1)},
				{Key: "a", Value: lkal.Int(2)},
			},
		}
		_, err := projection.ProjectRecord(rec)
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects heterogeneous List", func(t *testing.T) {
		_, err := model.ValidateLeafValue(lkal.List{lkal.Int(1), lkal.String("two")})
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects nested List", func(t *testing.T) {
		_, err := model.ValidateLeafValue(lkal.List{lkal.List{lkal.Int(1)}})
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects Record in List", func(t *testing.T) {
		_, err := model.ValidateLeafValue(lkal.List{lkal.Record{}})
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects Decimal over 100 digits", func(t *testing.T) {
		tooLong := "1." + strings.Repeat("5", 100)
		_, err := model.ValidateLeafValue(lkal.Decimal(tooLong))
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects block nesting over 1000 levels", func(t *testing.T) {
		rec := lkal.Record{
			Fields: []lkal.Field{{Key: "leaf", Value: lkal.Int(1)}},
		}
		for i := 0; i < 1001; i++ {
			rec = lkal.Record{
				Fields: []lkal.Field{{Key: "n", Value: rec}},
			}
		}
		_, err := projection.ProjectRecord(rec)
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})
}
