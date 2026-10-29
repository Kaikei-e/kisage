package model

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

func formatLKALSegments(segs []string) string {
	if len(segs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, seg := range segs {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(formatPathSegment(seg))
	}
	return b.String()
}

func formatPathSegment(seg string) string {
	if isBareSegment(seg) {
		return seg
	}
	return quoteLKALString(seg)
}

func isSegmentChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '.' || b == '-'
}

func isBareSegment(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isSegmentChar(s[i]) {
			return false
		}
	}
	return true
}

func isControlRune(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7f
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func parseRefSegments(src string) ([]string, bool) {
	if len(src) == 0 {
		return nil, false
	}
	i := 0
	var segs []string
	for {
		seg, nextIdx, ok := scanNextSegment(src, i)
		if !ok {
			return nil, false
		}
		segs = append(segs, seg)
		i = nextIdx
		if i == len(src) {
			return segs, true
		}
		if src[i] != '/' {
			return nil, false
		}
		i++
	}
}

func scanNextSegment(src string, off int) (string, int, bool) {
	if off >= len(src) {
		return "", off, false
	}
	if src[off] == '"' {
		return scanQuotedSegment(src, off)
	}
	if isSegmentChar(src[off]) {
		return scanBareSegment(src, off)
	}
	return "", off, false
}

func scanBareSegment(src string, off int) (string, int, bool) {
	i := off
	for i < len(src) && isSegmentChar(src[i]) {
		i++
	}
	return src[off:i], i, i > off
}

func scanQuotedSegment(src string, off int) (string, int, bool) {
	var decoded strings.Builder
	i := off + 1
	for i < len(src) {
		if src[i] == '"' {
			return decoded.String(), i + 1, true
		}
		if src[i] == '\\' {
			r, nextIdx, ok := scanEscapeSequence(src, i+1)
			if !ok {
				return "", nextIdx, false
			}
			decoded.WriteRune(r)
			i = nextIdx
			continue
		}
		r, sz := utf8.DecodeRuneInString(src[i:])
		if (r == utf8.RuneError && sz == 1) || isControlRune(r) {
			return "", i, false
		}
		decoded.WriteRune(r)
		i += sz
	}
	return "", i, false
}

func scanEscapeSequence(src string, off int) (rune, int, bool) {
	if off >= len(src) {
		return 0, off, false
	}
	switch src[off] {
	case '"':
		return '"', off + 1, true
	case '\\':
		return '\\', off + 1, true
	case 'n':
		return '\n', off + 1, true
	case 'r':
		return '\r', off + 1, true
	case 't':
		return '\t', off + 1, true
	case 'u':
		return scanUnicodeEscape(src, off+1)
	default:
		return 0, off, false
	}
}

func scanUnicodeEscape(src string, off int) (rune, int, bool) {
	if off >= len(src) || src[off] != '{' {
		return 0, off, false
	}
	i := off + 1
	hexStart := i
	for i < len(src) && isHexByte(src[i]) {
		i++
	}
	if i >= len(src) || src[i] != '}' {
		return 0, i, false
	}
	hexStr := src[hexStart:i]
	if len(hexStr) == 0 || len(hexStr) > 6 {
		return 0, i + 1, false
	}
	val, err := strconv.ParseUint(hexStr, 16, 32)
	if err != nil || val > 0x10FFFF || (val >= 0xD800 && val <= 0xDFFF) {
		return 0, i + 1, false
	}
	return rune(val), i + 1, true
}

func quoteLKALString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if isControlRune(r) {
				b.WriteString(`\u{`)
				b.WriteString(strconv.FormatInt(int64(r), 16))
				b.WriteByte('}')
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
