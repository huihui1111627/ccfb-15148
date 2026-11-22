package bitejson

import "fmt"

// ErrorKind classifies why a line failed.
type ErrorKind uint8

const (
	// ErrSyntax: the line is not one well-formed JSON value. Message matches
	// encoding/json's SyntaxError text for the same input.
	ErrSyntax ErrorKind = iota + 1
	// ErrUTF8: the line contains malformed UTF-8 text.
	ErrUTF8
	// ErrDepth: the line nests deeper than the configured limit.
	ErrDepth
	// ErrNumberRange: a decoded number is out of range for its target type.
	ErrNumberRange
	// ErrTypeConflict: the same column received incompatible types and no
	// unifying rule (string, null-fill) applied.
	ErrTypeConflict
)

func (k ErrorKind) String() string {
	switch k {
	case ErrSyntax:
		return "syntax"
	case ErrUTF8:
		return "utf8"
	case ErrDepth:
		return "depth"
	case ErrNumberRange:
		return "number-range"
	case ErrTypeConflict:
		return "type-conflict"
	}
	return "unknown"
}

// Diag describes a failure at a specific position inside one line.
type Diag struct {
	Kind ErrorKind
	// Byte is the 0-based offset within the line of the offending byte. For a
	// line that ends before the value is complete it is len(line)-1 (or 0 for
	// an empty line), since the failure is attributed to the last byte read.
	Byte int
	// Col is the 1-based byte column, matching encoding/json's
	// SyntaxError.Offset convention for the same line (0 for an empty line).
	Col int
	Msg string
}

// diagAt builds a diagnostic for the byte at 0-based offset i.
func diagAt(kind ErrorKind, i int, msg string) Diag {
	return Diag{Kind: kind, Byte: i, Col: i + 1, Msg: msg}
}

// diagEOF builds a diagnostic for a line that ended before completion. Like
// encoding/json, the reported column is the number of bytes read (0 for an
// empty line).
func diagEOF(msg string) Diag {
	return Diag{Kind: ErrSyntax, Byte: 0, Col: 0, Msg: msg}
}

// diagEOFAt builds an EOF diagnostic whose column is lineLen, with Byte set to
// the last byte read.
func diagEOFAt(lineLen int, msg string) Diag {
	byteOff := lineLen - 1
	if byteOff < 0 {
		byteOff = 0
	}
	return Diag{Kind: ErrSyntax, Byte: byteOff, Col: lineLen, Msg: msg}
}

func (d Diag) Error() string {
	return fmt.Sprintf("%s error at byte %d (column %d): %s", d.Kind, d.Byte, d.Col, d.Msg)
}

// LineError attaches a diagnostic to a block line number.
type LineError struct {
	Line int // 1-based line number within the block
	Diag Diag
}

func (e LineError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Diag.Error())
}
