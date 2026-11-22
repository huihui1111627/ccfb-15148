package bitejson

import "strconv"

// tokKind is the kind of a token produced by the line parser.
type tokKind uint8

const (
	tokObjectStart tokKind = iota
	tokObjectEnd
	tokArrayStart
	tokArrayEnd
	tokKey    // unquoted object member name, content [start,end)
	tokString // unquoted string value, content [start,end)
	tokNumber // raw number literal, [start,end)
	tokTrue
	tokFalse
	tokNull
)

// token is a structural unit of one line. start/end delimit the raw content
// (without the quotes, for strings) as line-relative offsets.
type token struct {
	kind  tokKind
	start int32
	end   int32
}

// parser validates one JSON line, walks its structural characters, and emits a
// token stream. Byte offsets in tokens and diagnostics are line-relative. It
// reads the line's bytes directly; the precomputed block masks (lineScan)
// accelerate whitespace skipping and string-body traversal.
type parser struct {
	ls    *lineScan
	toks  []token
	depth int
	max   int
	err   Diag
	fail  bool
	// strictEscapes enables reporting of malformed \u surrogate escapes, which
	// encoding/json silently turns into U+FFFD.
	strictEscapes bool
}

func (p *parser) setErr(kind ErrorKind, at int, msg string) {
	if p.fail {
		return
	}
	p.fail = true
	p.err = diagAt(kind, at, msg)
}

// setEOF records a failure caused by the line ending before the value was
// complete. encoding/json reports the number of bytes read as the offset.
func (p *parser) setEOF(msg string) {
	if p.fail {
		return
	}
	p.fail = true
	p.err = diagEOFAt(len(p.ls.data), msg)
}

// quoteChar formats one byte the way encoding/json does in its messages.
func quoteChar(c byte) string {
	if c == '\'' {
		return `'\''`
	}
	if c == '"' {
		return `'"'`
	}
	s := strconv.Quote(string(rune(c)))
	return "'" + s[1:len(s)-1] + "'"
}

func syntaxMsg(c byte, context string) string {
	return "invalid character " + quoteChar(c) + " " + context
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// skipString scans the string whose opening quote is at i and returns the
// offset one past the closing quote. The closing quote is found from the
// precomputed mask (which excludes escaped quotes); escapes are then checked
// only where a backslash occurs, so error positions match encoding/json.
func (p *parser) skipString(i int) (int, bool) {
	ls := p.ls
	end := ls.firstCloseQuote(i + 1)
	limit := end
	found := end >= 0
	if !found {
		limit = len(ls.data)
	}
	// The masks find the closing quote and every backslash and control byte;
	// walk only those positions, so long string bodies cost nothing per byte.
	j := i + 1
	for {
		j = ls.nextBslashOrCtrl(j, limit)
		if j < 0 {
			break
		}
		c := ls.data[j]
		if c < 0x20 {
			p.setErr(ErrSyntax, j, syntaxMsg(c, "in string literal"))
			return j, false
		}
		// c == '\\'
		if j+1 >= len(ls.data) {
			p.setEOF("invalid character ' ' in string escape code")
			return j, false
		}
		e := ls.data[j+1]
		switch e {
		case 'b', 'f', 'n', 'r', 't', '\\', '/', '"':
			j += 2
			continue
		case 'u':
			for k := 2; k < 6; k++ {
				if j+k >= len(ls.data) {
					p.setEOF(`invalid character ' ' in \u hexadecimal character escape`)
					return j, false
				}
				if !isHex(ls.data[j+k]) {
					p.setErr(ErrSyntax, j+k, syntaxMsg(ls.data[j+k], `in \u hexadecimal character escape`))
					return j, false
				}
			}
			if p.strictEscapes {
				adv, at, msg, ok := checkSurrogateEscape(ls.data, j)
				if !ok {
					p.setErr(ErrSyntax, at, msg)
					return j, false
				}
				j += adv
				continue
			}
			j += 6
			continue
		default:
			p.setErr(ErrSyntax, j+1, syntaxMsg(e, "in string escape code"))
			return j, false
		}
	}
	if !found {
		p.setEOF("unexpected end of JSON input")
		return len(ls.data), false
	}
	return end + 1, true
}

// number validates the number literal at i and returns one past it.
func (p *parser) number(i int) (int, bool) {
	ls := p.ls
	j := i
	if ls.at(j) == '-' {
		j++
	}
	switch {
	case ls.at(j) == '0':
		j++
	case '1' <= ls.at(j) && ls.at(j) <= '9':
		for isDigit(ls.at(j)) {
			j++
		}
	default:
		if j >= len(ls.data) {
			p.setEOF("invalid character ' ' in numeric literal")
		} else {
			p.setErr(ErrSyntax, j, syntaxMsg(ls.data[j], "in numeric literal"))
		}
		return j, false
	}
	if ls.at(j) == '.' {
		j++
		if !isDigit(ls.at(j)) {
			if j >= len(ls.data) {
				p.setEOF("invalid character ' ' after decimal point in numeric literal")
			} else {
				p.setErr(ErrSyntax, j, syntaxMsg(ls.data[j], "after decimal point in numeric literal"))
			}
			return j, false
		}
		for isDigit(ls.at(j)) {
			j++
		}
	}
	if c := ls.at(j); c == 'e' || c == 'E' {
		j++
		if c := ls.at(j); c == '+' || c == '-' {
			j++
		}
		if !isDigit(ls.at(j)) {
			if j >= len(ls.data) {
				p.setEOF("invalid character ' ' in exponent of numeric literal")
			} else {
				p.setErr(ErrSyntax, j, syntaxMsg(ls.data[j], "in exponent of numeric literal"))
			}
			return j, false
		}
		for isDigit(ls.at(j)) {
			j++
		}
	}
	return j, true
}

// literal validates true/false/null at i.
func (p *parser) literal(i int, word string) (int, bool) {
	for k := 1; k < len(word); k++ {
		if i+k >= len(p.ls.data) {
			p.setEOF("invalid character ' ' in literal " + word + " (expecting '" + string(word[k]) + "')")
			return i + k, false
		}
		if p.ls.data[i+k] != word[k] {
			ctx := "in literal " + word + " (expecting '" + string(word[k]) + "')"
			p.setErr(ErrSyntax, i+k, syntaxMsg(p.ls.data[i+k], ctx))
			return i + k, false
		}
	}
	return i + len(word), true
}

func (p *parser) emit(kind tokKind, start, end int) {
	p.toks = append(p.toks, token{kind: kind, start: int32(start), end: int32(end)})
}

// skipSpace advances past ' ', '\t' and '\r' using the block whitespace mask.
func (p *parser) skipSpace(i int) int {
	ls := p.ls
	n := len(ls.data)
	for i < n && ls.bit(ls.bm.ws, i) {
		i = ls.nextNonWS(i, n)
	}
	return i
}

// validate parses the line as exactly one JSON value with optional whitespace.
func (p *parser) validate() bool {
	i := p.skipSpace(0)
	if i >= len(p.ls.data) {
		p.setEOF("unexpected end of JSON input")
		return false
	}
	end, ok := p.value(i)
	if !ok {
		return false
	}
	j := p.skipSpace(end)
	if j < len(p.ls.data) {
		p.setErr(ErrSyntax, j, syntaxMsg(p.ls.data[j], "after top-level value"))
		return false
	}
	return true
}

// value parses one value at i and returns one past it.
func (p *parser) value(i int) (int, bool) {
	switch p.ls.at(i) {
	case '{':
		return p.object(i)
	case '[':
		return p.array(i)
	default:
		return p.scalar(i)
	}
}

func (p *parser) scalar(i int) (int, bool) {
	ls := p.ls
	c := ls.at(i)
	switch {
	case c == '"':
		end, ok := p.skipString(i)
		if !ok {
			return end, false
		}
		p.emit(tokString, i+1, end-1)
		return end, true
	case c == 't':
		end, ok := p.literal(i, "true")
		if ok {
			p.emit(tokTrue, i, end)
		}
		return end, ok
	case c == 'f':
		end, ok := p.literal(i, "false")
		if ok {
			p.emit(tokFalse, i, end)
		}
		return end, ok
	case c == 'n':
		end, ok := p.literal(i, "null")
		if ok {
			p.emit(tokNull, i, end)
		}
		return end, ok
	case c == '-' || isDigit(c):
		end, ok := p.number(i)
		if ok {
			p.emit(tokNumber, i, end)
		}
		return end, ok
	}
	p.setErr(ErrSyntax, i, syntaxMsg(c, "looking for beginning of value"))
	return i, false
}

func (p *parser) key(i int) (int, bool) {
	ls := p.ls
	if ls.at(i) != '"' {
		if i >= len(ls.data) {
			p.setEOF("unexpected end of JSON input")
			return i, false
		}
		p.setErr(ErrSyntax, i, syntaxMsg(ls.data[i], "looking for beginning of object key string"))
		return i, false
	}
	end, ok := p.skipString(i)
	if !ok {
		return end, false
	}
	p.emit(tokKey, i+1, end-1)
	return end, true
}

func (p *parser) object(i int) (int, bool) {
	p.depth++
	if p.depth > p.max {
		p.setErr(ErrDepth, i, "exceeded max depth")
		return i, false
	}
	p.emit(tokObjectStart, i, i+1)
	ls := p.ls
	i = p.skipSpace(i + 1)
	if i < len(ls.data) && ls.data[i] == '}' {
		p.depth--
		p.emit(tokObjectEnd, i, i+1)
		return i + 1, true
	}
	for {
		i = p.skipSpace(i)
		end, ok := p.key(i)
		if !ok {
			return end, false
		}
		i = p.skipSpace(end)
		if i >= len(ls.data) {
			p.setEOF("unexpected end of JSON input")
			return i, false
		}
		if ls.data[i] != ':' {
			p.setErr(ErrSyntax, i, syntaxMsg(ls.data[i], "after object key"))
			return i, false
		}
		i = p.skipSpace(i + 1)
		if i >= len(ls.data) {
			p.setEOF("unexpected end of JSON input")
			return i, false
		}
		end, ok = p.value(i)
		if !ok {
			return end, false
		}
		i = p.skipSpace(end)
		if i >= len(ls.data) {
			p.setEOF("unexpected end of JSON input")
			return i, false
		}
		switch ls.data[i] {
		case ',':
			i = p.skipSpace(i + 1)
			continue
		case '}':
			p.depth--
			p.emit(tokObjectEnd, i, i+1)
			return i + 1, true
		default:
			p.setErr(ErrSyntax, i, syntaxMsg(ls.data[i], "after object key:value pair"))
			return i, false
		}
	}
}

func (p *parser) array(i int) (int, bool) {
	p.depth++
	if p.depth > p.max {
		p.setErr(ErrDepth, i, "exceeded max depth")
		return i, false
	}
	p.emit(tokArrayStart, i, i+1)
	ls := p.ls
	i = p.skipSpace(i + 1)
	if i < len(ls.data) && ls.data[i] == ']' {
		p.depth--
		p.emit(tokArrayEnd, i, i+1)
		return i + 1, true
	}
	for {
		i = p.skipSpace(i)
		if i >= len(ls.data) {
			p.setEOF("unexpected end of JSON input")
			return i, false
		}
		end, ok := p.value(i)
		if !ok {
			return end, false
		}
		i = p.skipSpace(end)
		if i >= len(ls.data) {
			p.setEOF("unexpected end of JSON input")
			return i, false
		}
		switch ls.data[i] {
		case ',':
			i = p.skipSpace(i + 1)
			continue
		case ']':
			p.depth--
			p.emit(tokArrayEnd, i, i+1)
			return i + 1, true
		default:
			p.setErr(ErrSyntax, i, syntaxMsg(ls.data[i], "after array element"))
			return i, false
		}
	}
}

// reset prepares the parser to reuse its token buffer for a new line.
func (p *parser) reset(ls *lineScan, max int) {
	p.ls = ls
	p.depth = 0
	p.max = max
	p.fail = false
	p.err = Diag{}
	p.toks = p.toks[:0]
}

// checkSurrogateEscape validates the \u escape at offset j. It returns the
// number of bytes the escape occupies (6 for a lone escape, 12 for a valid
// surrogate pair) or, when the pairing is broken, the offending offset and a
// message.
func checkSurrogateEscape(data []byte, j int) (adv, at int, msg string, ok bool) {
	r := hex4(data[j+2 : j+6])
	switch {
	case r >= 0xd800 && r <= 0xdbff:
		// High surrogate: require an immediately following \uXXXX low surrogate.
		if j+11 >= len(data) || data[j+6] != '\\' || data[j+7] != 'u' {
			return 0, j, "unpaired high surrogate in \\u escape", false
		}
		r2 := hex4(data[j+8 : j+12])
		if r2 < 0xdc00 || r2 > 0xdfff {
			return 0, j, "invalid low surrogate after high surrogate", false
		}
		return 12, 0, "", true
	case r >= 0xdc00 && r <= 0xdfff:
		return 0, j, "unpaired low surrogate in \\u escape", false
	}
	return 6, 0, "", true
}
