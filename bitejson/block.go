package bitejson

// blockMasks holds the per-class bitmaps of a whole block, one uint64 per
// 64-byte slab. Bit i of slab s describes byte s*64+i.
type blockMasks struct {
	quote   []uint64
	bslash  []uint64
	escaped []uint64
	openQ   []uint64
	closeQ  []uint64
	inStr   []uint64
	ws      []uint64
	nl      []uint64 // '\n'
	ctrl    []uint64 // b < 0x20
	nonAsc  []uint64 // b >= 0x80
	open    []uint64 // '{' or '['
	close   []uint64 // '}' or ']'
	colon   []uint64
	comma   []uint64
	n       int // block length in bytes
}

func (bm *blockMasks) slabs(n int) {
	k := (n + 63) / 64
	bm.quote = grow(bm.quote, k)
	bm.bslash = grow(bm.bslash, k)
	bm.escaped = grow(bm.escaped, k)
	bm.openQ = grow(bm.openQ, k)
	bm.closeQ = grow(bm.closeQ, k)
	bm.inStr = grow(bm.inStr, k)
	bm.ws = grow(bm.ws, k)
	bm.nl = grow(bm.nl, k)
	bm.ctrl = grow(bm.ctrl, k)
	bm.nonAsc = grow(bm.nonAsc, k)
	bm.open = grow(bm.open, k)
	bm.close = grow(bm.close, k)
	bm.colon = grow(bm.colon, k)
	bm.comma = grow(bm.comma, k)
	bm.n = n
}

func grow(b []uint64, k int) []uint64 {
	if cap(b) < k {
		return make([]uint64, k)
	}
	b = b[:k]
	for i := range b {
		b[i] = 0
	}
	return b
}

func (bm *blockMasks) bit(arr []uint64, i int) bool {
	return arr[i>>6]&(1<<uint(i&63)) != 0
}

// scanBlock classifies an entire block. Carries flow between slabs, so the
// masks are identical no matter how the input was split before this call: the
// scanner's carry state (escape parity into the first byte, string state) is
// part of the scanner and every byte is scanned exactly once.
func (s *scanner) scanBlock(data []byte, bm *blockMasks) {
	n := len(data)
	bm.slabs(n)
	for off := 0; off < n; off += 64 {
		var sl slab
		if off+64 <= n {
			sl = s.scanSlab(data[off : off+64])
		} else {
			var buf [64]byte
			copy(buf[:], data[off:])
			sl = s.scanSlab(buf[:])
		}
		k := off / 64
		mask := ^uint64(0)
		if rem := n - off; rem < 64 {
			// Bytes past the end of the block are padding; do not let them
			// leak into the masks.
			mask = (uint64(1) << uint(rem)) - 1
		}
		bm.quote[k] = sl.quote & mask
		bm.bslash[k] = sl.bslash & mask
		bm.escaped[k] = sl.esc & mask
		bm.openQ[k] = sl.openQ & mask
		bm.closeQ[k] = sl.closeQ & mask
		bm.inStr[k] = sl.inStr & mask
		bm.ws[k] = sl.ws & mask
		bm.nl[k] = sl.nl & mask
		bm.ctrl[k] = sl.ctrl & mask
		bm.nonAsc[k] = sl.nonAsc & mask
		bm.open[k] = sl.open & mask
		bm.close[k] = sl.close & mask
		bm.colon[k] = sl.colon & mask
		bm.comma[k] = sl.comma & mask
	}
}

// lineScan gives a parser access to one line's bytes and to the block-wide
// masks, translating line-relative offsets to block offsets.
type lineScan struct {
	data []byte // exactly the line bytes, without the terminating newline
	base int    // offset of the line within the block
	bm   *blockMasks
}

func (ls *lineScan) bit(arr []uint64, i int) bool { return ls.bm.bit(arr, ls.base+i) }

// at returns line byte i, or 0 past the end. Validity checks must test the
// offset themselves: 0 is also a legitimate byte value.
func (ls *lineScan) at(i int) byte {
	if i < len(ls.data) {
		return ls.data[i]
	}
	return 0
}

// firstCloseQuote returns the line-relative offset of the first closing quote
// at or after i, or -1 if there is none.
func (ls *lineScan) firstCloseQuote(i int) int {
	if i >= len(ls.data) {
		return -1
	}
	j := ls.base + i
	s := j >> 6
	m := ls.bm.closeQ[s] >> uint(j&63)
	for {
		if m != 0 {
			p := j + trailing(m)
			if p-ls.base < len(ls.data) {
				return p - ls.base
			}
			return -1
		}
		s++
		if s >= len(ls.bm.closeQ) {
			return -1
		}
		j = s * 64
		m = ls.bm.closeQ[s]
	}
}

// nextNonWS returns the first line-relative offset >= i whose byte is not
// whitespace, scanning whole words of the whitespace mask.
func (ls *lineScan) nextNonWS(i, n int) int {
	j := ls.base + i
	s := j >> 6
	m := ^ls.bm.ws[s] >> uint(j&63)
	for {
		if m != 0 {
			p := j + trailing(m)
			if p-ls.base >= n {
				return n
			}
			return p - ls.base
		}
		s++
		j = s * 64
		if s >= len(ls.bm.ws) {
			return n
		}
		m = ^ls.bm.ws[s]
	}
}

// nextBslashOrCtrl returns the line-relative offset of the first byte at or
// after i that is a backslash or a control character, or -1 if none occurs
// before limit.
func (ls *lineScan) nextBslashOrCtrl(i, limit int) int {
	if i >= limit {
		return -1
	}
	j := ls.base + i
	s := j >> 6
	m := (ls.bm.bslash[s] | ls.bm.ctrl[s]) >> uint(j&63)
	for {
		if m != 0 {
			p := j + trailing(m)
			if p-ls.base >= limit {
				return -1
			}
			return p - ls.base
		}
		s++
		j = s * 64
		if s >= len(ls.bm.bslash) {
			return -1
		}
		m = ls.bm.bslash[s] | ls.bm.ctrl[s]
	}
}
