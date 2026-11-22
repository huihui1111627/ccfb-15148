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
		esc    uint64
		inStr  uint64
		openQ  uint64
		closeQ uint64
		nonAsc uint64
	)
	prevEsc := s.prevEscaped
	prevIn := s.prevInStr
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
		esc |= e << (k * 8)
		inStr |= is << (k * 8)
		openQ |= oq << (k * 8)
		closeQ |= cq << (k * 8)
		nonAsc |= compress(c.nonAscHi) << (k * 8)

		sl.nStr += popcount(oq)
		prevEsc, prevIn = nextEsc, nextIn
	}
	s.prevEscaped, s.prevInStr = prevEsc, prevIn
	sl.quote = quote
	sl.bslash = bslash
	sl.open = open
	sl.close = close
	sl.colon = colon
	sl.comma = comma
	sl.ctrl = ctrl
	sl.esc = esc
	sl.inStr = inStr
	sl.openQ = openQ
	sl.closeQ = closeQ
	sl.nonAsc = nonAsc
	return
}
