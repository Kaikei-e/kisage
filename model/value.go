package model

import (
	"slices"
	"strings"
	"unicode/utf8"

	lkal "github.com/Kaikei-e/LKAL"
)

const maxLKALDigits = 100

// EqualLeafValues compares two leaf values using LKAL's deterministic Equal without implicit conversion (ADR 0001 §1).
func EqualLeafValues(a, b lkal.Value) bool {
	return IsLeafValue(a) && IsLeafValue(b) && lkal.Equal(a, b)
}

// IsScalar reports whether v is one of the 7 LKAL scalar types
// (String, Int, Decimal, Bool, Enum, Quantity, Ref).
func IsScalar(v lkal.Value) bool {
	return scalarTypeName(v) != ""
}

// IsLeafValue reports whether v is an LKAL scalar or List (and not a Record or nil).
func IsLeafValue(v lkal.Value) bool {
	if IsScalar(v) {
		return true
	}
	_, ok := v.(lkal.List)
	return ok
}

// ValidateLeafValue checks that a programmatic lkal.Value is a valid LKAL leaf value
// (scalar or homogeneous scalar List) under LKAL's semantic and lexical rules, and returns
// its canonical normalized copy (ADR 0001 §1).
func ValidateLeafValue(v lkal.Value) (lkal.Value, error) {
	norm, reason := ValidateAndNormalizeLeaf(v)
	if reason != "" {
		return nil, Diagnostic{
			Code:   CodeInvalidInput,
			Reason: reason,
		}
	}
	return norm, nil
}

// ValidateAndNormalizeLeaf validates v as an LKAL leaf value and returns its canonical copy
// or a non-empty error reason string.
func ValidateAndNormalizeLeaf(v lkal.Value) (lkal.Value, string) {
	if v == nil {
		return nil, "use a non-nil value"
	}
	switch val := v.(type) {
	case lkal.String:
		return validateString(val)
	case lkal.Int, lkal.Bool:
		return val, ""
	case lkal.Decimal:
		return validateDecimal(val)
	case lkal.Quantity:
		return validateQuantity(val)
	case lkal.Enum:
		return validateEnum(val)
	case lkal.Ref:
		return validateRef(val)
	case lkal.List:
		return validateList(val)
	case lkal.Record:
		return nil, "Record is not a leaf value"
	default:
		return nil, "unsupported LKAL value type"
	}
}

// CloneLeafValue returns a defensive copy of a validated LKAL leaf value.
func CloneLeafValue(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Ref:
		return slices.Clone(val)
	case lkal.List:
		out := make(lkal.List, len(val))
		for i, el := range val {
			out[i] = CloneLeafValue(el)
		}
		return out
	default:
		return v
	}
}

func validateString(val lkal.String) (lkal.Value, string) {
	if !utf8.ValidString(string(val)) {
		return nil, "use valid UTF-8 text"
	}
	return val, ""
}

func validateDecimal(val lkal.Decimal) (lkal.Value, string) {
	digits, hasFrac, ok := scanLKALNumber(string(val))
	if !ok || !hasFrac {
		return nil, "write a decimal such as 0.25"
	}
	if digits > maxLKALDigits {
		return nil, "keep numbers within 100 digits"
	}
	return lkal.Decimal(normalizeLKALNumber(string(val))), ""
}

func validateQuantity(val lkal.Quantity) (lkal.Value, string) {
	digits, _, ok := scanLKALNumber(val.Number)
	if !ok || !isValidQuantityUnit(val.Number, val.Unit) {
		return nil, "write a quantity such as 512Mi"
	}
	if digits > maxLKALDigits {
		return nil, "keep numbers within 100 digits"
	}
	return lkal.Quantity{
		Number: normalizeLKALNumber(val.Number),
		Unit:   val.Unit,
	}, ""
}

func isValidQuantityUnit(num, unit string) bool {
	if len(unit) == 0 {
		return false
	}
	if (num == "0" || num == "-0") && isLKALRadixPrefix(unit[0]) {
		return false
	}
	for _, r := range unit {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

func validateEnum(val lkal.Enum) (lkal.Value, string) {
	s := string(val)
	if !utf8.ValidString(s) {
		return nil, "use valid UTF-8 text"
	}
	if !isValidLKALEnum(s) {
		return nil, "write an enum name that starts with a letter or _"
	}
	return val, ""
}

func validateRef(val lkal.Ref) (lkal.Value, string) {
	if len(val) == 0 {
		return nil, "write a reference with at least one segment"
	}
	for _, seg := range val {
		if !utf8.ValidString(seg) {
			return nil, "use valid UTF-8 text"
		}
	}
	return slices.Clone(val), ""
}

func validateList(val lkal.List) (lkal.Value, string) {
	normList := make(lkal.List, len(val))
	var firstType string
	for i, el := range val {
		if !IsScalar(el) {
			return nil, "use only scalars as list elements"
		}
		normEl, reason := ValidateAndNormalizeLeaf(el)
		if reason != "" {
			return nil, reason
		}
		elType := scalarTypeName(normEl)
		if i == 0 {
			firstType = elType
		} else if elType != firstType {
			return nil, "use one type for all list elements"
		}
		normList[i] = normEl
	}
	return normList, ""
}

func scalarTypeName(v lkal.Value) string {
	switch v.(type) {
	case lkal.String:
		return "String"
	case lkal.Int:
		return "Int"
	case lkal.Decimal:
		return "Decimal"
	case lkal.Bool:
		return "Bool"
	case lkal.Enum:
		return "Enum"
	case lkal.Quantity:
		return "Quantity"
	case lkal.Ref:
		return "Ref"
	default:
		return ""
	}
}

func isValidLKALEnum(s string) bool {
	if len(s) == 0 {
		return false
	}
	b0 := s[0]
	if !((b0 >= 'A' && b0 <= 'Z') || (b0 >= 'a' && b0 <= 'z') || b0 == '_') {
		return false
	}
	for i := 1; i < len(s); i++ {
		b := s[i]
		if !((b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '-') {
			return false
		}
	}
	return true
}

func isLKALRadixPrefix(b byte) bool {
	return b == 'x' || b == 'X' || b == 'b' || b == 'B' || b == 'o' || b == 'O'
}

func scanLKALNumber(s string) (int, bool, bool) {
	s = strings.TrimPrefix(s, "-")
	intDigits, rest, ok := scanIntegerDigits(s)
	if !ok {
		return 0, false, false
	}
	if !strings.HasPrefix(rest, ".") {
		return intDigits, false, len(rest) == 0
	}
	fracDigits, tail := scanDigitRun(rest[1:])
	if fracDigits == 0 || len(tail) > 0 {
		return 0, false, false
	}
	return intDigits + fracDigits, true, true
}

func scanIntegerDigits(s string) (int, string, bool) {
	if len(s) == 0 {
		return 0, "", false
	}
	if s[0] == '0' {
		rest := s[1:]
		if len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			return 0, "", false
		}
		return 1, rest, true
	}
	if s[0] >= '1' && s[0] <= '9' {
		digits, rest := scanDigitRun(s)
		return digits, rest, true
	}
	return 0, "", false
}

func scanDigitRun(s string) (int, string) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n, s[n:]
}

func normalizeLKALNumber(s string) string {
	if s == "-0" {
		return "0"
	}
	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if !hasDot {
		return s
	}
	isNeg := strings.HasPrefix(intPart, "-")
	intPart = strings.TrimPrefix(intPart, "-")
	fracPart = strings.TrimRight(fracPart, "0")
	if fracPart == "" {
		fracPart = "0"
	}
	if intPart == "0" && fracPart == "0" {
		return "0.0"
	}
	if isNeg {
		return "-" + intPart + "." + fracPart
	}
	return intPart + "." + fracPart
}
