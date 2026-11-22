package bitejson

import (
	"bytes"
	"testing"
)

// TestStrictSurrogateEscapes checks the opt-in reporting of malformed \u
// surrogate pairings at their exact position, and that valid pairs and ordinary
// escapes pass.
func TestStrictSurrogateEscapes(t *testing.T) {
	cases := map[string]bool{
		"{\"k\":\"\\ud83d\\ude00\"}": true, // valid explicit surrogate pair
		`{"k":"\ud800"}`:       false, // lone high surrogate
		`{"k":"\udc00"}`:       false, // lone low surrogate
		`{"k":"\ud800x"}`:      false, // high not followed by a low escape
		`{"k":"\ud800\ud800"}`: false, // high followed by high
		`{"k":"plain"}`:        true,
		`{"k":"A"}`:            true,
	}
	for in, want := range cases {
		tab, err := Run(bytes.NewReader([]byte(in+"\n")), MustQuery("k"), Options{StrictEscapes: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := tab.Bad == 0; got != want {
			t.Errorf("%s: accepted=%v want %v (%v)", in, got, want, tab.Errors)
		}
	}
	tab, _ := Run(bytes.NewReader([]byte(`{"k":"\ud800"}`+"\n")), MustQuery("k"), Options{StrictEscapes: true})
	if len(tab.Errors) != 1 || tab.Errors[0].Diag.Byte != 6 {
		t.Fatalf("position not reported at 6: %+v", tab.Errors)
	}
}

// TestLoneSurrogateDefaultMatchesStdlib confirms that without StrictEscapes a
// lone surrogate decodes to U+FFFD exactly as encoding/json does.
func TestLoneSurrogateDefaultMatchesStdlib(t *testing.T) {
	tab, _ := Run(bytes.NewReader([]byte(`{"k":"\ud800"}`+"\n")), MustQuery("k"), Options{})
	if tab.Bad != 0 {
		t.Fatalf("default should accept lone surrogate, got %v", tab.Errors)
	}
	if got := tab.Columns[0].Values[0].Str; got != "�" {
		t.Fatalf("got %q want U+FFFD", got)
	}
}
