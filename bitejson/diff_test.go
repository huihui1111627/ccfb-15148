package bitejson

import (
	"testing"
)

// TestAgainstBruteForce compares the engine against a full encoding/json decode
// plus path lookup over a large corpus of valid and deliberately broken lines.
func TestAgainstBruteForce(t *testing.T) {
	corpus := GenerateCorpus(20240101, 20000)
	q := verificationQuery()
	bad, total, samples, err := CheckAgainstBruteForce(corpus, q, Options{MaxDepth: 10000})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		t.Error(s)
	}
	if bad != 0 {
		t.Fatalf("%d of %d cells mismatched", bad, total)
	}
	if total == 0 {
		t.Fatal("no cells compared")
	}
	t.Logf("checked %d cell values", total)
}

func verificationQuery() *Query {
	return MustQuery(
		"id", "name", "value", "nested.a", "nested.b.c", "arr[0]", "arr[3]",
		"arr[*]", "arr[*].x", "deep.very.deep.field", "meta[1].k", "flag",
		"missing.path", "big", "sci", "unicode", "obj", "list",
	)
}
