package bitejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// CheckAgainstBruteForce decodes every line fully with encoding/json, looks up
// each query path in the decoded value, and compares every cell against what
// the engine extracted. It returns the number of mismatched cells, the total
// cells compared, and up to eight human-readable mismatch samples.
//
// It also checks that the set of failing lines matches encoding/json: a line
// the engine rejects must be rejected by stdlib, and vice versa. (Encoding/json
// reads only the first value; the check additionally rejects trailing content.)
func CheckAgainstBruteForce(data []byte, q *Query, opts Options) (bad, total int, samples []string, err error) {
	table, err := Run(bytes.NewReader(data), q, opts)
	if err != nil {
		return 0, 0, nil, err
	}
	regions := splitLines(data)
	if len(regions) != table.Lines {
		return 0, 0, nil, fmt.Errorf("line count mismatch: engine %d, split %d", table.Lines, len(regions))
	}
	fail := map[int]Diag{}
	for _, e := range table.Errors {
		fail[e.Line] = e.Diag
	}
	for li, line := range regions {
		var doc interface{}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		decErr := dec.Decode(&doc)
		if decErr == nil {
			var extra interface{}
			if e2 := dec.Decode(&extra); e2 != io.EOF {
				decErr = fmt.Errorf("trailing data")
			}
		}
		engineDiag, engineBad := fail[li+1]
		if decErr != nil {
			if !engineBad {
				samples = appendSample(samples, fmt.Sprintf("line %d: stdlib rejected (%v) but engine accepted: %.120q", li+1, decErr, line))
				bad++
				continue
			}
			// Compare the failure position with encoding/json's SyntaxError
			// offset, which is 1-based. Number-range errors have no stdlib
			// syntax offset and are skipped here.
			if se := jsonSyntaxError(line); se != nil && engineDiag.Kind == ErrSyntax {
				if int64(engineDiag.Col) != se.offset {
					samples = appendSample(samples, fmt.Sprintf("line %d: error position engine %d, stdlib %d (%q): %.120q",
						li+1, engineDiag.Col, se.offset, se.msg, line))
					bad++
				}
			}
			continue
		}
		if engineBad {
			samples = appendSample(samples, fmt.Sprintf("line %d: stdlib accepted but engine rejected (%v): %.120q", li+1, engineDiag, line))
			bad++
			continue
		}

		doc = normalizeJSONNumbers(doc)
		for ci, p := range q.paths {
			want, _ := bruteLookupValue(doc, p.segs)
			got := cellValue(table.Columns[ci].Values[li])
			total++
			if !jsonDeepEqual(got, want) {
				bad++
				if len(samples) < 8 {
					samples = append(samples, fmt.Sprintf("line %d path %q: engine %s, stdlib %s", li+1, p.raw, jsonText(got), jsonText(want)))
				}
			}
		}
	}
	return bad, total, samples, nil
}

// syntaxErr holds the offset and message of encoding/json's first syntax error
// for a single document.
type syntaxErr struct {
	offset int64
	msg    string
}

// jsonSyntaxError returns encoding/json's SyntaxError for line, or nil if the
// input is not a syntax error (well-formed, or a type/range error).
func jsonSyntaxError(line []byte) *syntaxErr {
	var v interface{}
	err := json.Unmarshal(line, &v)
	if se, ok := err.(*json.SyntaxError); ok {
		return &syntaxErr{offset: se.Offset, msg: se.Error()}
	}
	return nil
}

func appendSample(s []string, v string) []string {
	if len(s) >= 8 {
		return s
	}
	return append(s, v)
}

// splitLines splits data into lines without their trailing newline.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// CheckBlockInvariance runs the engine over data in several block sizes,
// including sizes that cut inside strings and escapes, and reports whether all
// results are identical.
//
// Tiny block sizes re-scan the carried partial line on every call, which is
// quadratic in line length; to keep this fast the sub-64-byte sizes run over a
// bounded prefix that still ends on a line boundary. Larger sizes run over the
// whole input.
func CheckBlockInvariance(data []byte, q *Query, opts Options) (bool, error) {
	prefix := boundedPrefix(data, 1<<16)
	sizes := []struct {
		bs  int
		buf []byte
	}{
		{1 << 20, data}, {4093, data}, {65, data}, {64, data},
		{63, prefix}, {13, prefix}, {8, prefix}, {7, prefix}, {1, prefix},
	}
	var reference *Table
	for _, s := range sizes {
		o := opts
		o.BlockSize = s.bs
		tab, err := Run(bytes.NewReader(s.buf), q, o)
		if err != nil {
			return false, err
		}
		if reference == nil {
			reference = tab
			continue
		}
		// The tiny sizes used a prefix, so only compare against another run on
		// the same buffer.
		var want *Table
		if len(s.buf) == len(data) {
			want = reference
		} else {
			w, err := Run(bytes.NewReader(s.buf), q, Options{BlockSize: 1 << 20, MaxDepth: opts.MaxDepth, StrictUTF8: opts.StrictUTF8})
			if err != nil {
				return false, err
			}
			want = w
		}
		if !tablesEqual(want, tab) {
			return false, fmt.Errorf("block size %d differs from block size 1MiB", s.bs)
		}
	}
	return true, nil
}

// boundedPrefix returns the longest prefix of data of at most n bytes that ends
// just after a newline (or all of data if it is shorter).
func boundedPrefix(data []byte, n int) []byte {
	if len(data) <= n {
		return data
	}
	if i := bytes.LastIndexByte(data[:n], '\n'); i >= 0 {
		return data[:i+1]
	}
	return data[:n]
}

func tablesEqual(a, b *Table) bool {
	if a.Lines != b.Lines || a.Bad != b.Bad || len(a.Errors) != len(b.Errors) {
		return false
	}
	for i := range a.Errors {
		if a.Errors[i] != b.Errors[i] {
			return false
		}
	}
	for ci := range a.Columns {
		x, y := a.Columns[ci], b.Columns[ci]
		if len(x.Values) != len(y.Values) {
			return false
		}
		for i := range x.Values {
			if !valueEqual(x.Values[i], y.Values[i]) {
				return false
			}
		}
	}
	return true
}

func valueEqual(a, b Value) bool {
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

// BenchResult reports throughput for the two readers.
type BenchResult struct {
	Bytes             int
	Lines             int
	BitParallelDur    time.Duration
	ByteAtATimeDur    time.Duration
	BitParallelMBps   float64
	ByteAtATimeMBps   float64
}

// Speedup is the bit-parallel engine's advantage over the byte-at-a-time one.
func (b BenchResult) Speedup() float64 {
	if b.ByteAtATimeMBps == 0 {
		return 0
	}
	return b.BitParallelMBps / b.ByteAtATimeMBps
}

// NsPerLine is the bit-parallel cost per line.
func (b BenchResult) NsPerLine() float64 {
	if b.Lines == 0 {
		return 0
	}
	return float64(b.BitParallelDur.Nanoseconds()) / float64(b.Lines)
}

// Benchmark times the engine against a baseline that decodes each line with
// encoding/json and looks up every path.
func Benchmark(data []byte, q *Query, opts Options) (BenchResult, error) {
	var r BenchResult
	r.Bytes = len(data)
	r.Lines = bytes.Count(data, []byte("\n"))

	t0 := time.Now()
	if _, err := Run(bytes.NewReader(data), q, opts); err != nil {
		return r, err
	}
	r.BitParallelDur = time.Since(t0)

	t1 := time.Now()
	slowRead(data, q)
	r.ByteAtATimeDur = time.Since(t1)

	if r.BitParallelDur > 0 {
		r.BitParallelMBps = float64(len(data)) / r.BitParallelDur.Seconds() / 1e6
	}
	if r.ByteAtATimeDur > 0 {
		r.ByteAtATimeMBps = float64(len(data)) / r.ByteAtATimeDur.Seconds() / 1e6
	}
	return r, nil
}

// --- helpers shared with the tests ---

// cellValue converts an engine cell to the value encoding/json would produce.
func cellValue(v Value) interface{} {
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
		dec := json.NewDecoder(bytes.NewReader([]byte(v.Str)))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			return v.Str
		}
		return normalizeJSONNumbers(out)
	}
	return nil
}

// normalizeJSONNumbers converts json.Number values to float64 recursively.
func normalizeJSONNumbers(v interface{}) interface{} {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case []interface{}:
		for i := range x {
			x[i] = normalizeJSONNumbers(x[i])
		}
		return x
	case map[string]interface{}:
		for k := range x {
			x[k] = normalizeJSONNumbers(x[k])
		}
		return x
	}
	return v
}

// bruteLookupValue resolves segs against a fully decoded document, following
// encoding/json map/array semantics.
func bruteLookupValue(doc interface{}, segs []segment) (interface{}, bool) {
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
		return bruteLookupValue(v, segs[1:])
	case []interface{}:
		switch s.kind {
		case segIndex:
			if s.index >= len(node) {
				return nil, false
			}
			return bruteLookupValue(node[s.index], segs[1:])
		case segWild:
			out := []interface{}{}
			for _, e := range node {
				if r, ok := bruteLookupValue(e, segs[1:]); ok {
					out = append(out, r)
				}
			}
			return out, true
		}
		return nil, false
	}
	return nil, false
}

// jsonDeepEqual compares values with the JSON model (float64 numbers, ordered
// slices, unordered maps).
func jsonDeepEqual(a, b interface{}) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []interface{}:
		y, ok := b.([]interface{})
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonDeepEqual(x[i], y[i]) {
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
			if !ok || !jsonDeepEqual(x[k], yv) {
				return false
			}
		}
		return true
	}
	return false
}

// jsonText renders a value compactly for messages.
func jsonText(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
