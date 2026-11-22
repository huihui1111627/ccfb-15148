package bitejson

// A word is eight input bytes held in a uint64. Every SWAR comparison produces
// a mask whose bit i (0..7) describes byte i of the word. A slab is eight
// words, i.e. 64 input bytes, with the 64-bit per-class bitmaps the structural
// walk and the visualiser work on.

// wordClass holds the structural classification of one 8-byte word. All masks
// use the low eight bits.
type wordClass struct {
	quoteHi  uint64 // b == '"'
	bslashHi uint64 // b == '\\'
	braceHi  uint64 // b == '{' or '}'
	brackHi  uint64 // b == '[' or ']'
	colonHi  uint64 // b == ':'
	commaHi  uint64 // b == ','
	ctrlHi   uint64 // b < 0x20
	nonAscHi uint64 // b >= 0x80
	wsHi     uint64 // b is ' ', '\t' or '\r'
	nlHi     uint64 // b == '\n'

	quote  uint64
	bslash uint64
	open   uint64 // '{' or '['
	close  uint64 // '}' or ']'
	colon  uint64
	comma  uint64
	ctrl   uint64 // b < 0x20
	ws     uint64 // ' ', '\t', '\r'
	nl     uint64 // '\n'
}

// classifyWord fills the class masks for the eight bytes of w. It performs no
// cross-byte state handling; that is resolveEscapes / resolveStrings.
func classifyWord(w uint64) (c wordClass) {
	c.quoteHi = byteEq(w, '"')
	c.bslashHi = byteEq(w, '\\')
	c.braceHi = byteEq(w, '{') | byteEq(w, '}')
	c.brackHi = byteEq(w, '[') | byteEq(w, ']')
	c.colonHi = byteEq(w, ':')
	c.commaHi = byteEq(w, ',')
	c.ctrlHi = byteLt(w, 0x20)
	c.nonAscHi = w & hiBits
	c.wsHi = byteEq(w, ' ') | byteEq(w, '\t') | byteEq(w, '\r')
	c.nlHi = byteEq(w, '\n')

	c.quote = compress(c.quoteHi)
	c.bslash = compress(c.bslashHi)
	c.open = compress(byteEq(w, '{') | byteEq(w, '['))
	c.close = compress(byteEq(w, '}') | byteEq(w, ']'))
	c.colon = compress(c.colonHi)
	c.comma = compress(c.commaHi)
	c.ctrl = compress(c.ctrlHi)
	c.nl = compress(c.nlHi)
	return
}

// resolveEscapes turns the backslash bitmap of one word (low 8 bits) into the
// set of escaped byte positions and the one-bit carry into the next word, using
// the add-overflow that resolves runs of backslashes in parallel. Runs of odd
// length end in an unescaped backslash; runs of even length are wholly escaped.
//
// prevEscaped says bit 0 is escaped by the previous word. nextEscaped says bit
// 0 of the following word is escaped.
func resolveEscapes(bslash, prevEscaped uint64) (escaped, nextEscaped uint64) {
	// A backslash escaped by the previous word cannot itself start a run.
	bs := (bslash & 0xff) &^ (prevEscaped & 1)
	follows := bs<<1 | (prevEscaped & 1)
	oddStarts := bs &^ evenBits &^ follows
	sum := oddStarts + bs
	invert := sum << 1
	escaped = (evenBits ^ invert) & follows & 0xff
	// Carry out of the parallel addition: an unescaped backslash ends the word
	// in an odd position, so the next word's first byte is escaped.
	nextEscaped = (sum >> 8) & 1
	return
}

// resolveStrings computes the in-string bitmap (low 8 bits), the opening and
// closing quote masks and the one-bit carry for one word. quotes must have
// escaped quotes removed. inStrIn says bit 0 is inside a string (string state
// before consuming byte 0). Convention: closeQ bytes are also marked inStr,
// opening quotes are not.
func resolveStrings(quotes, inStrIn uint64) (inStr, openQ, closeQ, inStrOut uint64) {
	q := quotes & 0xff
	p := q
	p ^= p << 1
	p ^= p << 2
	p ^= p << 4
	inStr = ((p << 1) ^ (0 - (inStrIn & 1))) & 0xff
	closeQ = inStr & q
	openQ = ^inStr & q
	// State after consuming byte 7: parity of every quote in this word.
	inStrOut = (p>>7 ^ inStrIn) & 1
	return
}

// slab is one 64-byte group's masks after escape and string resolution.
type slab struct {
	quote  uint64 // all quote bytes (escaped quotes included)
	bslash uint64
	open   uint64
	close  uint64
	colon  uint64
	comma  uint64
	ctrl   uint64
	ws     uint64 // ' ', '\t', '\r'
	nl     uint64 // '\n'
	esc    uint64
	inStr  uint64
	openQ  uint64
	closeQ uint64
	nonAsc uint64
	nStr   int
}

// loadWord reads eight bytes; the caller guarantees len(b) >= 8.
func loadWord(b []byte) uint64 {
	_ = b[7]
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}
