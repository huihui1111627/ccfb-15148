package bitejson
import ("bytes";"testing")

func TestDuplicateKeysLastWins(t *testing.T){
 input := `{"a":1,"b":{"x":"first"},"a":2,"b":{"x":"second"}}`+"\n"
	tab, _ := Run(bytes.NewReader([]byte(input)), MustQuery("a","b.x"), Options{})
	if got := tab.Columns[0].Values[0].Num; got != 2 { t.Fatalf("a=%v want 2", got) }
	if got := tab.Columns[1].Values[0].Str; got != "second" { t.Fatalf("b.x=%q want second", got) }
}

func TestWildcardMixedIndex(t *testing.T){
 input := `{"arr":[{"x":1},{"y":2},{"x":3}]}`+"\n"
	tab, _ := Run(bytes.NewReader([]byte(input)), MustQuery("arr[0].x","arr[*].x","arr[2]","arr[5]"), Options{})
	if got := tab.Columns[0].Values[0].Num; got != 1 { t.Fatalf("arr[0].x=%v", got) }
	if got := tab.Columns[1].Values[0].Str; got != "[1,3]" { t.Fatalf("arr[*].x=%q", got) }
	if got := tab.Columns[2].Values[0].Str; got != `{"x":3}` { t.Fatalf("arr[2]=%q", got) }
	if tab.Columns[3].Values[0].Present { t.Fatalf("arr[5] should be absent") }
}

func TestEmptyArrayWildcard(t *testing.T){
 input := `{"arr":[]}`+"\n"+`{"other":1}`+"\n"
	tab, _ := Run(bytes.NewReader([]byte(input)), MustQuery("arr[*]"), Options{})
	if !tab.Columns[0].Values[0].Present || tab.Columns[0].Values[0].Str != "[]" {
		t.Fatalf("row0 arr[*]=%+v want []", tab.Columns[0].Values[0])
	}
	if tab.Columns[0].Values[1].Present { t.Fatalf("row1 arr[*] should be absent") }
}

func TestEscapedKeys(t *testing.T){
 input := `{"a\"b":1,"cd":2}`+"\n"
	tab, _ := Run(bytes.NewReader([]byte(input)), MustQuery(`"a\"b"`, "cd"), Options{})
	if got := tab.Columns[0].Values[0].Num; got != 1 { t.Fatalf("a\\\"b=%v", got) }
	if got := tab.Columns[1].Values[0].Num; got != 2 { t.Fatalf("cd=%v", got) }
}
