package bitejson

// scanner turns a byte block into a sequence of slabs, threading the escape
// and in-string carries across word and block boundaries so that splitting the
// input at arbitrary points never changes the result.
type scanner struct {
	prevEscaped uint64 // bit 0 of next word is escaped
	prevInStr   uint64 // bit 0 of next word starts inside a string
}

func (s *scanner) reset() {
	s.prevEscaped = 0
	s.prevInStr = 0
}

// scanSlab classifies 64 bytes at b. Each of the eight words is classified with
// the SWAR path unless it contains a newline; a raw newline resets the carries
// mid-word, so the (rare) word containing one is classified byte by byte. This
// keeps the vectorised path in use for the bulk of a slab even when lines are
// shorter than a word.
func (s *scanner) scanSlab(b []byte) (sl slab) {
	prevEsc := s.prevEscaped
	prevIn := s.prevInStr
	for k := 0; k < 8; k++ {
		w := loadWord(b[k*8:])
		if byteEq(w, '\n') != 0 {
			s.scanWordScalar(b[k*8:k*8+8], &sl, k, &prevEsc, &prevIn)
			continue
		}
		c := classifyWord(w)
		e, nextEsc := resolveEscapes(c.bslash, prevEsc)
		q := c.quote &^ e
		is, oq, cq, nextIn := resolveStrings(q, prevIn)
		sh := uint(k * 8)
		sl.quote |= c.quote << sh
		sl.bslash |= c.bslash << sh
		sl.open |= c.open << sh
		sl.close |= c.close << sh
		sl.colon |= c.colon << sh
		sl.comma |= c.comma << sh
		sl.ctrl |= c.ctrl << sh
		sl.ws |= compress(c.wsHi) << sh
		sl.nl |= c.nl << sh
		sl.esc |= e << sh
		sl.inStr |= is << sh
		sl.openQ |= oq << sh
		sl.closeQ |= cq << sh
		sl.nonAsc |= compress(c.nonAscHi) << sh
		sl.nStr += popcount(oq)
		prevEsc, prevIn = nextEsc, nextIn
	}
	s.prevEscaped, s.prevInStr = prevEsc, prevIn
	return sl
}

// scanWordScalar classifies one eight-byte word byte by byte, resetting the
// carries at any newline. It accumulates into sl at word k.
func (s *scanner) scanWordScalar(b []byte, sl *slab, k int, prevEsc, prevIn *uint64) {
	for j := 0; j < 8; j++ {
		bb := b[j]
		if bb == '\n' {
			*prevEsc, *prevIn = 0, 0
		}
		bit := uint64(1) << uint(k*8+j)
		if *prevEsc&1 != 0 {
			// Consumed by the previous backslash. Recorded as escaped, still a
			// raw quote/backslash in the class masks, and inside the string body.
			sl.esc |= bit
			*prevEsc = 0
			if bb == '"' {
				sl.quote |= bit
			}
			if bb == '\\' {
				sl.bslash |= bit
			}
			if *prevIn&1 != 0 {
				sl.inStr |= bit
			}
			continue
		}
		if bb == '"' {
			sl.quote |= bit
			if *prevIn&1 != 0 {
				sl.inStr |= bit
				sl.closeQ |= bit
				*prevIn = 0
			} else {
				sl.openQ |= bit
				sl.nStr++
				*prevIn = 1
			}
			continue
		}
		if *prevIn&1 != 0 {
			sl.inStr |= bit
		}
		switch bb {
		case '\\':
			sl.bslash |= bit
			*prevEsc = 1
		case '{', '[':
			sl.open |= bit
		case '}', ']':
			sl.close |= bit
		case ':':
			sl.colon |= bit
		case ',':
			sl.comma |= bit
		case ' ', '\t', '\r':
			sl.ws |= bit
		case '\n':
			sl.nl |= bit
		}
		if bb < 0x20 {
			sl.ctrl |= bit
		}
		if bb >= 0x80 {
			sl.nonAsc |= bit
		}
	}
}
