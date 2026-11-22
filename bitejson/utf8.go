package bitejson

// utfErrKind distinguishes the two malformed-text failures the engine reports.
type utfErrKind uint8

const (
	utfOK utfErrKind = iota
	utfInvalidByte
	utfTruncated
)

// checkUTF8 validates data and returns the byte offset of the first problem.
// It skips runs of ASCII eight bytes at a time and decodes the rest rune by
// rune, so ordinary log lines cost one word test per eight bytes. Overlong
// encodings, surrogate code points and values above U+10FFFF are reported as
// invalid at the first byte of the offending sequence; a sequence cut short by
// the end of the data is reported as truncated at the same position.
func checkUTF8(data []byte) (pos int, kind utfErrKind) {
	i, n := 0, len(data)
	for i < n {
		// Fast path: a whole word of ASCII.
		for i+8 <= n {
			if loadWord(data[i:])&hiBits != 0 {
				break
			}
			i += 8
		}
		if i >= n {
			return 0, utfOK
		}
		b := data[i]
		if b < 0x80 {
			i++
			continue
		}
		var need int
		var min rune
		var cp rune
		switch {
		case b&0xe0 == 0xc0:
			need, cp, min = 1, rune(b&0x1f), 0x80
		case b&0xf0 == 0xe0:
			need, cp, min = 2, rune(b&0x0f), 0x800
		case b&0xf8 == 0xf0:
			need, cp, min = 3, rune(b&0x07), 0x10000
		default:
			return i, utfInvalidByte
		}
		if i+need >= n {
			return i, utfTruncated
		}
		for k := 1; k <= need; k++ {
			c := data[i+k]
			if c&0xc0 != 0x80 {
				return i, utfInvalidByte
			}
			cp = cp<<6 | rune(c&0x3f)
		}
		if cp < min || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff) {
			return i, utfInvalidByte
		}
		i += need + 1
	}
	return 0, utfOK
}

func (k utfErrKind) message() string {
	if k == utfTruncated {
		return "truncated UTF-8 sequence"
	}
	return "invalid UTF-8 byte sequence"
}
