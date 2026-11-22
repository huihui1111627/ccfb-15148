package bitejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
)

// jsonValue converts an engine cell into the comparable Go form encoding/json
// would produce for the same path.
func jsonValue(t *testing.T, v Value) interface{} {
	if !v.Present {
		return nil
	}
	switch v.Kind {
	case cellBool:
		return v.Bool
	case cellNumber:
		return v.Num
	case cellString:
		return v.Str
	case cellObject, cellArray:
		var out interface{}
		dec := json.NewDecoder(strings.NewReader(v.Str))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			t.Fatalf("cell %q is not valid JSON: %v", v.Str, err)
		}
		return normalizeNumbers(out)
	}
	return nil
}

// normalizeNumbers converts json.Number values to float64 so containers and
// scalars compare uniformly.
func normalizeNumbers(v interface{}) interface{} {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case []interface{}:
		for i := range x {
			x[i] = normalizeNumbers(x[i])
		}
		return x
	case map[string]interface{}:
		for k := range x {
			x[k] = normalizeNumbers(x[k])
		}
		return x
	}
	return v
}

// brutePath resolves a compiled path against a fully decoded JSON document,
// following the same map/array semantics as the engine.
func brutePath(doc interface{}, segs []segment, wildcard bool) (interface{}, bool) {
	if len(segs) == 0 {
		return doc, true
	}
	s := segs[0]
	switch node := doc.(type) {
	case map[string]interface{}:
		if s.kind != segKey {
			return nil, false
		}
		v, ok := node[s.key]
		if !ok {
			return nil, false
		}
		return brutePath(v, segs[1:], wildcard)
	case []interface{}:
		switch s.kind {
		case segIndex:
			if s.index >= len(node) {
				return nil, false
			}
			return brutePath(node[s.index], segs[1:], wildcard)
		case segWild:
			out := []interface{}{}
			for _, e := range node {
				if r, ok := brutePath(e, segs[1:], wildcard); ok {
					out = append(out, r)
				}
			}
			return out, true
		}
		return nil, false
	}
	return nil, false
}

func sameJSON(a, b interface{}) bool {
	return deepEqualJSON(a, b)
}

func deepEqualJSON(a, b interface{}) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		if !ok {
			return false
		}
		if math.IsNaN(x) || math.IsNaN(y) {
			return false
		}
		return x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []interface{}:
		y, ok := b.([]interface{})
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !deepEqualJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]interface{}:
		y, ok := b.(map[string]interface{})
		if !ok || len(x) != len(y) {
			return false
		}
		for k := range x {
			yv, ok := y[k]
			if !ok || !deepEqualJSON(x[k], yv) {
				return false
			}
		}
		return true
	}
	return false
}

// TestAgainstBruteForce compares extracted columns against a full
// encoding/json decode followed by path lookup, for a large corpus of generated
// lines (valid and deliberately broken).
func TestAgainstBruteForce(t *testing.T) {
	rng := newCorpusRand(20240101)
	paths := []string{
		"id", "name", "value", "nested.a", "nested.b.c", "arr[0]", "arr[3]",
		"arr[*]", "arr[*].x", "deep.very.deep.field", "meta[1].k", "flag",
		"missing.path", "big", "sci", "unicode", "obj", "list",
	}
	q := MustQuery(paths...)
	corpus := generateCorpus(rng, 20000)
	lines := bytes.Count(corpus, []byte("\n"))

	table, err := Run(bytes.NewReader(corpus), q, Options{MaxDepth: 10000, BlockSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	// Brute force each line.
	mismatches := 0
	start := 0
	for li := 0; li < lines; li++ {
		end := bytes.IndexByte(corpus[start:], '\n')
		line := corpus[start : start+end]
		start += end + 1
		var doc interface{}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		decErr := dec.Decode(&doc)
		if decErr == nil {
			// Reject trailing content the way the engine does.
			var extra interface{}
			if err2 := dec.Decode(&extra); err2 != io.EOF {
				decErr = fmt.Errorf("trailing data")
			}
		}
		if decErr != nil {
			if !lineFailed(table, li) {
				t.Fatalf("line %d: stdlib rejected (%v), engine accepted: %q", li, decErr, line)
			}
			continue
		}
		if lineFailed(table, li) {
			t.Fatalf("line %d: stdlib accepted, engine rejected: %q (err %v)", li, line, lineDiag(table, li))
		}
		doc = normalizeNumbers(doc)
		for ci, p := range q.paths {
			want, _ := brutePath(doc, p.segs, isWildcard(p))
			got := jsonValue(t, table.Columns[ci].Values[li])
			if !deepEqualJSON(got, want) {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("line %d path %q: got %#v want %#v (line %q)", li, p.raw, got, want, line)
				}
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d value mismatches", mismatches)
	}
}

func lineFailed(tab *Table, li int) bool {
	for _, e := range tab.Errors {
		if e.Line == li+1 {
			return true
		}
	}
	return false
}

func lineDiag(tab *Table, li int) string {
	for _, e := range tab.Errors {
		if e.Line == li+1 {
			return fmt.Sprint(e.Diag)
		}
	}
	return ""
}
