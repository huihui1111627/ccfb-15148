package bitejson

import (
	"math/rand"
	"testing"
)

// refClassify is a deliberately naive per-byte reference. Conventions:
//   - esc[i]: byte i is consumed by an unescaped backslash at i-1.
//   - inStr[i]: the scanner is inside a string *before* consuming byte i, so
//     closing quotes are marked inStr but opening quotes are not.
//   - openQ/closeQ mark the quote bytes themselves.
func refClassify(data []byte) (esc, inStr, openQ, closeQ []bool) {
	esc = make([]bool, len(data))
	inStr = make([]bool, len(data))
	openQ = make([]bool, len(data))
	closeQ = make([]bool, len(data))
	inside := false
	prevUnescBslash := false
	for i, b := range data {
		if prevUnescBslash {
			esc[i] = true
		}
		inStr[i] = inside
		prevUnescBslash = false
		if esc[i] {
			continue
		}
		if b == '\\' {
			prevUnescBslash = true
			continue
		}
		if inside {
			if b == '"' {
				closeQ[i] = true
				inside = false
			}
			continue
		}
		if b == '"' {
			openQ[i] = true
			inside = true
		}
	}
	return
}

// scanClassify runs the slab pipeline over a buffer and returns per-byte masks
// for comparison with refClassify.
func scanClassify(data []byte) (esc, inStr, openQ, closeQ []bool) {
	n := len(data)
	esc = make([]bool, n)
	inStr = make([]bool, n)
	openQ = make([]bool, n)
	closeQ = make([]bool, n)
	var sc scanner
	for off := 0; off < n; off += 64 {
		var buf [64]byte
		copy(buf[:], data[off:])
		sl := sc.scanSlab(buf[:])
		for k := 0; k < 64 && off+k < n; k++ {
			bit := uint64(1) << uint(k)
			esc[off+k] = sl.esc&bit != 0
			inStr[off+k] = sl.inStr&bit != 0
			openQ[off+k] = sl.openQ&bit != 0
			closeQ[off+k] = sl.closeQ&bit != 0
		}
	}
	return
}

func TestClassifyAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabets := [][]byte{
		[]byte(`{"a":1,"b":"x\ny"}` + "\n"),
		[]byte(`\`),
		{'"', '\\'},
		[]byte(`{}[]:,`),
		{0x00, 0x1f, 0x20, 0x7f, 0x80, 0xff, '"', '\\'},
	}
	for trial := 0; trial < 5000; trial++ {
		n := rng.Intn(400)
		buf := make([]byte, n)
		alpha := alphabets[rng.Intn(len(alphabets))]
		mode := rng.Intn(3)
		for i := range buf {
			switch mode {
			case 0:
				buf[i] = byte(rng.Intn(256))
			case 1:
				buf[i] = alpha[rng.Intn(len(alpha))]
			default:
				buf[i] = byte(rng.Intn(6)) + 0x20
			}
		}
		for k := 0; k < 3 && n > 0; k++ {
			p := rng.Intn(n)
			for j := p; j < n && j < p+130 && rng.Intn(8) != 0; j++ {
				buf[j] = '\\'
			}
		}
		we, wi, wo, wc := refClassify(buf)
		ge, gi, go_, gc := scanClassify(buf)
		for i := range buf {
			if we[i] != ge[i] || wi[i] != gi[i] || wo[i] != go_[i] || wc[i] != gc[i] {
				t.Fatalf("trial %d byte %d (%q) mismatch: esc %v/%v inStr %v/%v open %v/%v close %v/%v\nbuf=%q",
					trial, i, buf[i], we[i], ge[i], wi[i], gi[i], wo[i], go_[i], wc[i], gc[i], buf)
			}
		}
	}
}
