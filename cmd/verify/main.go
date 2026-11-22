// Command verify generates a large randomised JSON log, checks the bit-parallel
// engine against a brute-force encoding/json decode, and reports throughput
// against a byte-at-a-time baseline. With -page it also writes a standalone
// HTML visualiser for the first block of the input.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"time"

	"bitjson/bitejson"
)

func main() {
	lines := flag.Int("lines", 500000, "number of lines to generate")
	seed := flag.Int64("seed", 1, "random seed")
	blockSize := flag.Int("block", 1<<20, "block size for the standalone run")
	page := flag.String("page", "", "write an HTML visualiser for the first block to this file")
	pageLines := flag.Int("page-lines", 40, "lines shown in the page")
	strict := flag.Bool("strict-utf8", false, "reject lines with malformed UTF-8")
	quick := flag.Bool("quick", false, "skip the brute-force comparison")
	flag.Parse()

	corpus := bitejson.GenerateCorpus(*seed, *lines)
	fmt.Printf("generated %d lines, %d bytes (%.1f MiB)\n", *lines, len(corpus), float64(len(corpus))/1e6)

	q := bitejson.MustQuery(
		"id", "name", "value", "nested.a", "nested.b.c", "arr[0]", "arr[3]",
		"arr[*]", "arr[*].x", "deep.very.deep.field", "meta[1].k", "flag",
		"missing.path", "big", "sci", "unicode", "obj", "list",
	)
	opts := bitejson.Options{BlockSize: *blockSize, StrictUTF8: *strict}

	// Correctness: compare against brute force.
	if !*quick {
		t0 := time.Now()
		bad, total, samples, err := bitejson.CheckAgainstBruteForce(corpus, q, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brute-force check failed to run: %v\n", err)
			os.Exit(1)
		}
		if len(samples) > 0 {
			fmt.Fprintf(os.Stderr, "MISMATCHES (%d of %d cells):\n", bad, total)
			for _, s := range samples {
				fmt.Fprintln(os.Stderr, "  "+s)
			}
			os.Exit(1)
		}
		fmt.Printf("brute-force check: %d cell values all equal to encoding/json (%s)\n", total, time.Since(t0).Round(time.Millisecond))
	}

	// Block invariance.
	inv0 := time.Now()
	ok, err := bitejson.CheckBlockInvariance(corpus, q, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "block-invariance check failed: %v\n", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "BLOCK INVARIANCE FAILED")
		os.Exit(1)
	}
	fmt.Printf("block invariance: identical results for all tested block sizes (%s)\n", time.Since(inv0).Round(time.Millisecond))

	// Throughput.
	bench, err := bitejson.Benchmark(corpus, q, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchmark failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("throughput: bit-parallel %.1f MB/s, byte-at-a-time %.1f MB/s, speedup %.1fx\n",
		bench.BitParallelMBps, bench.ByteAtATimeMBps, bench.Speedup())
	fmt.Printf("  bit-parallel: %s for %d lines (%.1f ns/line)\n",
		bench.BitParallelDur.Round(time.Millisecond), bench.Lines, bench.NsPerLine())

	if *page != "" {
		// Use the compact demo sample: the generated log lines are thousands of
		// bytes each and a bitmap of them is unreadable. A short sample shows
		// the structure, the extracted columns, the timings and the ratio.
		demo := bitejson.DemoInput()
		if *pageLines > 0 && *pageLines < 1000 {
			// Honour an explicit line cap if one was requested.
			n := 0
			for i, b := range demo {
				if b == '\n' {
					n++
					if n >= *pageLines {
						demo = demo[:i+1]
						break
					}
				}
			}
		}
		f, err := os.Create(*page)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := bitejson.WritePage(f, demo, q, opts, "bitejson 解析过程"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		f.Close()
		fmt.Printf("wrote visualiser to %s (%d bytes)\n", *page, len(demo))
	}
	_ = bytes.MinRead
}
