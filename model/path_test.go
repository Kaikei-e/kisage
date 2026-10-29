package model_test

import (
	"errors"
	"testing"

	"github.com/Kaikei-e/kisage/model"
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

// TestCounterexample15_QuotedSegmentsWithSlashAndDot verifies Counterexample #15 (ADR 0001 §1):
// Path segments containing '/' or '.' must be compared as parsed segment sequences rather than raw string prefixes.
func TestCounterexample15_QuotedSegmentsWithSlashAndDot(t *testing.T) {
	pQuotedSlash := model.MustParsePath(`/"a/b"/c`)
	pThreeSegs := model.MustParsePath(`/a/b/c`)
	pA := model.MustParsePath(`/a`)
	pQuotedSlashParent := model.MustParsePath(`/"a/b"`)

	if pQuotedSlash.Len() != 2 || pQuotedSlash.Segments()[0] != "a/b" || pQuotedSlash.Segments()[1] != "c" {
		t.Fatalf("unexpected segments for %s: %#v", pQuotedSlash, pQuotedSlash.Segments())
	}
	if pQuotedSlash.Equal(pThreeSegs) {
		t.Fatalf("expected /\"a/b\"/c and /a/b/c to be distinct paths")
	}

	// /a is a proper ancestor of /a/b/c, but MUST NOT be treated as an ancestor of /"a/b"/c!
	if !pA.IsProperAncestorOf(pThreeSegs) {
		t.Fatalf("expected /a to be a proper ancestor of /a/b/c")
	}
	if pA.IsProperAncestorOf(pQuotedSlash) || pA.Overlaps(pQuotedSlash) {
		t.Fatalf("/a must not be an ancestor of or overlap with /\"a/b\"/c")
	}
	if !pQuotedSlashParent.IsProperAncestorOf(pQuotedSlash) {
		t.Fatalf("expected /\"a/b\" to be a proper ancestor of /\"a/b\"/c")
	}

	roundTrip, err := model.ParsePath(pQuotedSlash.String())
	if err != nil {
		t.Fatalf("ParsePath(%q) failed: %v", pQuotedSlash.String(), err)
	}
	if !roundTrip.Equal(pQuotedSlash) {
		t.Fatalf("round-trip mismatch: got %v, want %v", roundTrip, pQuotedSlash)
	}
}

func TestPathParsingAndEscapes(t *testing.T) {
	p, err := model.NewPath("a\nb", `c"d`, "e\\f", "g\th")
	if err != nil {
		t.Fatalf("NewPath failed: %v", err)
	}
	formatted := p.String()
	parsed, err := model.ParsePath(formatted)
	if err != nil {
		t.Fatalf("ParsePath(%q) failed: %v", formatted, err)
	}
	if !parsed.Equal(p) {
		t.Fatalf("round-trip failed: got %#v, want %#v", parsed, p)
	}

	uPath, err := model.ParsePath(`/"hello\u{20}world"/leaf`)
	if err != nil {
		t.Fatalf("ParsePath with unicode escape failed: %v", err)
	}
	if uPath.Segments()[0] != "hello world" {
		t.Fatalf("expected 'hello world', got %q", uPath.Segments()[0])
	}

	for _, bad := range []string{"", "/", "/a//b", `/"unterminated`, `/"bad\xescape"`} {
		if _, err := model.ParsePath(bad); err == nil {
			t.Fatalf("expected ParsePath(%q) to fail", bad)
		} else {
			assertDiagnosticCode(t, err, model.CodeInvalidInput)
		}
	}
}

func TestSortDiagnosticsOrder(t *testing.T) {
	diags := []model.Diagnostic{
		{Code: model.CodeStructureConflict, Path: model.MustParsePath("/b"), Reason: "r"},
		{Code: model.CodeUnrepresentable, Path: model.MustParsePath("/a"), Reason: "r"},
		{Code: model.CodeStructureConflict, Path: model.MustParsePath("/a"), Reason: "r"},
	}
	model.SortDiagnostics(diags)
	if !diags[0].Path.Equal(model.MustParsePath("/a")) || diags[0].Code != model.CodeStructureConflict {
		t.Fatalf("unexpected first diagnostic after sort: %+v", diags[0])
	}
	if !diags[1].Path.Equal(model.MustParsePath("/a")) || diags[1].Code != model.CodeUnrepresentable {
		t.Fatalf("unexpected second diagnostic after sort: %+v", diags[1])
	}
	if !diags[2].Path.Equal(model.MustParsePath("/b")) {
		t.Fatalf("unexpected third diagnostic after sort: %+v", diags[2])
	}
}

