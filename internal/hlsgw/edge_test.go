package hlsgw_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kanalvo/internal/balancer"
	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

const edgeKey = "key-e1"

func (f *fixture) addEdge() int64 {
	f.t.Helper()
	return must(f.store.CreateEdge(context.Background(), store.NewEdge{
		Name: "e1", BaseURL: "http://e1", ControlURL: "http://e1", Key: edgeKey, PullIP: "10.0.0.5", Weight: 100,
	}))
}

// edgeGet, edge'in nginx'inin yaptığı isteği taklit eder: anahtar ve izleyici adresi başlıktadır.
func (f *fixture) edgeGet(path, key, viewerIP string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "10.0.0.5:40000" // edge'in kendi adresi
	if key != "" {
		req.Header.Set(balancer.KeyHeader, key)
	}
	if viewerIP != "" {
		req.Header.Set("X-Real-IP", viewerIP)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func TestEdgeEndpointsRequireAValidEdgeKey(t *testing.T) {
	f := setup(t)
	f.addEdge()
	for _, key := range []string{"", "yanlis", "key-e", "key-e1x"} {
		for _, path := range []string{"/edge/hls/" + f.valid() + "/1.m3u8", "/edge/segment/1.m3u8", "/edge/segment/1-9.ts"} {
			if rec := f.edgeGet(path, key, "203.0.113.7"); rec.Code != http.StatusForbidden {
				t.Errorf("anahtar %q ile %s: durum %d", key, path, rec.Code)
			}
		}
	}
	if hits := f.upstreamHits(); len(hits) != 0 {
		t.Fatalf("anahtarsız istek SRS'e ulaştı: %v", hits)
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 0 {
		t.Fatalf("anahtarsız istek oturum açtı: %d", n)
	}
}

// Kabul edilen istek dosyayı taşımaz: nginx'e dosyayı önbelleğinden vermesini söyler.
func TestEdgeAuthAnswersWithAnInternalRedirect(t *testing.T) {
	f := setup(t)
	remote := f.addEdge()

	rec := f.edgeGet("/edge/hls/"+f.valid()+"/1.m3u8", edgeKey, "203.0.113.7")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Accel-Redirect") != "/_segment/1.m3u8" || rec.Body.Len() != 0 {
		t.Fatalf("durum %d, X-Accel-Redirect %q, gövde %q", rec.Code, rec.Header().Get("X-Accel-Redirect"), rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("yetki yanıtı önbelleğe alınmamalı: %q", rec.Header().Get("Cache-Control"))
	}
	if rec := f.edgeGet("/edge/hls/"+f.valid()+"/1-9.ts", edgeKey, "203.0.113.7"); rec.Header().Get("X-Accel-Redirect") != "/_segment/1-9.ts" {
		t.Fatalf("parça için yönlendirme: %q", rec.Header().Get("X-Accel-Redirect"))
	}
	if hits := f.upstreamHits(); len(hits) != 0 {
		t.Fatalf("yetki isteği SRS'e gitmemeli: %v", hits)
	}
	// Oturum, edge'in bildirdiği izleyici adresiyle ve o edge'e yazılır.
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE edge_id = $1 AND ip = '203.0.113.7' AND kind = 'hls'`, remote); n != 1 {
		t.Fatalf("oturum edge'e ve izleyicinin adresine yazılmalıydı: %d", n)
	}
}

func TestEdgeAuthAppliesTheSameRulesAsTheLocalGateway(t *testing.T) {
	f := setup(t)
	f.addEdge()
	ctx := context.Background()
	check := func(what, path string, want int) {
		t.Helper()
		rec := f.edgeGet(path, edgeKey, "203.0.113.7")
		if rec.Code != want {
			t.Errorf("%s: durum %d, beklenen %d", what, rec.Code, want)
		}
		if rec.Header().Get("X-Accel-Redirect") != "" {
			t.Errorf("%s: reddedilen istek dosyaya yönlendirildi", what)
		}
	}

	check("sahte imza", "/edge/hls/sahte/1.m3u8", http.StatusForbidden)
	check("süresi dolmuş imza", "/edge/hls/"+f.token(f.viewer, f.channel, now.Add(-time.Second))+"/1.m3u8", http.StatusForbidden)
	check("başka kanalın dosyası", "/edge/hls/"+f.valid()+"/2.m3u8", http.StatusForbidden)
	check("bilinmeyen dosya adı", "/edge/hls/"+f.valid()+"/index.html", http.StatusNotFound)

	// Limit 1: adresi paylaşan ikinci kişi ilkini düşürür; düşen oturum edge'den de geri dönemez.
	first := f.session(f.viewer, f.channel, "b1", now.Add(time.Hour))
	second := f.session(f.viewer, f.channel, "b2", now.Add(time.Hour))
	if rec := f.edgeGet("/edge/hls/"+first+"/1.m3u8", edgeKey, "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("ilk oturum: %d", rec.Code)
	}
	if rec := f.edgeGet("/edge/hls/"+second+"/1.m3u8", edgeKey, "203.0.113.8"); rec.Code != http.StatusOK {
		t.Fatalf("ikinci oturum: %d", rec.Code)
	}
	check("yerinden edilmiş oturum", "/edge/hls/"+first+"/1.m3u8", http.StatusForbidden)

	if err := f.store.MarkOffline(ctx, f.channel, "a"); err != nil {
		t.Fatal(err)
	}
	check("çevrimdışı kanal", "/edge/hls/"+second+"/1.m3u8", http.StatusNotFound)
}

// Edge'in bildirdiği izleyici adresi oturumun ağıdır: aynı adres başka bir ağdan kullanılınca
// oturum taşınır ve hemen geri taşınamaz (yerel geçitle aynı kural).
func TestEdgeReportedViewerAddressIsTheSessionNetwork(t *testing.T) {
	f := setup(t)
	f.addEdge()
	path := "/edge/hls/" + f.valid() + "/1.m3u8"
	if rec := f.edgeGet(path, edgeKey, "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("ilk ağ: %d", rec.Code)
	}
	if rec := f.edgeGet(path, edgeKey, "198.51.100.9"); rec.Code != http.StatusOK {
		t.Fatalf("ikinci ağa taşınma: %d", rec.Code)
	}
	if rec := f.edgeGet(path, edgeKey, "203.0.113.7"); rec.Code != http.StatusForbidden {
		t.Fatalf("eski ağdan istek reddedilmeliydi: %d", rec.Code)
	}
}

// İzleyici, yerel geçide X-Real-IP başlığı göndererek kendini başka bir ağda gösteremez.
func TestViewerCannotSpoofItsAddressOnTheLocalGateway(t *testing.T) {
	f := setup(t)
	req := httptest.NewRequest(http.MethodGet, "/hls/"+f.valid()+"/1.m3u8", nil)
	req.RemoteAddr = "192.0.2.44:5000"
	req.Header.Set("X-Real-IP", "203.0.113.7")
	req.Header.Set(balancer.KeyHeader, "uydurma")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("durum %d", rec.Code)
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE ip = '192.0.2.44' AND edge_id = $1`, f.local); n != 1 {
		t.Fatal("oturum bağlantının gerçek adresine ve yerel edge'e yazılmalıydı")
	}
}

func TestEdgeSegmentServesTheFileWithCacheLifetimes(t *testing.T) {
	f := setup(t)
	f.addEdge()

	rec := f.edgeGet("/edge/segment/1.m3u8", edgeKey, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "#EXTM3U\n1-9.ts\n" || rec.Header().Get("Cache-Control") != "max-age=1" {
		t.Fatalf("çalma listesi: durum %d, gövde %q, Cache-Control %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	rec = f.edgeGet("/edge/segment/1-9.ts?x=1", edgeKey, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "TSDATA" || rec.Header().Get("Cache-Control") != "max-age=60" {
		t.Fatalf("parça: durum %d, gövde %q, Cache-Control %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	// Olmayan dosya önbelleğe alınmaz.
	rec = f.edgeGet("/edge/segment/1-777.ts", edgeKey, "")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("olmayan parça: durum %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	for _, name := range []string{"index.html", "..%2F..%2Fapi%2Fv1%2Fclients", "1.flv"} {
		if rec := f.edgeGet("/edge/segment/"+name, edgeKey, ""); rec.Code != http.StatusNotFound {
			t.Errorf("geçersiz dosya adı %q: durum %d", name, rec.Code)
		}
	}

	hits := f.upstreamHits()
	if len(hits) != 3 || hits[0] != "/live/1.m3u8?" || hits[1] != "/live/1-9.ts?" {
		t.Fatalf("SRS'e giden istekler: %v", hits)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyLeaked {
		t.Fatal("edge anahtarı SRS'e aktarıldı")
	}
}
