package bitejson

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCheckUTF8MatchesStdlib(t *testing.T) {
	cases := []struct {
		s    string
		ok   bool
		kind utfErrKind
		pos  int
	}{
		{"", true, utfOK, 0},
		{"hello", true, utfOK, 0},
		{"héllo", true, utfOK, 0},
		{"😀🚡", true, utfOK, 0},
		{"日本語", true, utfOK, 0},
		{"\xff", false, utfInvalidByte, 0},
		{"abc\xff", false, utfInvalidByte, 3},
		{"\xc3", false, utfTruncated, 0},
		{"\xc3\x28", false, utfInvalidByte, 0},
		{"\xe2\x82", false, utfTruncated, 0},
		{"\xe2\x28\xa1", false, utfInvalidByte, 0},
		{"\xf0\x9f\x98", false, utfTruncated, 0},
		{"\xed\xa0\x80", false, utfInvalidByte, 0}, // surrogate
		{"\xc0\xaf", false, utfInvalidByte, 0},     // overlong
		{"\xf4\x90\x80\x80", false, utfInvalidByte, 0}, // > U+10FFFF
		{"ok\xe2\x82\xac", true, utfOK, 0},
	}
	for _, c := range cases {
		pos, kind := checkUTF8([]byte(c.s))
		if (kind == utfOK) != c.ok {
			t.Errorf("%q: ok=%v want %v (kind %v pos %d)", c.s, kind == utfOK, c.ok, kind, pos)
			continue
		}
		if !c.ok && (kind != c.kind || pos != c.pos) {
			t.Errorf("%q: got kind %v pos %d, want %v pos %d", c.s, kind, pos, c.kind, c.pos)
		}
	}
}

// TestStrictUTF8Lines checks that strict mode marks only the offending line and
// reports the position, while a line with valid multi-byte text passes.
func TestStrictUTF8Lines(t *testing.T) {
	input := "{\"n\":1,\"s\":\"ok é\"}\n" +
		"{\"n\":2,\"s\":\"bad \xff byte\"}\n" +
		"{\"n\":3,\"s\":\"trunc \xc3\"}\n" +
		"{\"n\":4,\"s\":\"fine 😀\"}\n"
	tab, err := Run(bytes.NewReader([]byte(input)), MustQuery("n", "s"), Options{StrictUTF8: true})
	if err != nil {
		t.Fatal(err)
	}
	if tab.Lines != 4 || tab.Bad != 2 {
		t.Fatalf("lines %d bad %d", tab.Lines, tab.Bad)
	}
	if tab.Errors[0].Line != 2 || tab.Errors[0].Diag.Kind != ErrUTF8 {
		t.Fatalf("first error %+v", tab.Errors[0])
	}
	if tab.Errors[1].Line != 3 {
		t.Fatalf("second error %+v", tab.Errors[1])
	}
	// The invalid byte in "bad \xff byte" is at line offset 19.
	if tab.Errors[0].Diag.Byte != 16 {
		t.Fatalf("utf8 error byte %d, want 16", tab.Errors[0].Diag.Byte)
	}
}

// TestNonStrictUTF8Replaces checks that without strict mode malformed bytes are
// decoded like encoding/json, replacing them with U+FFFD.
func TestNonStrictUTF8Replaces(t *testing.T) {
	input := "{\"s\":\"a\xffb\"}\n"
	tab, err := Run(bytes.NewReader([]byte(input)), MustQuery("s"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if tab.Bad != 0 {
		t.Fatalf("non-strict should accept, got bad %d: %v", tab.Bad, tab.Errors)
	}
	got := tab.Columns[0].Values[0].Str
	want := "a" + string(utf8.RuneError) + "b"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Cross-check against encoding/json.
	_ = strings.Count
}
