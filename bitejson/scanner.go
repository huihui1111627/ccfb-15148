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

// scanSlab classifies 64 bytes at b. It is the vectorised outer loop: eight
// independent SWAR classifications feed the escape and string carries in
// sequence, and the per-word masks are stitched into one 64-bit slab bitmap.
func (s *scanner) scanSlab(b []byte) (sl slab) {
	var (
		quote  uint64
		bslash uint64
		open   uint64
		close  uint64
		colon  uint64
		comma  uint64
		ctrl   uint64
		ws     uint64
		nl     uint64
		esc    uint64
		inStr  uint64
		openQ  uint64
		closeQ uint64
		nonAsc uint64
	)
	prevEsc := s.prevEscaped
	prevIn := s.prevInStr
	savedEsc, savedIn := prevEsc, prevIn
	for k := 0; k < 8; k++ {
		w := loadWord(b[k*8:])
		c := classifyWord(w)
		e, nextEsc := resolveEscapes(c.bslash, prevEsc)
		q := c.quote &^ e
		is, oq, cq, nextIn := resolveStrings(q, prevIn)

		quote |= c.quote << (k * 8)
		bslash |= c.bslash << (k * 8)
		open |= c.open << (k * 8)
		close |= c.close << (k * 8)
		colon |= c.colon << (k * 8)
		comma |= c.comma << (k * 8)
		ctrl |= c.ctrl << (k * 8)
		ws |= compress(c.wsHi) << (k * 8)
		nl |= c.nl << (k * 8)
		esc |= e << (k * 8)
		inStr |= is << (k * 8)
		openQ |= oq << (k * 8)
		closeQ |= cq << (k * 8)
		nonAsc |= compress(c.nonAscHi) << (k * 8)

		sl.nStr += popcount(oq)
		prevEsc, prevIn = nextEsc, nextIn
	}
	if nl != 0 {
		// The slab contained a line boundary. The vectorised per-word carry
		// cannot reset mid-word, so redo this slab scalar with the carries from
		// before it. This happens once per line.
		s.prevEscaped, s.prevInStr = savedEsc, savedIn
		return s.scanSlabScalar(b)
	}
	s.prevEscaped, s.prevInStr = prevEsc, prevIn
	sl.quote = quote
	sl.bslash = bslash
	sl.open = open
	sl.close = close
	sl.colon = colon
	sl.comma = comma
	sl.ctrl = ctrl
	sl.ws = ws
	sl.nl = nl
	sl.esc = esc
	sl.inStr = inStr
	sl.openQ = openQ
	sl.closeQ = closeQ
	sl.nonAsc = nonAsc
	return
}

// scanSlabScalar classifies 64 bytes byte by byte. It is used for slabs that
// contain a line boundary, where the escape and string carries must reset at
// the newline; a correct scalar pass is simpler than splitting the vector
// carries around a mid-word reset.
func (s *scanner) scanSlabScalar(b []byte) (sl slab) {
	// p is the 64-byte padded slab; s.scanBlock masks off padding afterwards.
	prevEsc := s.prevEscaped
	prevIn := s.prevInStr
	for j := 0; j < 64; j++ {
		bb := b[j]
		if bb == '\n' {
			prevEsc, prevIn = 0, 0
		}
		bit := uint64(1) << uint(j)
		if prevEsc&1 != 0 {
			// This byte is consumed by the previous backslash. It is escaped
			// even if it is a quote, and it is part of the string body. The
			// quote mask records all quote bytes, escaped ones included.
			sl.esc |= bit
			prevEsc = 0
			if bb == '"' {
				sl.quote |= bit
			}
			if bb == '\\' {
				sl.bslash |= bit
			}
			if prevIn&1 != 0 {
				sl.inStr |= bit
			}
			continue
		}
		if bb == '"' {
			sl.quote |= bit
			if prevIn&1 != 0 {
				sl.inStr |= bit
				sl.closeQ |= bit
				prevIn = 0
			} else {
				sl.openQ |= bit
				sl.nStr++
				prevIn = 1
			}
			continue
		}
		if prevIn&1 != 0 {
			sl.inStr |= bit
		}
		switch bb {
		case '\\':
			sl.bslash |= bit
			prevEsc = 1
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
	s.prevEscaped, s.prevInStr = prevEsc, prevIn
	return
}
