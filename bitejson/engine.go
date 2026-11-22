package bitejson

import (
	"bufio"
	"io"
	"math/bits"
	"strconv"
)

// Options configures an engine.
type Options struct {
	// MaxDepth limits container nesting. Zero means encoding/json's 10000.
	MaxDepth int
	// UnifyColumnTypes asks each column to report a single JSON type. Without
	// it columns are "mixed" and each cell keeps its own kind.
	UnifyColumnTypes bool
	// StrictUTF8 rejects lines containing malformed UTF-8 (invalid byte
	// sequences, truncated multi-byte characters, broken surrogate escapes are
	// always rejected by the parser). When false, malformed bytes are decoded
	// as U+FFFD by the string decoder, as encoding/json does.
	StrictUTF8 bool
	// BlockSize is the preferred read size for Run. Zero means 1 MiB.
	BlockSize int
}

func (o Options) maxDepth() int {
	if o.MaxDepth <= 0 {
		return 10000
	}
	return o.MaxDepth
}

func (o Options) blockSize() int {
	if o.BlockSize <= 0 {
		return 1 << 20
	}
	return o.BlockSize
}

// Stats describes what one block or one run did.
type Stats struct {
	Lines   int // lines completed
	OK      int // lines that parsed and extracted cleanly
	Bad     int // lines that failed
	Bytes   int // input bytes consumed (including carried tail)
	Strings int // complete strings the scanner saw
}

// Table holds one column per query path and the per-line diagnostics.
type Table struct {
	Columns []*Column
	Lines   int
	Bad     int
	Errors  []LineError
}

// Column stores the extracted values for one path, one entry per line.
type Column struct {
	Path   string
	List   bool // wildcard path: cells are JSON arrays of every match
	Kind   valueKind
	Mixed  bool // more than one kind appeared and unification could not resolve it
	Int    bool // number column whose values are all integers
	Float  bool // number column that needs float64
	Values []Value
}

// BlockResult is returned for every block given to ProcessBlock.
type BlockResult struct {
	// Table holds the cells of the complete lines in this block.
	Table Table
	// Stats summarises the block.
	Stats Stats
	// FirstLine is the 1-based global line number of Table's first row.
	FirstLine int
	// Data is the block's complete prefix (ending with its final newline).
	Data []byte
	// LineOff holds the block-relative start of each line in Data.
	LineOff []int32
	// Masks holds the bitmaps for Data, for the visualiser.
	Masks *blockMasks
}

// Engine processes blocks of newline-delimited JSON. It keeps the unterminated
// tail of a block as the prefix of the next, so no line is ever split across
// two parse calls and results never depend on block boundaries.
type Engine struct {
	opts    Options
	query   *Query
	sc      scanner
	carry   []byte
	nextLno int // 1-based number of the next line to be emitted
}

// NewEngine builds an engine for q.
func NewEngine(q *Query, opts Options) *Engine {
	return &Engine{opts: opts, query: q, nextLno: 1}
}

// ProcessBlock consumes one block. Complete lines are returned now; the
// trailing unterminated line is buffered until a later block or Flush.
func (e *Engine) ProcessBlock(data []byte) *BlockResult {
	res := &BlockResult{FirstLine: e.nextLno}
	if len(e.carry) == 0 {
		return e.process(data, res)
	}
	buf := make([]byte, 0, len(e.carry)+len(data))
	buf = append(buf, e.carry...)
	buf = append(buf, data...)
	e.carry = nil
	return e.process(buf, res)
}

// process scans buf, splits off the trailing partial line, and handles the rest.
func (e *Engine) process(buf []byte, res *BlockResult) *BlockResult {
	// buf always starts at a line boundary (the stream start, or the byte after
	// the last newline of the previous block), so the scanner starts clean.
	// This is what makes results independent of where blocks were cut.
	e.sc.reset()
	bm := &blockMasks{}
	e.sc.scanBlock(buf, bm)
	lastNL := lastNewline(bm)
	if lastNL < 0 {
		e.carry = append(e.carry[:0], buf...)
		res.Masks = bm
		return res
	}
	e.carry = append(e.carry[:0], buf[lastNL+1:]...)
	complete := buf[:lastNL+1]
	res.Data = complete
	res.Masks = bm
	res.LineOff = lineStarts(bm, lastNL)
	res.Table.Columns = make([]*Column, len(e.query.paths))
	for ci, p := range e.query.paths {
		res.Table.Columns[ci] = &Column{Path: p.raw, List: isWildcard(p)}
	}
	e.processLines(complete, res)
	e.nextLno += res.Table.Lines
	return res
}

// Flush handles a final unterminated line, if the stream ended without a
// newline. It returns nil when nothing is buffered.
func (e *Engine) Flush() *BlockResult {
	if len(e.carry) == 0 {
		return nil
	}
	buf := e.carry
	e.carry = nil
	bm := &blockMasks{}
	e.sc.scanBlock(buf, bm)
	res := &BlockResult{FirstLine: e.nextLno, Data: buf, Masks: bm}
	res.LineOff = []int32{0}
	res.Table.Columns = make([]*Column, len(e.query.paths))
	for ci, p := range e.query.paths {
		res.Table.Columns[ci] = &Column{Path: p.raw, List: isWildcard(p)}
	}
	e.processLines(buf, res)
	e.nextLno += res.Table.Lines
	return res
}

// processLines parses and extracts every line of complete, which ends with a
// newline.
func (e *Engine) processLines(complete []byte, res *BlockResult) {
	off := res.LineOff
	res.Table.Lines = len(off)
	for li := range off {
		start := int(off[li])
		end := len(complete)
		if li+1 < len(off) {
			end = int(off[li+1])
		}
		bodyEnd := end
		if bodyEnd > start && complete[bodyEnd-1] == '\n' {
			bodyEnd--
		}
		line := complete[start:bodyEnd]
		ls := &lineScan{data: line, base: start, bm: res.Masks}
		e.processLine(ls, res, li)
	}
}

// processLine validates and extracts one line, appending one cell per column.
func (e *Engine) processLine(ls *lineScan, res *BlockResult, li int) {
	n := len(e.query.paths)
	globalLine := res.FirstLine + li
	var cells []Value
	bad := false
	var diag Diag

	if e.opts.StrictUTF8 {
		if pos, kind := checkUTF8(ls.data); kind != utfOK {
			diag = diagAt(ErrUTF8, pos, kind.message())
			bad = true
		}
	}
	if !bad {
		p := &parser{ls: ls, max: e.opts.maxDepth()}
		if !p.validate() {
			diag = p.err
			bad = true
		} else {
			cells = make([]Value, n)
			x := &extractor{data: ls.data}
			for ci, path := range e.query.paths {
				x.wildHit = false
				ms, _ := x.eval(p.toks, 0, path.segs, 0, nil)
				v, ok, where := e.decodeMatches(ls, path, ms, x.wildHit)
				if !ok {
					bad = true
					diag = diagAt(ErrNumberRange, where, "number out of range for float64")
				}
				cells[ci] = v
			}
		}
	}
	if bad {
		res.Table.Bad++
		res.Stats.Bad++
		res.Table.Errors = append(res.Table.Errors, LineError{Line: globalLine, Diag: diag})
		cells = make([]Value, n)
	} else {
		res.Stats.OK++
	}
	res.Stats.Lines++
	res.Stats.Strings += popcount(0) // strings are counted per block, below
	for ci := range cells {
		col := res.Table.Columns[ci]
		col.Values = append(col.Values, cells[ci])
	}
}


// decodeMatches turns the located matches for one path into a cell value.
func (e *Engine) decodeMatches(ls *lineScan, path *Path, ms []match, wildHit bool) (Value, bool, int) {
	if isWildcard(path) {
		if !wildHit {
			// The path never reached an array, so it is simply absent.
			return Value{Present: false}, true, 0
		}
		// A reached wildcard yields a list, empty when no element matched.
		buf := make([]byte, 0, 16)
		buf = append(buf, '[')
		for i, m := range ms {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, ls.data[m.start:m.end]...)
		}
		buf = append(buf, ']')
		return Value{Kind: cellArray, Str: string(buf), Present: true}, true, 0
	}
	if len(ms) == 0 {
		return Value{Present: false}, true, 0
	}
	// encoding/json map semantics: the last duplicate key wins.
	m := ms[len(ms)-1]
	switch m.kind {
	case cellObject, cellArray:
		return Value{Kind: m.kind, Str: string(ls.data[m.start:m.end]), Present: true}, true, 0
	case cellString:
		s := unquoteString(ls.data, int(m.start)+1, int(m.end)-1, nil)
		return Value{Kind: cellString, Str: string(s), Present: true}, true, 0
	case cellBool:
		return Value{Kind: cellBool, Bool: ls.data[m.start] == 't', Present: true}, true, 0
	case cellNumber:
		nv, ok := parseNumberValue(ls.data, int(m.start), int(m.end), true)
		if !ok {
			return Value{}, false, int(m.start)
		}
		return Value{Kind: cellNumber, Num: nv.f, IsInt: nv.isInt, Int64: nv.i64, Uint64: nv.u64, Present: true}, true, 0
	}
	return Value{Kind: cellNull, Present: false}, true, 0
}

func isWildcard(p *Path) bool {
	for _, s := range p.segs {
		if s.kind == segWild {
			return true
		}
	}
	return false
}

// Finalize resolves each column's type and applies the unification rules. It
// is called by Run; callers driving the engine block by block can call it on a
// table they assembled themselves.
//
// Rules, in order:
//  1. null and absent cells never conflict; they are filled (Present stays
//     false) and the column takes the remaining type.
//  2. If the surviving cells share one kind, the column is that kind.
//  3. Numbers are further unified into int/float: if every number literal is an
//     integer that fits int64/uint64 the column is integral, otherwise float.
//  4. Otherwise the column is Mixed. With UnifyColumnTypes set, mixed scalars
//     are converted to their JSON text (Kind string) so the column is uniform;
//     mixed containers and scalars remain a type conflict.
func (t *Table) Finalize(unify bool) {
	for _, col := range t.Columns {
		col.finalize(unify, t)
	}
}

func (col *Column) finalize(unify bool, t *Table) {
	var kinds uint8
	anyInt, anyFloat := false, false
	for _, v := range col.Values {
		if !v.Present {
			continue
		}
		kinds |= 1 << uint(v.Kind)
		if v.Kind == cellNumber {
			if v.IsInt {
				anyInt = true
			} else {
				anyFloat = true
			}
		}
	}
	col.Mixed = false
	switch {
	case kinds == 0:
		col.Kind = cellNull
	case kinds&(kinds-1) == 0:
		// One kind only.
		col.Kind = valueKind(bitsIndex8(kinds))
		if col.Kind == cellNumber {
			if anyInt && anyFloat {
				col.Kind = cellNumber // still a number column; float view
				col.Float = true
			} else if anyFloat {
				col.Float = true
			} else {
				col.Int = true
			}
		}
	default:
		col.Mixed = true
		hasContainer := kinds&(1<<uint(cellObject)) != 0 || kinds&(1<<uint(cellArray)) != 0
		if unify && !hasContainer {
			// Coerce every present scalar to its JSON text.
			for i := range col.Values {
				if col.Values[i].Present {
					col.Values[i] = scalarAsText(col.Values[i])
				}
			}
			col.Kind = cellString
			col.Mixed = false
		}
	}
}

// lastNewline returns the offset of the last newline not inside a string, or -1.
func lastNewline(bm *blockMasks) int {
	for s := len(bm.nl) - 1; s >= 0; s-- {
		if m := bm.nl[s]; m != 0 {
			return s*64 + 63 - leadingZeros(m)
		}
	}
	return -1
}

// lineStarts returns the start offset of every line in data, whose last byte is
// the newline at lastNL.
func lineStarts(bm *blockMasks, lastNL int) []int32 {
	starts := make([]int32, 0, 16)
	starts = append(starts, 0)
	for s := 0; s < len(bm.nl); s++ {
		m := bm.nl[s]
		for m != 0 {
			pos := s*64 + trailing(m)
			if pos >= lastNL {
				break
			}
			starts = append(starts, int32(pos+1))
			m = lowClear(m)
		}
		if s*64 > lastNL {
			break
		}
	}
	return starts
}

// Run reads newline-delimited JSON from r and returns the combined table.
func Run(r io.Reader, q *Query, opts Options) (*Table, error) {
	e := NewEngine(q, opts)
	bs := opts.blockSize()
	buf := make([]byte, bs)
	br := bufio.NewReaderSize(r, bs)
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
	for {
		n, err := br.Read(buf)
		if n > 0 {
			absorb(e.ProcessBlock(buf[:n]))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	absorb(e.Flush())
	out.Finalize(opts.UnifyColumnTypes)
	return out, nil
}

// bitsIndex8 returns the position of the single set bit in x (x must be a
// power of two).
func bitsIndex8(x uint8) int { return int(bits.TrailingZeros8(x)) }

// scalarAsText converts a present scalar cell to its JSON text, for type
// unification. Strings become quoted JSON strings; containers are not handled
// here because the caller never unifies them.
func scalarAsText(v Value) Value {
	switch v.Kind {
	case cellBool:
		if v.Bool {
			return Value{Kind: cellString, Str: "true", Present: true}
		}
		return Value{Kind: cellString, Str: "false", Present: true}
	case cellNumber:
		if v.IsInt {
			if v.Int64 != 0 {
				return Value{Kind: cellString, Str: strconv.FormatInt(v.Int64, 10), Present: true}
			}
			return Value{Kind: cellString, Str: strconv.FormatUint(v.Uint64, 10), Present: true}
		}
		return Value{Kind: cellString, Str: strconv.FormatFloat(v.Num, 'g', -1, 64), Present: true}
	case cellString:
		return v
	}
	return v
}
