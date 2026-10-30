package model

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxLKALBlockDepth is the block nesting limit of the pinned LKAL implementation.
const MaxLKALBlockDepth = 1000

// Path represents a parsed sequence of path segments in an LKAL document or target resource.
// All equality, ordering, ancestor/descendant, and Scope checks operate on parsed segments
// rather than raw string prefixes (ADR 0001 §1, ADR 0002 §3, Counterexample #15).
type Path []string

// ValidateLeafPath validates a target leaf path before any LKAL edits. Parsing a
// reference path alone does not reject reserved record keys or excessive depth.
// Path is an exported slice, so programmatically constructed segments need UTF-8
// validation here as well.
func ValidateLeafPath(p Path) error {
	reason := ""
	switch {
	case len(p) == 0:
		reason = "target path must not be empty"
	case len(p)-1 > MaxLKALBlockDepth:
		reason = "nest blocks at most 1000 levels deep"
	default:
		for _, seg := range p {
			if seg == "" {
				reason = "path segment must not be empty"
				break
			}
			if !utf8.ValidString(seg) {
				reason = "path segment contains invalid UTF-8"
				break
			}
			if hasControlRune(seg) {
				reason = "path segment must not contain control characters"
				break
			}
			if strings.HasPrefix(seg, "$") {
				reason = fmt.Sprintf("key %q must not start with $", seg)
				break
			}
		}
	}
	if reason != "" {
		return Diagnostic{Code: CodeInvalidInput, Path: p.Clone(), Reason: reason}
	}
	return nil
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// NewPath constructs a non-root Path from parsed segments after validating UTF-8 encoding.
func NewPath(segs ...string) (Path, error) {
	if len(segs) == 0 {
		return nil, Diagnostic{
			Code:   CodeInvalidInput,
			Reason: "path must contain at least one segment",
		}
	}
	for _, s := range segs {
		if !utf8.ValidString(s) {
			return nil, Diagnostic{
				Code:   CodeInvalidInput,
				Reason: "path segment contains invalid UTF-8",
			}
		}
	}
	return slices.Clone(Path(segs)), nil
}

// ParsePath parses a path string (with or without a single leading '/') into segments
// according to LKAL's path segment grammar (bare [A-Za-z0-9_.-]+ or quoted "...").
func ParsePath(s string) (Path, error) {
	if s == "" || s == "/" {
		return nil, Diagnostic{
			Code:   CodeInvalidInput,
			Reason: "malformed path: path must not be empty",
		}
	}
	body := strings.TrimPrefix(s, "/")
	segs, ok := parseRefSegments(body)
	if !ok || len(segs) == 0 {
		return nil, Diagnostic{
			Code:   CodeInvalidInput,
			Reason: "malformed path: separate segments with / and quote segments that contain other characters",
		}
	}
	return Path(segs), nil
}

// MustParsePath parses s with ParsePath and panics on error. Intended for tests and static constants.
func MustParsePath(s string) Path {
	p, err := ParsePath(s)
	if err != nil {
		panic(err)
	}
	return p
}

// Segments returns a defensive copy of the path segments.
func (p Path) Segments() []string {
	return slices.Clone([]string(p))
}

// Clone returns a defensive copy of p.
func (p Path) Clone() Path {
	if p == nil {
		return nil
	}
	return slices.Clone(p)
}

// Append returns a new Path with seg appended, without mutating p.
func (p Path) Append(seg string) Path {
	out := make(Path, len(p)+1)
	copy(out, p)
	out[len(p)] = seg
	return out
}

// Len returns the number of segments in p.
func (p Path) Len() int {
	return len(p)
}

// IsRoot reports whether p is the empty root path.
func (p Path) IsRoot() bool {
	return len(p) == 0
}

// Equal reports whether p and other have identical segments.
func (p Path) Equal(other Path) bool {
	return slices.Equal(p, other)
}

// Compare compares p and other lexicographically by parsed segment sequence (ADR 0003 §1).
func (p Path) Compare(other Path) int {
	return slices.Compare(p, other)
}

// IsProperAncestorOf reports whether p is a proper ancestor of other (p ≺ other).
func (p Path) IsProperAncestorOf(other Path) bool {
	return len(p) > 0 && len(p) < len(other) && slices.Equal(p, other[:len(p)])
}

// IsProperDescendantOf reports whether p is a proper descendant of other (other ≺ p).
func (p Path) IsProperDescendantOf(other Path) bool {
	return other.IsProperAncestorOf(p)
}

// Overlaps reports whether p and other are the same path, or one is a proper ancestor of the other.
func (p Path) Overlaps(other Path) bool {
	return p.Equal(other) || p.IsProperAncestorOf(other) || other.IsProperAncestorOf(p)
}

// Parent returns the immediate parent path of p if len(p) > 1.
func (p Path) Parent() (Path, bool) {
	if len(p) <= 1 {
		return nil, false
	}
	return slices.Clone(p[:len(p)-1]), true
}

// Ancestors returns all proper non-root ancestor paths of p in order from shortest (depth 1) to parent.
func (p Path) Ancestors() []Path {
	if len(p) <= 1 {
		return nil
	}
	anc := make([]Path, len(p)-1)
	for i := 1; i < len(p); i++ {
		anc[i-1] = slices.Clone(p[:i])
	}
	return anc
}

// LKALPath formats p without a leading '/' in canonical LKAL path syntax (for lkal.File.Get/Set/Delete).
func (p Path) LKALPath() string {
	return formatLKALSegments(p)
}

// String formats p in canonical kisage path notation with a leading '/'.
func (p Path) String() string {
	if len(p) == 0 {
		return "/"
	}
	return "/" + p.LKALPath()
}
