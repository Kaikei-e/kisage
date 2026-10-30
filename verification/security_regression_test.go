package verification_test

import (
	"testing"
)

func TestSecurityRegression_GC09_LeanScannerRejectsEscapedStringsAndAxioms(t *testing.T) {
	badSources := []string{
		"theorem t : True := sorryAx True False",
		"theorem t : True := by admit",
		"axiom bad_ax : False",
		"unsafe def bad : Nat := 0",
		`def s := "escaped \" quote" sorry`,
		"/- unclosed block comment sorry",
		"\"unclosed string literal",
		"theorem t : True := -/ trivial",
	}
	for _, src := range badSources {
		if err := checkLeanFileForSorry(src); err == nil {
			t.Fatalf("expected checkLeanFileForSorry to reject %q", src)
		}
	}

	validSources := []string{
		`def msg := "string containing sorry and /- inside quotes \" still in string"`,
		"/- nested /- comment with sorry and axiom -/ still in comment -/\ntheorem ok : True := trivial\n",
		"-- line comment with sorryAx and admit\ntheorem ok2 : True := trivial\n",
	}
	for _, src := range validSources {
		if err := checkLeanFileForSorry(src); err != nil {
			t.Fatalf("expected checkLeanFileForSorry to accept %q, got %v", src, err)
		}
	}
}
