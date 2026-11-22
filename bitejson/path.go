package bitejson

import (
	"fmt"
	"strconv"
)

// segKind is one component of a compiled path.
type segKind uint8

const (
	segKey segKind = iota // object member by name
	segIndex              // array element by non-negative index
	segWild               // any array element
)

type segment struct {
	kind  segKind
	key   string
	index int
}

// Path is a compiled field path such as
//   rope.tension
//   sensors[3].value
//   readings[*].raw
//
// Object keys may be quoted (".a b", "['x']") when they contain separators.
type Path struct {
	raw  string
	segs []segment
}

func (p *Path) String() string { return p.raw }

// ParsePath compiles a dotted path with optional array subscripts and the '*'
// wildcard. An empty path is rejected.
func ParsePath(s string) (*Path, error) {
	if s == "" {
		return nil, fmt.Errorf("empty path")
	}
	p := &Path{raw: s}
	i := 0
	expectKey := true
	for i < len(s) {
		c := s[i]
		switch {
		case c == '.':
			if expectKey {
				return nil, fmt.Errorf("path %q: unexpected '.' at %d", s, i)
			}
			expectKey = true
			i++
		case c == '[':
			j := i + 1
			for j < len(s) && s[j] != ']' {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("path %q: unterminated '[' at %d", s, i)
			}
			sub := s[i+1 : j]
			seg := segment{}
			if sub == "*" {
				seg.kind = segWild
			} else {
				n, err := strconv.Atoi(sub)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("path %q: bad array subscript %q at %d", s, sub, i)
				}
				seg.kind = segIndex
				seg.index = n
			}
			p.segs = append(p.segs, seg)
			expectKey = false
			i = j + 1
		case c == '"' || c == '\'':
			// Quoted key: read to the matching quote, honouring backslash
			// escapes so a key may contain the quote character itself.
			q := c
			j := i + 1
			var key []byte
			for j < len(s) && s[j] != q {
				if s[j] == '\\' && j+1 < len(s) {
					key = append(key, s[j+1])
					j += 2
					continue
				}
				key = append(key, s[j])
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("path %q: unterminated quote at %d", s, i)
			}
			p.segs = append(p.segs, segment{kind: segKey, key: string(key)})
			expectKey = false
			i = j + 1
		default:
			// bare key up to '.' or '['
			j := i
			for j < len(s) && s[j] != '.' && s[j] != '[' {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("path %q: empty segment at %d", s, i)
			}
			p.segs = append(p.segs, segment{kind: segKey, key: s[i:j]})
			expectKey = false
			i = j
		}
	}
	if expectKey {
		return nil, fmt.Errorf("path %q: trailing '.'", s)
	}
	// fold a leading bare key followed directly by subscript: already handled.
	return p, nil
}

// mustPath is a test and internal helper.
func mustPath(s string) *Path {
	p, err := ParsePath(s)
	if err != nil {
		panic(err)
	}
	return p
}
