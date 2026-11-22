package bitejson

import "testing"

// reference escape resolution over 8 bytes with a one-bit carry in/out.
func refEscapes(bs, prev uint64) (esc, next uint64) {
	prevUnesc := prev == 1
	for i := 0; i < 8; i++ {
		if prevUnesc {
			esc |= 1 << uint(i)
			prevUnesc = false
			continue
		}
		if bs&(1<<uint(i)) != 0 {
			prevUnesc = true
		}
	}
	if prevUnesc {
		next = 1
	}
	return
}

func TestResolveEscapesExhaustiveWord(t *testing.T) {
	for p := 0; p < 256; p++ {
		for prev := uint64(0); prev <= 1; prev++ {
			got, gn := resolveEscapes(uint64(p), prev)
			want, wn := refEscapes(uint64(p), prev)
			if got != want || gn != wn {
				t.Fatalf("p=%08b prev=%d got=%08b/%d want=%08b/%d", p, prev, got, gn, want, wn)
			}
		}
	}
}
