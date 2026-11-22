// Package bitejson implements a bit-parallel JSON scanner and column extractor.
//
// The scanner never classifies bytes one at a time. For each 64-byte group it
// computes, with SWAR arithmetic, the byte-mask of every structural character
// class at once (quotes, backslashes, braces, brackets, colon, comma), derives
// which bytes are escaped and which bytes live inside strings purely with
// carry/borrow propagation, and then a second, sparse pass walks only the
// structural positions to build the per-line value spans. Queries name a set
// of JSON paths; only those values are decoded into typed columns.
//
// Everything is built so that the result of processing a block of input does
// not depend on where the block boundaries fall: all carry state (escape
// parity from a trailing backslash run) is threaded across groups.
package bitejson

import "math/bits"

const (
	hiBits   = 0x8080808080808080
	loBits   = 0x7f7f7f7f7f7f7f7f
	oneByte  = 0x0101010101010101
	evenBits = 0x5555555555555555
)

// byteEq returns a mask with the high bit set in every byte of w that equals c.
// It uses the exact zero-byte detector; the classic (x-1)&^x form is avoided
// because it also fires on 0x01 bytes following a match.
func byteEq(w uint64, c byte) uint64 {
	x := w ^ (oneByte * uint64(c))
	return ^(((x & loBits) + loBits) | x | loBits)
}

// byteLt returns a mask with the high bit set in every byte of w that is
// strictly less than c (unsigned comparison, no cross-byte borrow).
func byteLt(w uint64, c byte) uint64 {
	b := oneByte * uint64(c)
	t := (w | hiBits) - (b & loBits)
	wh, bh := w&hiBits, b&hiBits
	return (bh&^wh | ^(wh^bh)&^t) & hiBits
}

// byteLe returns a mask with the high bit set in every byte <= c.
func byteLe(w uint64, c byte) uint64 {
	if c == 0xff {
		return hiBits
	}
	return byteLt(w, c+1)
}

func byteGt(w uint64, c byte) uint64 { return byteLt(^w, ^c) }

// compress maps the high bit of each byte in m to one bit of a 8-bit result,
// byte 0 becoming bit 0. It is a movemask for SWAR vectors.
func compress(m uint64) uint64 {
	return ((m & hiBits) * 0x0002040810204081) >> 56
}

// carryOut reports the highest bit of m as a 0/1 value.
func carryOut(m uint64) uint64 { return m >> 63 }

func popcount(x uint64) int { return bits.OnesCount64(x) }

// trailing returns the index of the lowest set bit; x must be non-zero.
func trailing(x uint64) int { return bits.TrailingZeros64(x) }

// lowClear clears the lowest set bit.
func lowClear(x uint64) uint64 { return x & (x - 1) }
