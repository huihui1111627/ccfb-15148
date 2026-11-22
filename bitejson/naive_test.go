package bitejson
import ("testing";"time";"fmt")
func TestNaiveMatchesSWAR(t *testing.T){
 corpus := GenerateCorpus(11, 500)
 var a, b blockMasks
 sc:=scanner{}; sc.scanBlock(corpus,&a)
 classifyNaive(corpus,&b)
 cmp := func(name string, x,y []uint64){
   if len(x)!=len(y){t.Fatalf("%s len",name)}
   for i:=range x{ if x[i]!=y[i]{t.Fatalf("%s slab %d: %x vs %x",name,i,x[i],y[i])} }
 }
 cmp("quote",a.quote,b.quote); cmp("bslash",a.bslash,b.bslash); cmp("esc",a.escaped,b.escaped)
 cmp("inStr",a.inStr,b.inStr); cmp("openQ",a.openQ,b.openQ); cmp("closeQ",a.closeQ,b.closeQ)
 cmp("ws",a.ws,b.ws); cmp("nl",a.nl,b.nl); cmp("open",a.open,b.open); cmp("close",a.close,b.close)
 cmp("colon",a.colon,b.colon); cmp("comma",a.comma,b.comma)
}
func BenchmarkScanSWAR(b *testing.B){ corpus:=GenerateCorpus(7,5000); b.SetBytes(int64(len(corpus))); b.ResetTimer(); for i:=0;i<b.N;i++{ var bm blockMasks; sc:=scanner{}; sc.scanBlock(corpus,&bm) } }
func BenchmarkScanNaive(b *testing.B){ corpus:=GenerateCorpus(7,5000); b.SetBytes(int64(len(corpus))); b.ResetTimer(); for i:=0;i<b.N;i++{ var bm blockMasks; classifyNaive(corpus,&bm) } }
var _ = time.Now
var _ = fmt.Sprint
