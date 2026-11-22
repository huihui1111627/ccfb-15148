package bitejson

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"time"
)

// InspectInfo is everything the visualiser needs about one processed block:
// the bytes, their structural classification, the extracted columns and the
// per-stage timings.
type InspectInfo struct {
	Data    []byte
	Class   []ByteClass
	Lines   []InspectLine
	Timings []StageTiming
	Stats   Stats
	// Compare holds the throughput of this engine and of a straightforward
	// encoding/json-based reader over the same data.
	Compare SpeedCompare
}

// ByteClass classifies one input byte for display.
type ByteClass struct {
	Ch      byte
	Quoted  bool // inside a string
	Escaped bool
	Kind    string // "quote","brace","bracket","colon","comma","backslash","ws","ctrl","high","other"
}

// InspectLine is one line's extracted cells and outcome.
type InspectLine struct {
	Number  int
	Text    string
	Cell    []InspectCell
	Bad     bool
	Diag    string
}

// InspectCell is one path's value on one line, already rendered for display.
type InspectCell struct {
	Path  string
	Text  string
	Kind  string
	Hit   bool
}

// StageTiming records how long one processing stage took over the block.
type StageTiming struct {
	Stage time.Duration
	Bytes int
}

// SpeedCompare holds measured throughputs in megabytes per second. ScanScope
// measures only structural classification (bit-parallel vs a byte-at-a-time
// classifier over the same masks); EndToEnd measures extraction plus a
// full encoding/json decode baseline.
type SpeedCompare struct {
	// BitPar and ByteAtATime are the end-to-end numbers: this engine versus
	// splitting lines and decoding each with encoding/json.
	BitPar      float64
	ByteAtATime float64
	// ScanBitPar and ScanByteAtATime isolate the classifier.
	ScanBitPar      float64
	ScanByteAtATime float64
	Bytes           int
}

// Inspect scans and processes data as blocks and returns everything the
// visualiser displays. The data is treated exactly as a run would treat it:
// complete lines are processed and a trailing partial line is flushed too.
func Inspect(data []byte, q *Query, opts Options) *InspectInfo {
	info := &InspectInfo{Data: data}

	// Stage 1: bit-parallel classification over the whole buffer.
	t0 := time.Now()
	sc := scanner{}
	bm := &blockMasks{}
	sc.scanBlock(data, bm)
	info.Timings = append(info.Timings, StageTiming{Stage: time.Since(t0), Bytes: len(data)})

	// Stage 2: line split.
	t1 := time.Now()
	lastNL := lastNewline(bm)
	info.Timings = append(info.Timings, StageTiming{Stage: time.Since(t1), Bytes: len(data)})

	info.Class = classifyBytes(data, bm)

	// Stage 3: parse + extract. Run the engine over the data and keep each
	// block's line offsets so display rows line up with what was processed.
	t2 := time.Now()
	e := NewEngine(q, opts)
	var rows []InspectLine
	globalBase := 0
	consume := func(res *BlockResult) {
		if res == nil {
			return
		}
		errByLine := map[int]LineError{}
		for _, le := range res.Table.Errors {
			errByLine[le.Line] = le
		}
		for li, off := range res.LineOff {
			end := len(res.Data)
			if li+1 < len(res.LineOff) {
				end = int(res.LineOff[li+1])
			}
			if end > int(off) && end <= len(res.Data) && res.Data[end-1] == '\n' {
				end--
			}
			line := res.Data[off:end]
			l := InspectLine{Number: res.FirstLine + li, Text: string(line)}
			for _, col := range res.Table.Columns {
				if li < len(col.Values) {
					v := col.Values[li]
					l.Cell = append(l.Cell, InspectCell{
						Path: col.Path, Text: renderValue(v), Kind: v.Kind.String(), Hit: v.Present,
					})
				}
			}
			if le, ok := errByLine[li+1]; ok {
				l.Bad = true
				l.Diag = le.Diag.Error()
			}
			rows = append(rows, l)
		}
		globalBase += res.Table.Lines
	}
	// Split the buffer at complete-line boundaries so blocks match a run.
	if lastNL >= 0 {
		consume(e.ProcessBlock(data[:lastNL+1]))
		if lastNL+1 < len(data) {
			consume(e.ProcessBlock(data[lastNL+1:]))
			consume(e.Flush())
		}
	} else {
		consume(e.ProcessBlock(data))
		consume(e.Flush())
	}
	_ = globalBase
	info.Lines = rows
	info.Timings = append(info.Timings, StageTiming{Stage: time.Since(t2), Bytes: len(data)})
	info.Stats.Lines = len(rows)
	for _, l := range rows {
		if l.Bad {
			info.Stats.Bad++
		}
	}
	info.Stats.Strings = countStrings(bm)

	// Stage 4: speed comparison.
	info.Compare = compareSpeed(data, q, opts)
	return info
}

func countStrings(bm *blockMasks) int {
	n := 0
	for _, m := range bm.openQ {
		n += popcount(m)
	}
	return n
}

// renderValue produces a short display string for a cell.
func renderValue(v Value) string {
	if !v.Present {
		return "—"
	}
	switch v.Kind {
	case cellString:
		return v.Str
	case cellBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case cellNumber:
		if v.IsInt {
			if v.Int64 != 0 {
				return fmt.Sprintf("%d", v.Int64)
			}
			return fmt.Sprintf("%d", v.Uint64)
		}
		return fmt.Sprintf("%g", v.Num)
	case cellObject, cellArray:
		return truncateRunes(v.Str, 60)
	}
	return ""
}

// truncateRunes shortens s to at most n runes, appending an ellipsis, without
// cutting a multi-byte character in half.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) > n-3 {
		return string(r[:n-3]) + "..."
	}
	return s
}

// classifyBytes produces the per-byte display classification.
func classifyBytes(data []byte, bm *blockMasks) []ByteClass {
	out := make([]ByteClass, len(data))
	for i := range data {
		c := ByteClass{Ch: data[i]}
		c.Quoted = bm.bit(bm.inStr, i)
		c.Escaped = bm.bit(bm.escaped, i)
		switch {
		case bm.bit(bm.openQ, i) || bm.bit(bm.closeQ, i):
			c.Kind = "quote"
		case bm.bit(bm.bslash, i):
			c.Kind = "backslash"
		case bm.bit(bm.open, i) || bm.bit(bm.close, i):
			if bm.bit(bm.open, i) {
				c.Kind = "bracket"
			} else {
				c.Kind = "bracket"
			}
		case bm.bit(bm.colon, i):
			c.Kind = "colon"
		case bm.bit(bm.comma, i):
			c.Kind = "comma"
		case bm.bit(bm.ws, i) || data[i] == '\n':
			c.Kind = "ws"
		case bm.bit(bm.ctrl, i):
			c.Kind = "ctrl"
		case bm.bit(bm.nonAsc, i):
			c.Kind = "high"
		default:
			c.Kind = "other"
		}
		out[i] = c
	}
	return out
}

// compareSpeed measures this engine against baselines over the same bytes. To
// make the numbers representative rather than startup-dominated on a small
// sample, the input is repeated to at least benchFloor bytes and each
// measurement takes the best of several runs.
func compareSpeed(data []byte, q *Query, opts Options) SpeedCompare {
	const benchFloor = 4 << 20
	const runs = 3
	bench := benchPad(data, benchFloor)
	cp := SpeedCompare{Bytes: len(data)}

	cp.BitPar = bestMBps(bench, runs, func() {
		e := NewEngine(q, Options{BlockSize: 1 << 20})
		e.ProcessBlock(bench)
		e.Flush()
	})
	cp.ByteAtATime = bestMBps(bench, runs, func() { slowRead(bench, q) })
	cp.ScanBitPar = bestMBps(bench, runs, func() {
		var bm blockMasks
		sc := scanner{}
		sc.scanBlock(bench, &bm)
	})
	cp.ScanByteAtATime = bestMBps(bench, runs, func() {
		var bm blockMasks
		classifyNaive(bench, &bm)
	})
	return cp
}

// benchPad repeats data until it reaches at least floor bytes, ending on a line
// boundary.
func benchPad(data []byte, floor int) []byte {
	if len(data) >= floor || len(data) == 0 {
		return data
	}
	n := (floor + len(data) - 1) / len(data)
	out := make([]byte, 0, n*len(data))
	for i := 0; i < n; i++ {
		out = append(out, data...)
	}
	return out
}

// bestMBps runs fn several times over bench and returns the best throughput in
// MB/s.
func bestMBps(bench []byte, runs int, fn func()) float64 {
	best := time.Duration(0)
	for r := 0; r < runs; r++ {
		t := time.Now()
		fn()
		d := time.Since(t)
		if best == 0 || d < best {
			best = d
		}
	}
	if best == 0 {
		return 0
	}
	return float64(len(bench)) / best.Seconds() / 1e6
}

// htmlPage renders an InspectInfo as a standalone HTML document.
func htmlPage(info *InspectInfo, title string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh"><head><meta charset="utf-8">`)
	b.WriteString("<title>" + html.EscapeString(title) + "</title>")
	b.WriteString(`<style>
body{font:13px/1.5 ui-monospace,Menlo,Consolas,monospace;margin:0;background:#0f1115;color:#d5d8de}
header{padding:14px 20px;background:#171a21;border-bottom:1px solid #2a2f3a}
h1{font-size:16px;margin:0 0 4px}
.sub{color:#8b93a1;font-size:12px}
main{padding:16px 20px;display:grid;gap:18px}
section{background:#141821;border:1px solid #232833;border-radius:8px;padding:12px 14px}
h2{font-size:13px;margin:0 0 10px;color:#9aa4b2;font-weight:600;letter-spacing:.04em;text-transform:uppercase}
table{border-collapse:collapse;width:100%}
td,th{padding:3px 8px;text-align:left;vertical-align:top;border-bottom:1px solid #1e232d}
.num{color:#6b7383;text-align:right;white-space:nowrap}
.bitgrid{display:grid;grid-template-columns:repeat(64,12px);gap:1px;font-size:9px;line-height:12px}
.cell{height:12px;text-align:center;border-radius:2px;background:#1b2029;color:#5c6472}
.cell.lit{background:#2d5b4a;color:#d7ffe9}
.cell.q{background:#6b4a12;color:#ffe6b3}
.cell.e{background:#6b1a1a;color:#ffd0d0}
.cell.s{background:#123a5b;color:#cfe8ff}
.bitrow{display:flex;align-items:center;gap:10px;margin:2px 0}
.bitrow .name{width:92px;color:#8b93a1}
.bits{letter-spacing:1px;color:#5fb8ff;font-size:11px}
.bar{height:10px;background:#22303f;border-radius:5px;overflow:hidden}
.bar>span{display:block;height:100%;background:#3f8cff}
.bad{color:#ff8080}
.ok{color:#5fd08a}
.mute{color:#6b7383}
pre{margin:0;white-space:pre-wrap;word-break:break-all}
</style></head><body>`)
	b.WriteString("<header><h1>" + html.EscapeString(title) + "</h1>")
	b.WriteString(fmt.Sprintf(`<div class="sub">%d 字节 · %d 行 · %d 失败 · 位图按 64 字节一组</div>`,
		len(info.Data), len(info.Lines), info.Stats.Bad))
	b.WriteString("</header><main>")

	// Panel 1: structural character bitmaps.
	b.WriteString(`<section><h2>输入块结构字符位图</h2>`)
	shown := len(info.Class)
	if shown > 12*64 {
		shown = 12 * 64
	}
	b.WriteString(fmt.Sprintf("<div class=\"sub\" style=\"margin-bottom:8px\">前 %d 字节（共 %d），每 64 字节一组；绿=命中该类，橙=引号，红=转义，蓝=字符串内</div>", shown, len(info.Class)))
	b.WriteString(bitmapPanel(info))
	b.WriteString("</section>")

	// Panel 2: extracted columns.
	b.WriteString(`<section><h2>每行抽出的列</h2><table><tr><th class="num">#</th>`)
	for _, c := range info.Lines {
		_ = c
		break
	}
	var paths []string
	if len(info.Lines) > 0 {
		for _, cell := range info.Lines[0].Cell {
			paths = append(paths, cell.Path)
		}
	}
	for _, p := range paths {
		b.WriteString("<th>" + html.EscapeString(p) + "</th>")
	}
	b.WriteString("</tr>")
	for _, l := range info.Lines {
		cls := ""
		if l.Bad {
			cls = " class=\"bad\""
		}
		b.WriteString(fmt.Sprintf("<tr%s><td class=\"num\">%d</td>", cls, l.Number))
		for _, cell := range l.Cell {
			style := ""
			if !cell.Hit {
				style = " class=\"mute\""
			}
			b.WriteString("<td" + style + ">" + html.EscapeString(cell.Text) + "</td>")
		}
		b.WriteString("</tr>")
		if l.Bad {
			b.WriteString("<tr><td></td><td colspan=\"" + fmt.Sprint(len(paths)) + "\" class=\"bad\">" +
				html.EscapeString(l.Diag) + "</td></tr>")
		}
	}
	b.WriteString("</table></section>")

	// Panel 3: stage timings.
	b.WriteString(`<section><h2>各阶段的耗时</h2>`)
	names := []string{"位运算建结构位图", "行边界切分", "解析与按路径抽列"}
	var total time.Duration
	for _, t := range info.Timings {
		total += t.Stage
	}
	for i, t := range info.Timings {
		pct := 0.0
		if total > 0 {
			pct = float64(t.Stage) / float64(total) * 100
		}
		name := "阶段"
		if i < len(names) {
			name = names[i]
		}
		b.WriteString(fmt.Sprintf(`<div class="bitrow"><span class="name">%s</span>
<span class="bits">%8.3f ms</span><span class="bar" style="flex:1"><span style="width:%.0f%%"></span></span></div>`,
			html.EscapeString(name), float64(t.Stage.Microseconds())/1000, pct))
	}
	b.WriteString(fmt.Sprintf(`<div class="sub" style="margin-top:8px">合计 %.3f ms，块内 %d 字节</div>`,
		float64(total.Microseconds())/1000, len(info.Data)))
	b.WriteString("</section>")

	// Panel 4: throughput comparison.
	b.WriteString(`<section><h2>逐字符解析速度对比</h2>`)
	cp := info.Compare
	maxv := cp.BitPar
	if cp.ByteAtATime > maxv {
		maxv = cp.ByteAtATime
	}
	if maxv <= 0 {
		maxv = 1
	}
	rows := []struct {
		name string
		v    float64
	}{
		{"结构位图·位运算", cp.ScanBitPar},
		{"结构位图·逐字节", cp.ScanByteAtATime},
		{"整条流水线·位并行引擎", cp.BitPar},
		{"整条流水线·encoding/json", cp.ByteAtATime},
	}
	for _, r := range rows {
		b.WriteString(fmt.Sprintf(`<div class="bitrow"><span class="name">%s</span>
<span class="bits">%8.1f MB/s</span><span class="bar" style="flex:1"><span style="width:%.0f%%"></span></span></div>`,
			html.EscapeString(r.name), r.v, r.v/maxv*100))
	}
	if cp.ScanByteAtATime > 0 {
		b.WriteString(fmt.Sprintf(`<div class="sub" style="margin-top:8px">结构位图加速比 ≈ %.1f×；整条流水线加速比 ≈ %.1f×</div>`,
			cp.ScanBitPar/cp.ScanByteAtATime, cp.BitPar/cp.ByteAtATime))
	}
	b.WriteString("</section>")

	b.WriteString("</main></body></html>")
	return b.String()
}

// bitmapPanel renders the per-class bit rows for each slab.
func bitmapPanel(info *InspectInfo) string {
	var b strings.Builder
	// Recompute masks for display from the stored classification so we do not
	// need to expose internal masks.
	type cls struct {
		name string
		test func(ByteClass) bool
	}
	classes := []cls{
		{"引号", func(c ByteClass) bool { return c.Kind == "quote" }},
		{"反斜杠", func(c ByteClass) bool { return c.Kind == "backslash" }},
		{"花括号", func(c ByteClass) bool { return c.Ch == '{' || c.Ch == '}' }},
		{"方括号", func(c ByteClass) bool { return c.Ch == '[' || c.Ch == ']' }},
		{"冒号", func(c ByteClass) bool { return c.Kind == "colon" }},
		{"逗号", func(c ByteClass) bool { return c.Kind == "comma" }},
		{"字符串内", func(c ByteClass) bool { return c.Quoted }},
	}
	n := len(info.Class)
	const maxSlabs = 12 // keep the page a reasonable size
	if n > maxSlabs*64 {
		n = maxSlabs * 64
	}
	for s := 0; s < n; s += 64 {
		end := s + 64
		if end > n {
			end = n
		}
		// Byte cells.
		b.WriteString("<div class=\"bitgrid\" style=\"margin-bottom:10px\">")
		for i := s; i < end; i++ {
			c := info.Class[i]
			cl := "cell"
			ch := c.Ch
			// Never emit invalid UTF-8 into the page: single bytes >= 0x80 are
			// shown as a placeholder and their value appears in the tooltip.
			disp := string(ch)
			if ch >= 0x80 {
				disp = "·"
			}
			switch {
			case ch == '\n':
				disp, cl = "↵", "cell ws"
			case ch == ' ':
				disp, cl = "·", "cell ws"
			case ch == '\t':
				disp, cl = "→", "cell ws"
			case ch == '\r':
				disp, cl = "␍", "cell ws"
			case c.Escaped:
				cl = "cell e"
			case c.Quoted:
				cl = "cell s"
			case c.Kind == "quote":
				cl = "cell q"
			case ch < 0x20 || ch >= 0x7f:
				disp, cl = "·", "cell"
			}
			b.WriteString("<div class=\"" + cl + "\" title=\"" + html.EscapeString(fmt.Sprintf("offset %d · %s", i, c.Kind)) + "\">" + html.EscapeString(disp) + "</div>")
		}
		b.WriteString("</div>")
		// One bit row per class.
		for _, k := range classes {
			var bits strings.Builder
			for i := s; i < end; i++ {
				if k.test(info.Class[i]) {
					bits.WriteByte('1')
				} else {
					bits.WriteByte('0')
				}
			}
			b.WriteString("<div class=\"bitrow\"><span class=\"name\">" + k.name + "</span><span class=\"bits\">" + bits.String() + "</span></div>")
		}
		b.WriteString("<hr style=\"border-color:#232833;margin:14px 0\">")
	}
	return b.String()
}


// slowRead is the deliberately naive baseline: split on newlines and decode
// every line fully with encoding/json, then look each path up in the decoded
// value. It exists so the visualiser can show the speed ratio honestly.
func slowRead(data []byte, q *Query) {
	start := 0
	for start < len(data) {
		idx := bytesIndexByte(data[start:], '\n')
		if idx < 0 {
			break
		}
		line := data[start : start+idx]
		start += idx + 1
		var doc interface{}
		if err := jsonDecode(line, &doc); err != nil {
			continue
		}
		for _, p := range q.paths {
			bruteLookup(doc, p.segs)
		}
	}
}

func bytesIndexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// jsonDecode is var-indirected so the page can use the standard library
// decoder without importing it at every call site.
var jsonDecode = func(data []byte, v *interface{}) error { return json.Unmarshal(data, v) }

// bruteLookup walks a decoded value along segs, matching the engine's
// map/array semantics; the result is discarded (the point is the cost).
func bruteLookup(doc interface{}, segs []segment) {
	for _, s := range segs {
		switch node := doc.(type) {
		case map[string]interface{}:
			if s.kind != segKey {
				return
			}
			v, ok := node[s.key]
			if !ok {
				return
			}
			doc = v
		case []interface{}:
			switch s.kind {
			case segIndex:
				if s.index >= len(node) {
					return
				}
				doc = node[s.index]
			case segWild:
				for _, e := range node {
					bruteLookup(e, nil)
				}
				return
			default:
				return
			}
		default:
			return
		}
	}
}

// WritePage renders the visualiser for one block of input.
func WritePage(w io.Writer, data []byte, q *Query, opts Options, title string) error {
	info := Inspect(data, q, opts)
	_, err := io.WriteString(w, htmlPage(info, title))
	return err
}
