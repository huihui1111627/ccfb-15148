package bitejson

import (
	"math/rand"
	"strconv"
	"strings"
)

// corpusRand is a small deterministic PRNG wrapper so the corpus is stable
// across runs and Go versions.
type corpusRand struct{ *rand.Rand }

func NewCorpusRand(seed int64) *corpusRand { return &corpusRand{rand.New(rand.NewSource(seed))} }

func (r *corpusRand) pick(n int) int { return r.Intn(n) }

var (
	keys = []string{"id", "name", "value", "nested", "arr", "deep", "meta", "flag",
		"big", "sci", "unicode", "obj", "list", "a", "b", "c", "x", "k", "very", "field", "z"}
	strs = []string{
		"", "hello", "a slightly longer string with spaces", "日本語のログ",
		"emoji 😀🚡⛑", "tab\tnewline\\nquote\"backslash\\slash/", "é",
		"ctrl-sim", strings.Repeat("long", 300),
	}
	numSamples = []string{"0", "-0", "1", "-1", "42", "3.14", "-2.5e10", "1e308",
		"1.7976931348623157e308", "12345678901234567890", "-9223372036854775808",
		"0.1", "1E+2", "1e-323", "9007199254740993"}
)

// genValue writes a random JSON value into b and returns it.
func genValue(r *corpusRand, b []byte, depth int) []byte {
	if depth <= 0 {
		return genScalar(r, b)
	}
	switch r.pick(10) {
	case 0, 1:
		// object
		b = append(b, '{')
		n := r.pick(6)
		for i := 0; i < n; i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = genString(r, b)
			b = append(b, ':')
			b = genValue(r, b, depth-1)
		}
		b = append(b, '}')
	case 2, 3:
		b = append(b, '[')
		n := r.pick(6)
		for i := 0; i < n; i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = genValue(r, b, depth-1)
		}
		b = append(b, ']')
	default:
		return genScalar(r, b)
	}
	return b
}

func genScalar(r *corpusRand, b []byte) []byte {
	switch r.pick(10) {
	case 0:
		return append(b, "null"...)
	case 1, 2:
		if r.pick(2) == 0 {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case 3, 4, 5:
		return append(b, numSamples[r.pick(len(numSamples))]...)
	default:
		return genString(r, b)
	}
}

// appendJSONStringBody escapes s as JSON string content (no surrounding
// quotes), so samples containing control or quote bytes stay valid.
func appendJSONStringBody(b []byte, s string) []byte {
	q := strconv.Quote(s)
	return append(b, q[1:len(q)-1]...)
}

// genString writes a JSON string with escapes and Unicode into b.
func genString(r *corpusRand, b []byte) []byte {
	b = append(b, '"')
	switch r.pick(6) {
	case 0:
		// plain, JSON-escaped so raw control bytes never leak into the line
		b = appendJSONStringBody(b, strs[r.pick(len(strs))])
	case 1:
		// escaped
		esc := []string{`\n`, `\t`, `\r`, `\b`, `\f`, `\"`, `\\`, `\/`,
			`A`, `é`, `😀`, `😀`}
		n := 1 + r.pick(4)
		for i := 0; i < n; i++ {
			if r.pick(2) == 0 {
				b = append(b, 'x')
			}
			b = append(b, esc[r.pick(len(esc))]...)
		}
	case 2:
		// lone surrogate (legal JSON, decoded to U+FFFD)
		b = append(b, `\ud800`...)
	case 3:
		// long run
		b = append(b, strings.Repeat("abcdefghij", 1+r.pick(60))...)
	case 4:
		// unicode mix
		b = append(b, "混合 mixed データ 0️⃣ 🚠"...)
	default:
		b = append(b, keys[r.pick(len(keys))]...)
	}
	return append(b, '"')
}

// genLine builds one record object, sometimes corrupting it.
func genLine(r *corpusRand, b []byte) []byte {
	b = append(b, '{')
	n := 6 + r.pick(10)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		k := keys[r.pick(len(keys))]
		b = append(b, '"')
		b = append(b, k...)
		b = append(b, '"', ':')
		switch k {
		case "id":
			b = append(b, numSamples[r.pick(len(numSamples))]...)
		case "name", "unicode":
			b = genString(r, b)
		case "nested":
			b = append(b, '{')
			b = append(b, `"a":`...)
			b = genValue(r, b, 2)
			b = append(b, `,"b":`...)
			b = genValue(r, b, 2)
			b = append(b, '}')
		case "arr":
			b = append(b, '[')
			for j := 0; j < 4; j++ {
				if j > 0 {
					b = append(b, ',')
				}
				if k == "arr" && j == 0 {
					b = append(b, "1.5"...)
				} else {
					b = genValue(r, b, 1)
				}
			}
			b = append(b, ']')
		case "deep":
			b = append(b, `{"very":{"deep":`...)
			b = genValue(r, b, 3)
			b = append(b, "}}"...)
		case "meta":
			b = append(b, `[0,{"k":`...)
			b = genValue(r, b, 1)
			b = append(b, "}]"...)
		default:
			b = genValue(r, b, 3)
		}
	}
	b = append(b, '}')

	// Corruption: sometimes break the current line deliberately. corrupt works
	// on just this line, which is everything after the last newline in b.
	if r.pick(12) == 0 {
		nl := -1
		for i := len(b) - 1; i >= 0; i-- {
			if b[i] == '\n' {
				nl = i
				break
			}
		}
		start := nl + 1
		line := corrupt(r, append([]byte(nil), b[start:]...))
		b = append(b[:start], line...)
	}
	return append(b, '\n')
}

// corrupt damages a valid line the way a truncated or garbled log record would.
func corrupt(r *corpusRand, b []byte) []byte {
	switch r.pick(8) {
	case 0: // truncate
		if len(b) > 2 {
			b = b[:1+r.pick(len(b)-1)]
		}
	case 1: // drop a brace
		b = b[:len(b)-1]
	case 2: // insert junk
		p := r.pick(len(b))
		b = append(b[:p], append([]byte{byte(`{}[]:,"\`[r.pick(7)])}, b[p:]...)...)
	case 3: // unterminated string: replace a quote
		for i := range b {
			if b[i] == '"' {
				b[i] = 'x'
				break
			}
		}
	case 4: // bad escape
		p := r.pick(len(b))
		b = append(b[:p], append([]byte(`\q`), b[p:]...)...)
	case 5: // raw control byte inside
		p := r.pick(len(b))
		b = append(b[:p], append([]byte{0x01}, b[p:]...)...)
	case 6: // trailing junk after the record
		b = append(b, "garbage"...)
	case 7: // invalid utf-8 byte
		p := r.pick(len(b))
		b = append(b[:p], append([]byte{0xff}, b[p:]...)...)
	}
	return b
}

// generateCorpus produces n newline-terminated lines.
func GenerateCorpus(seed int64, n int) []byte {
	return generateCorpus(NewCorpusRand(seed), n)
}

func generateCorpus(r *corpusRand, n int) []byte {
	buf := make([]byte, 0, n*256)
	for i := 0; i < n; i++ {
		buf = genLine(r, buf)
	}
	return buf
}

// corpusLine returns line li (0-based) of the corpus without its newline.
func corpusLine(corpus []byte, li int) []byte {
	start := 0
	for i := 0; i < li; i++ {
		start = indexNewline(corpus, start)
		if start < 0 {
			return nil
		}
		start++
	}
	end := indexNewline(corpus, start)
	if end < 0 {
		end = len(corpus)
	}
	return corpus[start:end]
}

func indexNewline(b []byte, from int) int {
	for i := from; i < len(b); i++ {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

// DemoInput is a small, human-scale newline-delimited sample designed to
// exercise every visible feature of the visualiser: nested objects and arrays,
// escapes, Unicode, a long-ish string, array wildcards, duplicate keys, a
// missing field, and two deliberately broken lines.
func DemoInput() []byte {
	lines := []string{
		`{"id":101,"name":"hoist-1","value":-12.5,"flag":true,"arr":[10,20,30,40],"nested":{"a":"x","b":{"c":"deep"}},"unicode":"钢丝绳 é 😀"}`,
		`{"name":"hoist-2","id":102,"value":7,"arr":[1,2],"nested":{"a":"y\"z","b":{"c":"tab\there"}},"flag":false,"sci":1.5e3}`,
		`{"id":103,"arr":[{"x":"first"},{"x":"second"},{"y":"no-x"}],"nested":{"b":{"c":null}},"list":[true,false,null],"deep":{"very":{"deep":"found"}}}`,
		`{"name":"broken","id":104,"arr":[1,2,3],`,
		`{"id":105,"name":"escapes","unicode":"中文 😀","obj":{"k":"v"},"value":0.001,"missing":null}`,
		`{"id":106,"arr":[],"nested":{"a":{"b":{"c":123}}},"flag":true,"big":12345678901234567890}`,
		`{"id":107,"name":"bad-escape","unicode":"\q","arr":[5]}`,
	}
	buf := make([]byte, 0, 1024)
	for _, l := range lines {
		buf = append(buf, l...)
		buf = append(buf, '\n')
	}
	return buf
}
