package bitejson

// matcher evaluates all query paths in a single walk of a line's token stream.
//
// The paths are compiled into a trie. Each trie node holds the columns whose
// path terminates there and the children leading deeper (by key, by index, and
// a wildcard). Walking a line visits each needed subtree once and skips every
// subtree no path descends into with an O(1) token jump, so unneeded fields are
// never expanded.
type matcher struct {
	root *trieNode
	out  [][]match
	// wildHit[ci] is set when column ci's wildcard step actually iterated an
	// array, distinguishing an empty list from an absent path.
	wildHit []bool
}

type trieNode struct {
	ends []int
	kids map[childEntry]*trieNode
	wild *trieNode
	cols []int // all columns terminated at or below this node
}

type childEntry struct {
	key    string
	index  int
	isIdx  bool
}

func buildMatcher(q *Query) *matcher {
	m := &matcher{root: &trieNode{}}
	for ci, p := range q.paths {
		n := m.root
		for _, seg := range p.segs {
			switch seg.kind {
			case segWild:
				if n.wild == nil {
					n.wild = &trieNode{}
				}
				n = n.wild
			case segIndex:
				n = m.child(n, childEntry{index: seg.index, isIdx: true})
			default:
				n = m.child(n, childEntry{key: seg.key})
			}
		}
		n.ends = append(n.ends, ci)
	}
	computeCols(m.root)
	return m
}

// computeCols fills each node's cols with every column terminated at or below
// it, so duplicate-key handling can truncate those columns' output cheaply.
func computeCols(n *trieNode) []int {
	cols := append([]int{}, n.ends...)
	if n.wild != nil {
		cols = append(cols, computeCols(n.wild)...)
	}
	for _, c := range n.kids {
		cols = append(cols, computeCols(c)...)
	}
	n.cols = cols
	return cols
}

func (m *matcher) child(n *trieNode, e childEntry) *trieNode {
	if n.kids == nil {
		n.kids = make(map[childEntry]*trieNode)
	}
	c := n.kids[e]
	if c == nil {
		c = &trieNode{}
		n.kids[e] = c
	}
	return c
}

func (m *matcher) reset(ncol int) {
	if cap(m.out) < ncol {
		m.out = make([][]match, ncol)
		m.wildHit = make([]bool, ncol)
	}
	m.out = m.out[:ncol]
	m.wildHit = m.wildHit[:ncol]
	for i := range m.out {
		m.out[i] = m.out[i][:0]
		m.wildHit[i] = false
	}
}

// run fills m.out for the value at pos.
func (m *matcher) run(x *extractor, toks []token, pos int) {
	m.walk(x, toks, pos, m.root, nil)
}

// walk visits the value at pos against node n and returns the next token index.
func (m *matcher) walk(x *extractor, toks []token, pos int, n *trieNode, _ *mark) int {
	t := toks[pos]
	if len(n.ends) > 0 {
		s, e := rawSpan(toks, pos)
		k := kindOfToken(t.kind)
		for _, ci := range n.ends {
			m.out[ci] = append(m.out[ci], match{kind: k, start: s, end: e})
		}
	}
	if len(n.kids) == 0 && n.wild == nil {
		return valueEnd(toks, pos)
	}
	switch t.kind {
	case tokObjectStart:
		p := pos + 1
		// lastSeen tracks, per child subtree, the ranges of m.out to clear if a
		// duplicate key replaces them.
		var seen []mark
		for p < len(toks) && toks[p].kind != tokObjectEnd {
			key := toks[p]
			p++
			child := m.lookupKey(x, key, n)
			if child == nil {
				p = valueEnd(toks, p)
				continue
			}
			// Clear any previous results for the columns under this child so
			// that the last duplicate key wins, as in encoding/json.
			seen = m.clearUnder(child, seen)
			p = m.walk(x, toks, p, child, nil)
		}
		return p + 1
	case tokArrayStart:
		// Entering the array means any wildcard step here was reached, even if
		// the array is empty.
		if n.wild != nil {
			for _, col := range n.wild.cols {
				m.wildHit[col] = true
			}
		}
		p := pos + 1
		idx := 0
		for p < len(toks) && toks[p].kind != tokArrayEnd {
			startTok := p
			var kid *trieNode
			if n.kids != nil {
				kid = n.kids[childEntry{index: idx, isIdx: true}]
			}
			// An element may be needed by its index and/or the wildcard. Walk it
			// separately for each; the duplicate work only happens when a query
			// names both arr[i] and arr[*] for the same index, which is rare.
			switch {
			case kid != nil && n.wild != nil:
				p = m.walk(x, toks, startTok, kid, nil)
				for _, col := range n.wild.cols {
					m.wildHit[col] = true
				}
				m.walk(x, toks, startTok, n.wild, nil)
			case kid != nil:
				p = m.walk(x, toks, startTok, kid, nil)
			case n.wild != nil:
				for _, col := range n.wild.cols {
					m.wildHit[col] = true
				}
				p = m.walk(x, toks, startTok, n.wild, nil)
			default:
				p = valueEnd(toks, p)
			}
			idx++
		}
		return p + 1
	}
	return valueEnd(toks, pos)
}

// mark records the current output length of every column under child, so a
// repeated duplicate key can truncate them (last key wins).
type mark struct {
	child *trieNode
	lens  []int
}

func (m *matcher) clearUnder(child *trieNode, seen []mark) []mark {
	for i := range seen {
		if seen[i].child == child {
			for j, col := range child.cols {
				m.out[col] = m.out[col][:seen[i].lens[j]]
			}
			return seen
		}
	}
	lens := make([]int, len(child.cols))
	for j, col := range child.cols {
		lens[j] = len(m.out[col])
	}
	return append(seen, mark{child: child, lens: lens})
}

func (m *matcher) lookupKey(x *extractor, key token, n *trieNode) *trieNode {
	if n.kids == nil {
		return nil
	}
	body := x.data[key.start:key.end]
	if !hasEscape(body) {
		return n.kids[childEntry{key: string(body)}]
	}
	x.scratch = unquoteString(x.data, int(key.start), int(key.end), x.scratch[:0])
	return n.kids[childEntry{key: string(x.scratch)}]
}
