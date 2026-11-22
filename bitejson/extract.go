package bitejson

// match is one value located by a path inside a line: its JSON kind and the
// line-relative byte span [start,end).
type match struct {
	kind  valueKind
	start int32
	end   int32
}

// rawSpan returns the byte span of the value's complete JSON text, including
// quotes for strings. Token spans for strings exclude the quotes because the
// decoders work on the body; wildcard reconstruction needs the full text.
func rawSpan(toks []token, pos int) (start, end int32) {
	t := toks[pos]
	switch t.kind {
	case tokString, tokKey:
		return t.start - 1, t.end + 1
	case tokObjectStart, tokArrayStart:
		s := t.start
		depth := 0
		for i := pos; i < len(toks); i++ {
			switch toks[i].kind {
			case tokObjectStart, tokArrayStart:
				depth++
			case tokObjectEnd, tokArrayEnd:
				depth--
				if depth == 0 {
					return s, toks[i].end
				}
			}
		}
		return s, t.end
	default:
		return t.start, t.end
	}
}

// valueEnd returns the token index just past the value starting at pos.
func valueEnd(toks []token, pos int) int {
	switch toks[pos].kind {
	case tokObjectStart, tokArrayStart:
		depth := 0
		for i := pos; i < len(toks); i++ {
			switch toks[i].kind {
			case tokObjectStart, tokArrayStart:
				depth++
			case tokObjectEnd, tokArrayEnd:
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(toks)
	default:
		return pos + 1
	}
}

// spanOf returns the byte span of the value whose token starts at pos. For
// containers the span covers the whole container, braces included.
func spanOf(toks []token, pos int) (start, end int32) {
	t := toks[pos]
	if t.kind != tokObjectStart && t.kind != tokArrayStart {
		return t.start, t.end
	}
	depth := 0
	for i := pos; i < len(toks); i++ {
		switch toks[i].kind {
		case tokObjectStart, tokArrayStart:
			depth++
		case tokObjectEnd, tokArrayEnd:
			depth--
			if depth == 0 {
				return t.start, toks[i].end
			}
		}
	}
	return t.start, t.end
}

// extractor evaluates a compiled path against one line's token stream.
type extractor struct {
	data    []byte
	scratch []byte
	// wildHit is set when a wildcard step actually iterated an array, even if
	// no element matched the rest of the path. This distinguishes "empty list"
	// from "path never reached an array".
	wildHit bool
}

// keyEquals reports whether the object key token equals name after unquoting.
func (x *extractor) keyEquals(k token, name string) bool {
	body := x.data[k.start:k.end]
	if !hasEscape(body) {
		return string(body) == name
	}
	x.scratch = unquoteString(x.data, int(k.start), int(k.end), x.scratch[:0])
	return string(x.scratch) == name
}

func hasEscape(b []byte) bool {
	for _, c := range b {
		if c == '\\' {
			return true
		}
	}
	return false
}

// eval appends every value matched by segs[si:] within the value at pos and
// returns the token index just past that value.
func (x *extractor) eval(toks []token, pos int, segs []segment, si int, out []match) ([]match, int) {
	t := toks[pos]
	if si == len(segs) {
		s, e := rawSpan(toks, pos)
		out = append(out, match{kind: kindOfToken(t.kind), start: s, end: e})
		return out, valueEnd(toks, pos)
	}
	seg := segs[si]
	switch t.kind {
	case tokObjectStart:
		p := pos + 1
		// encoding/json map semantics: the last occurrence of a duplicate key
		// wins. Collect the latest matching sub-result.
		var last []match
		for p < len(toks) && toks[p].kind != tokObjectEnd {
			key := toks[p]
			p++
			if seg.kind == segKey && x.keyEquals(key, seg.key) {
				var sub []match
				sub, p = x.eval(toks, p, segs, si+1, sub[:0])
				last = append(last[:0], sub...)
			} else {
				p = valueEnd(toks, p)
			}
		}
		out = append(out, last...)
		return out, p + 1
	case tokArrayStart:
		p := pos + 1
		idx := 0
		if seg.kind == segWild {
			x.wildHit = true
		}
		for p < len(toks) && toks[p].kind != tokArrayEnd {
			match := seg.kind == segWild || (seg.kind == segIndex && idx == seg.index)
			if match {
				out, p = x.eval(toks, p, segs, si+1, out)
			} else {
				p = valueEnd(toks, p)
			}
			idx++
		}
		return out, p + 1
	}
	return out, valueEnd(toks, pos)
}

// kindOfToken maps a token kind to the cell value kind.
func kindOfToken(k tokKind) valueKind {
	switch k {
	case tokObjectStart:
		return cellObject
	case tokArrayStart:
		return cellArray
	case tokString:
		return cellString
	case tokNumber:
		return cellNumber
	case tokTrue, tokFalse:
		return cellBool
	}
	return cellNull
}
