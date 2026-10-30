package model_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
)

func TestSecurityRegression_GC07_CanonicalLeafEquality(t *testing.T) {
	if !model.EqualLeafValues(lkal.Decimal("2.00"), lkal.Decimal("2.0")) {
		t.Fatal("expected canonical decimal equality for 2.00 and 2.0")
	}
	if !model.EqualLeafValues(lkal.Decimal("-0.0"), lkal.Decimal("0.0")) {
		t.Fatal("expected canonical decimal equality for -0.0 and 0.0")
	}
	heterogeneous := lkal.List{lkal.Int(1), lkal.String("two")}
	if model.IsLeafValue(heterogeneous) {
		t.Fatal("expected heterogeneous list to be rejected by IsLeafValue")
	}
	if model.EqualLeafValues(heterogeneous, heterogeneous) {
		t.Fatal("expected invalid leaf values to return false in EqualLeafValues")
	}
}

func TestSecurityRegression_GC17_ValidateLeafPathRejectsEmptyAndControlChars(t *testing.T) {
	invalidPaths := []model.Path{
		{"app", ""},
		{"app", "bad\x00seg"},
		{"app", "bad\nseg"},
		{"app", "bad\x7fseg"},
	}
	for _, p := range invalidPaths {
		if err := model.ValidateLeafPath(p); err == nil {
			t.Fatalf("expected ValidateLeafPath(%v) to fail", p)
		}
	}
}
