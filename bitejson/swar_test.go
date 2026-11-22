package bitejson

import (
	"math/rand"
	"testing"
)

func TestSWARComparisons(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	words := []uint64{0, ^uint64(0), loBits, hiBits, oneByte * 0x20}
	for i := 0; i < 200; i++ {
		words = append(words, rng.Uint64(), rng.Uint64()&loBits, rng.Uint64()|hiBits, rng.Uint64()&oneByte)
	}
	for _, w := range words {
		for c := 0; c < 256; c++ {
			var wantEq, wantLt, wantLe uint64
			for i := 0; i < 8; i++ {
				b := byte(w >> (8 * uint(i)))
				if b == byte(c) {
					wantEq |= 1 << uint(i)
				}
				if b < byte(c) {
					wantLt |= 1 << uint(i)
				}
				if b <= byte(c) {
					wantLe |= 1 << uint(i)
				}
			}
			if got := compress(byteEq(w, byte(c))); got != wantEq {
				t.Fatalf("byteEq w=%016x c=%d got=%x want=%x", w, c, got, wantEq)
			}
			if got := compress(byteLt(w, byte(c))); got != wantLt {
				t.Fatalf("byteLt w=%016x c=%d got=%x want=%x", w, c, got, wantLt)
			}
			if got := compress(byteLe(w, byte(c))); got != wantLe {
				t.Fatalf("byteLe w=%016x c=%d got=%x want=%x", w, c, got, wantLe)
			}
		}
	}
}

func TestCompress(t *testing.T) {
	for i := 0; i < 8; i++ {
		if got := compress(uint64(0x80) << uint(8*i)); got != 1<<uint(i) {
			t.Fatalf("compress bit %d = %x", i, got)
		}
	}
	if got := compress(0x8000400000000000); got != 0x80 {
		t.Fatalf("compress high byte = %x", got)
	}
	if got := compress(0x0000000000000080 | 0x0000000000008000); got != 0x03 {
		t.Fatalf("compress low bytes = %x", got)
	}
}
