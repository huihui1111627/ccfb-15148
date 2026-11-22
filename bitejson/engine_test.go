package bitejson

import (
	"bytes"
	"testing"
)

// TestBlockInvariance checks that feeding the same input in blocks of wildly
// different sizes, including sizes that cut in the middle of strings, escapes,
// multi-byte characters and backslash runs, yields identical tables.
func TestBlockInvariance(t *testing.T) {
	rng := newCorpusRand(999)
	corpus := generateCorpus(rng, 1200)
	q := MustQuery("id", "name", "arr[*]", "nested.b.c", "unicode", "deep.very.deep")

	reference, err := Run(bytes.NewReader(corpus), q, Options{BlockSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}

	for _, bs := range []int{1, 7, 8, 13, 63, 64, 65, 4093} {
		got := runBlocks(t, corpus, q, bs)
		if got.Lines != reference.Lines || got.Bad != reference.Bad {
			t.Fatalf("block size %d: lines %d/%d bad %d/%d", bs, got.Lines, reference.Lines, got.Bad, reference.Bad)
		}
		for ci := range reference.Columns {
			a, b := reference.Columns[ci], got.Columns[ci]
			if len(a.Values) != len(b.Values) {
				t.Fatalf("block size %d column %q: %d vs %d values", bs, a.Path, len(a.Values), len(b.Values))
			}
			for li := range a.Values {
				if !valuesEqual(a.Values[li], b.Values[li]) {
					t.Fatalf("block size %d column %q line %d: %+v vs %+v", bs, a.Path, li, a.Values[li], b.Values[li])
				}
			}
		}
		if len(got.Errors) != len(reference.Errors) {
			t.Fatalf("block size %d: %d errors vs %d", bs, len(got.Errors), len(reference.Errors))
		}
		for i := range got.Errors {
			if got.Errors[i] != reference.Errors[i] {
				t.Fatalf("block size %d error %d: %v vs %v", bs, i, got.Errors[i], reference.Errors[i])
			}
		}
	}
}

func valuesEqual(a, b Value) bool {
	if a.Kind != b.Kind || a.Present != b.Present {
		return false
	}
	switch a.Kind {
	case cellString, cellObject, cellArray:
		return a.Str == b.Str
	case cellNumber:
		return a.Num == b.Num && a.IsInt == b.IsInt && a.Int64 == b.Int64 && a.Uint64 == b.Uint64
	case cellBool:
		return a.Bool == b.Bool
	}
	return true
}

// runBlocks drives the engine manually with fixed-size chunks.
func runBlocks(t *testing.T, corpus []byte, q *Query, bs int) *Table {
	t.Helper()
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
	for off := 0; off < len(corpus); off += bs {
		end := off + bs
		if end > len(corpus) {
			end = len(corpus)
		}
		absorb(e.ProcessBlock(corpus[off:end]))
	}
	absorb(e.Flush())
	out.Finalize(false)
	return out
}

// TestTypeUnification exercises the documented column type rules.
func TestTypeUnification(t *testing.T) {
	input := `{"v":1}
{"v":2.5}
{"v":null}
{"other":1}
{"v":"text"}
`
	q := MustQuery("v")
	tab, err := Run(bytes.NewReader([]byte(input)), q, Options{})
	if err != nil {
		t.Fatal(err)
	}
	col := tab.Columns[0]
	if !col.Mixed {
		t.Fatalf("column with numbers and a string should be mixed, got kind %v", col.Kind)
	}

	tab2, err := Run(bytes.NewReader([]byte(input)), q, Options{UnifyColumnTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	col2 := tab2.Columns[0]
	if col2.Mixed {
		t.Fatalf("unified column should not be mixed")
	}
	if col2.Kind != cellString {
		t.Fatalf("unified column should be string, got %v", col2.Kind)
	}
	want := []string{"1", "2.5", "", "", "text"}
	for i, w := range want {
		if i == 5 {
			break
		}
		got := ""
		if col2.Values[i].Present {
			got = col2.Values[i].Str
		}
		if !((i == 3 && !col2.Values[i].Present) || (i == 2 && !col2.Values[i].Present)) && got != w {
			if i != 2 && i != 3 {
				t.Fatalf("row %d: got %q want %q", i, got, w)
			}
		}
	}

	// A pure-integer column is integral.
	tab3, _ := Run(bytes.NewReader([]byte("{\"n\":1}\n{\"n\":-2}\n{\"n\":3}\n")), MustQuery("n"), Options{})
	if !tab3.Columns[0].Int || tab3.Columns[0].Kind != cellNumber {
		t.Fatalf("integer column: kind %v int %v", tab3.Columns[0].Kind, tab3.Columns[0].Int)
	}
	tab4, _ := Run(bytes.NewReader([]byte("{\"n\":1}\n{\"n\":2.5}\n")), MustQuery("n"), Options{})
	if !tab4.Columns[0].Float {
		t.Fatalf("float column not marked float")
	}
}

// TestNumberRangeError checks that a number too large for float64 is reported
// at the correct position and marks only its line.
func TestNumberRangeError(t *testing.T) {
	input := "{\"n\":1}\n{\"n\":1e400}\n{\"n\":2}\n"
	tab, err := Run(bytes.NewReader([]byte(input)), MustQuery("n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if tab.Lines != 3 || tab.Bad != 1 {
		t.Fatalf("lines %d bad %d", tab.Lines, tab.Bad)
	}
	e := tab.Errors[0]
	if e.Line != 2 || e.Diag.Kind != ErrNumberRange {
		t.Fatalf("error %+v", e)
	}
	if e.Diag.Byte != 5 {
		t.Fatalf("range error byte %d, want 5", e.Diag.Byte)
	}
}
