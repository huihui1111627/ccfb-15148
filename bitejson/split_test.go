package bitejson

import (
	"bytes"
	"testing"
)

// TestArbitrarySplitInvariance feeds the same corpus split at random points
// (including inside strings, escapes, backslash runs and multi-byte
// characters) and checks the results match a single-block run exactly.
func TestArbitrarySplitInvariance(t *testing.T) {
	corpus := GenerateCorpus(4242, 800)
	q := MustQuery("id", "name", "arr[*]", "nested.b.c", "unicode", "deep.very.deep", "value")
	ref, err := Run(bytes.NewReader(corpus), q, Options{BlockSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	rng := NewCorpusRand(77)
	for trial := 0; trial < 40; trial++ {
		e := NewEngine(q, Options{})
		out := &Table{Columns: make([]*Column, len(q.paths))}
		for ci, p := range q.paths {
			out.Columns[ci] = &Column{Path: p.raw, List: isWildcard(p)}
		}
		absorb := func(res *BlockResult) {
			if res == nil {
				return
			}
			out.Lines += res.Table.Lines
			out.Bad += res.Table.Bad
			out.Errors = append(out.Errors, res.Table.Errors...)
			for ci, col := range res.Table.Columns {
				out.Columns[ci].Values = append(out.Columns[ci].Values, col.Values...)
			}
		}
		for off := 0; off < len(corpus); {
			n := 1 + rng.pick(500)
			end := off + n
			if end > len(corpus) {
				end = len(corpus)
			}
			absorb(e.ProcessBlock(corpus[off:end]))
			off = end
		}
		absorb(e.Flush())
		out.Finalize(false)
		if !tablesEqual(ref, out) {
			t.Fatalf("trial %d: random split differs from single-block run", trial)
		}
	}
}
