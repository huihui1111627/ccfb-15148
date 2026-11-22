package bitejson

import (
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// unquoteString decodes the body of a JSON string (raw[start:end], without the
// surrounding quotes) into buf, appending the result. It reproduces the
// decoding rules of encoding/json: escapes are expanded, \u surrogate pairs are
// combined, lone surrogates become U+FFFD, and invalid UTF-8 bytes are replaced
// with U+FFFD per byte. The caller has already validated the string, so this
// does not report errors.
func unquoteString(raw []byte, start, end int, buf []byte) []byte {
	body := raw[start:end]
	// Fast path: no backslash and all ASCII needs no copying beyond the body.
	need := false
	for _, c := range body {
		if c == '\\' || c >= utf8.RuneSelf {
			need = true
			break
		}
	}
	if !need {
		return append(buf, body...)
	}
	i := 0
	for i < len(body) {
		c := body[i]
		if c == '\\' {
			i++
			switch body[i] {
			case '"':
				buf = append(buf, '"')
			case '\\':
				buf = append(buf, '\\')
			case '/':
				buf = append(buf, '/')
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'u':
				r := hex4(body[i+1:])
				i += 4
				if utf16.IsSurrogate(r) {
					if i+6 < len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
						r2 := hex4(body[i+3:])
						if dec := utf16.DecodeRune(r, r2); dec != utf8.RuneError {
							i += 6
							buf = utf8.AppendRune(buf, dec)
							i++
							continue
						}
					}
					r = utf8.RuneError
				}
				buf = utf8.AppendRune(buf, r)
			}
			i++
			continue
		}
		if c < utf8.RuneSelf {
			buf = append(buf, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(body[i:])
		buf = utf8.AppendRune(buf, r)
		i += size
	}
	return buf
}

// hex4 decodes four hex digits at the start of b.
func hex4(b []byte) rune {
	var r rune
	for k := 0; k < 4; k++ {
		r = r*16 + hexVal(b[k])
	}
	return r
}

func hexVal(c byte) rune {
	switch {
	case '0' <= c && c <= '9':
		return rune(c - '0')
	case 'a' <= c && c <= 'f':
		return rune(c-'a') + 10
	default:
		return rune(c-'A') + 10
	}
}

// scalarKind is the JSON type of a decoded scalar.
type scalarKind uint8

const (
	kindNull scalarKind = iota
	kindBool
	kindNumber
	kindString
	kindObject
	kindArray
)

func (k scalarKind) String() string {
	switch k {
	case kindNull:
		return "null"
	case kindBool:
		return "bool"
	case kindNumber:
		return "number"
	case kindString:
		return "string"
	case kindObject:
		return "object"
	case kindArray:
		return "array"
	}
	return "unknown"
}

// scalarInput describes one value to decode: its kind and raw extent. For
// strings start/end exclude the quotes; for numbers they are the literal.
type scalarInput struct {
	kind  scalarKind
	raw   []byte
	start int
	end   int
}

// classifyValue returns the kind of the value whose first byte is raw[start].
func classifyValue(raw []byte, start, end int) scalarKind {
	if start >= end {
		return kindNull
	}
	switch raw[start] {
	case '{':
		return kindObject
	case '[':
		return kindArray
	case '"':
		return kindString
	case 't', 'f':
		return kindBool
	case 'n':
		return kindNull
	default:
		return kindNumber
	}
}

// numNorm is a number normalised so that values which are equal as float64
// compare equal, independent of how they were written (1 vs 1.0 vs 1e0).
type numNorm struct {
	f       float64
	isInt   bool
	i64     int64
	u64     uint64
	negZero bool
}

// parseNumberValue parses a JSON number literal. If strictFloat is set and the
// value overflows float64, it returns ok=false. The common cases (small
// integers, ordinary decimals) avoid allocation by scanning bytes directly;
// only large or exponential literals fall back to strconv.
func parseNumberValue(raw []byte, start, end int, strictFloat bool) (numNorm, bool) {
	s := raw[start:end]
	var n numNorm
	neg := false
	i := 0
	if i < len(s) && s[i] == '-' {
		neg = true
		i++
	}
	// Fast path: integer with no fraction or exponent that fits comfortably.
	if !hasFloatSyntax(s) {
		if v, ok := fastUint(s[i:]); ok {
			n.isInt = true
			if neg {
				n.i64 = -int64(v)
				n.f = -float64(v)
			} else {
				n.u64 = v
				n.f = float64(v)
			}
			return n, true
		}
		// Fall back to strconv for integers at the edge of the range.
		if neg {
			if v, err := strconv.ParseInt(string(s), 10, 64); err == nil {
				n.isInt, n.i64, n.f = true, v, float64(v)
				return n, true
			}
		} else if v, err := strconv.ParseUint(string(s), 10, 64); err == nil {
			n.isInt, n.u64, n.f = true, v, float64(v)
			return n, true
		}
	}
	f, err := strconv.ParseFloat(string(s), 64)
	if err != nil && strictFloat {
		return n, false
	}
	n.f = f
	if math.Signbit(f) && f == 0 {
		n.negZero = true
	}
	return n, true
}

// fastUint parses up to 18 decimal digits into a uint64 without allocation.
func fastUint(d []byte) (uint64, bool) {
	if len(d) == 0 || len(d) > 18 {
		return 0, false
	}
	var v uint64
	for _, c := range d {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	return v, true
}

// hasFloatSyntax reports whether s contains '.', 'e' or 'E'.
func hasFloatSyntax(s []byte) bool {
	for _, c := range s {
		if c == '.' || c == 'e' || c == 'E' {
			return true
		}
	}
	return false
}
