package bitejson

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// buildLineScan builds the block masks for one line, for tests and small
// inputs. The line has no terminating newline, so base is 0.
func buildLineScan(t *testing.T, line []byte) *lineScan {
	t.Helper()
	var sc scanner
	bm := &blockMasks{}
	sc.scanBlock(line, bm)
	return &lineScan{data: line, base: 0, bm: bm}
}

func parseLine(t *testing.T, line []byte) (*parser, bool) {
	t.Helper()
	ls := buildLineScan(t, line)
	p := &parser{ls: ls, max: 10000}
	ok := p.validate()
	return p, ok
}

// stdError extracts the offset and message from encoding/json's error for a
// single JSON document, or reports that it accepted the input.
func stdError(line []byte) (offset int64, msg string, errKind int) {
	var v interface{}
	err := json.Unmarshal(line, &v)
	if err == nil {
		return 0, "", 0
	}
	switch e := err.(type) {
	case *json.SyntaxError:
		return e.Offset, e.Error(), 1
	case *json.UnmarshalTypeError:
		return e.Offset, e.Error(), 2
	}
	return -1, err.Error(), 3
}

func TestTokenizerMatchesEncodingJSON(t *testing.T) {
	valid := []string{
		`{"a":1}`, `{"a":1,"b":[1,2,{"c":"x"}]}`, `[1,2,3]`, `"x"`, `123`, `-4.5e-3`,
		`{"k":"v","k2":null,"k3":true,"k4":false}`, `{"a":{}}`, `{"a":[]}`, `{"a":{}}`,
		`{"a":"é\n\t\\\"\/"}`, `{"é":"😀"}`, `  {"a": 1}  `, "\t{\"a\":\r1}\n",
		`{"a":{"b":{"c":[[[1]]]}}}`, `[{"a":1},{"b":2}]`,
		`{"a":1,"a":2}`, `{"":""}`, `[ ]`, `{ }`, `[-0,0,0.0,1e0,1E+1]`,
	}
	broken := []string{
		``, ` `, `{`, `}`, `[`, `]`, `{"a"`, `{"a":`, `{"a":1`, `{"a":1,}`, `{"a":1 "b":2}`,
		`[1,]`, `[,1]`, `[1 2]`, `{"a" 1}`, `{a:1}`, `{"a":01}`, `{"a":1.}`, `{"a":1e}`,
		`{"a":tru}`, `{"a":"\q"}`, `{"a":"\u12"}`, `{"a":"abc`, `{"a":"a` + "\x01" + `b"}`,
		`{"a":}`, `{"a":,}`, `{"a":1}}`, `[1]]`, `{"a":1}x`, `nul`, `nullx`, `truefalse`,
		`{"a":-}`, `{"a":+1}`, `{"a":1,,}`, `{}}`, `{"a":{"b":}}`, `{"a":[}`, `[}`, `{]`,
		`{"a":"\`, `{"a":"\u12`, `{"a":1.`, `{"a":1e`, `{"a":-`, `{"a":tru`, `{"a":"x`,
		"{\"a\":\"x\n\"}", "{\"a\":\"x\\\n\"}", `{"a":[1,2}`, `{"a":1}b`, `"a"b`, `1 2`,
		`{"a" : }`, `{"a":"\u123"}`, `{"a":-01}`, `{"a":00}`,
	}
	cases := append(append([]string{}, valid...), broken...)

	rng := rand.New(rand.NewSource(7))
	alphabet := []byte(`{}[]:,"\ 0123456789.eE+-truefalsnABCxyz` + "\t\r\n\x01é😀")
	for i := 0; i < 4000; i++ {
		n := rng.Intn(40)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		cases = append(cases, string(b))
	}
	// Mutations of valid docs.
	for i := 0; i < 3000; i++ {
		s := []byte(valid[rng.Intn(len(valid))])
		if len(s) == 0 {
			continue
		}
		for k := 0; k < 1+rng.Intn(3); k++ {
			switch rng.Intn(3) {
			case 0:
				s[rng.Intn(len(s))] = alphabet[rng.Intn(len(alphabet))]
			case 1:
				p := rng.Intn(len(s) + 1)
				s = append(s[:p], append([]byte{alphabet[rng.Intn(len(alphabet))]}, s[p:]...)...)
			case 2:
				p := rng.Intn(len(s))
				s = append(s[:p], s[p+1:]...)
			}
			if len(s) == 0 {
				break
			}
		}
		cases = append(cases, string(s))
	}

	for _, s := range cases {
		if strings.Contains(s, "\n") {
			continue // the tokenizer is given one line without its newline
		}
		line := []byte(s)
		p, ok := parseLine(t, line)
		soff, smsg, skind := stdError(line)
		if skind == 3 {
			continue // not a syntax/type error; skip exotic cases
		}
		if skind == 2 {
			// float range error: the tokenizer accepts it; the column decode
			// stage reports it. Only require acceptance here.
			if !ok {
				t.Fatalf("line %q: encoding/json range error %q but tokenizer rejected with %v", s, smsg, p.err)
			}
			continue
		}
		if skind == 1 {
			if ok {
				t.Fatalf("line %q: encoding/json rejected (%d %q), tokenizer accepted", s, soff, smsg)
			}
			if p.err.Kind != ErrSyntax {
				t.Fatalf("line %q: tokenizer kind %v, want syntax", s, p.err.Kind)
			}
			if int64(p.err.Col) != soff {
				t.Fatalf("line %q: offset %d msg %q, want offset %d msg %q", s, p.err.Col, p.err.Msg, soff, smsg)
			}
			if p.err.Msg != smsg {
				t.Fatalf("line %q: msg %q, want %q", s, p.err.Msg, smsg)
			}
			continue
		}
		if !ok {
			t.Fatalf("line %q: encoding/json accepted, tokenizer rejected with %v", s, p.err)
		}
	}
}
