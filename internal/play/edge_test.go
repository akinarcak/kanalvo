package play_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

func (f *fixture) addEdge(name string, weight int) int64 {
	f.t.Helper()
	ctx := context.Background()
	id := must(f.store.CreateEdge(ctx, store.NewEdge{
		Name: name, BaseURL: "http://" + name + ".test", ControlURL: "http://" + name + ".test", Key: "key-" + name,
		PullIP: "10.0.0.5", Weight: weight,
	}))
	if err := f.store.MarkEdgeSeen(ctx, id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// hosts, aynı adresi n kez ister ve yönlendirilen sunucuları sayar.
func (f *fixture) hosts(file string, n int) map[string]int {
	f.t.Helper()
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		rec := f.get(f.path("ali", "pw", file))
		if rec.Code != http.StatusFound {
			f.t.Fatalf("%s: durum %d", file, rec.Code)
		}
		counts[must(url.Parse(rec.Header().Get("Location"))).Host]++
	}
	return counts
}

func TestViewersAreSpreadAcrossHealthyEdges(t *testing.T) {
	f := setup(t)
	f.addEdge("e1", 100)

	for _, file := range []string{fmt.Sprintf("%d.ts", f.channel), fmt.Sprintf("%d.m3u8", f.channel)} {
		counts := f.hosts(file, 60)
		if len(counts) != 2 || counts["e1.test"] == 0 {
			t.Fatalf("%s: izleyiciler iki sunucuya dağılmalıydı: %v", file, counts)
		}
	}
}

func TestEdgeRedirectUsesTheSamePathsAsLocal(t *testing.T) {
	f := setup(t)
	if err := f.store.UpdateEdge(context.Background(), f.local, store.EdgeUpdate{Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	f.addEdge("e1", 100)

	ts, _ := f.redirect(fmt.Sprintf("%d.ts", f.channel))
	if ts.Host != "e1.test" || ts.Path != fmt.Sprintf("/live/%d.ts", f.channel) || ts.Query().Get("token") == "" {
		t.Fatalf(".ts yönlendirmesi: %s", ts)
	}
	hls, _ := f.redirect(fmt.Sprintf("%d.m3u8", f.channel))
	if hls.Host != "e1.test" || len(hls.Path) < len("/hls/x/1.m3u8") || hls.Path[:5] != "/hls/" {
		t.Fatalf("HLS yönlendirmesi: %s", hls)
	}
}

func TestEdgeWithoutHealthSignalGetsNoViewers(t *testing.T) {
	f := setup(t)
	id := f.addEdge("e1", 1000)
	testdb.Exec(t, `UPDATE edges SET last_seen_at = now() - interval '1 minute' WHERE id = $1`, id)

	for _, file := range []string{fmt.Sprintf("%d.ts", f.channel), fmt.Sprintf("%d.m3u8", f.channel)} {
		if counts := f.hosts(file, 30); counts["e1.test"] != 0 {
			t.Fatalf("%s: sinyali kesilen edge'e yönlendirildi: %v", file, counts)
		}
	}

	// Sinyal dönünce yeniden yönlendirme alır.
	if err := f.store.MarkEdgeSeen(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if counts := f.hosts(fmt.Sprintf("%d.ts", f.channel), 60); counts["e1.test"] == 0 {
		t.Fatalf("sinyali dönen edge yönlendirme almalıydı: %v", counts)
	}
}

func TestDisabledEdgeGetsNoViewers(t *testing.T) {
	f := setup(t)
	id := f.addEdge("e1", 1000)
	if err := f.store.UpdateEdge(context.Background(), id, store.EdgeUpdate{Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if counts := f.hosts(fmt.Sprintf("%d.ts", f.channel), 30); counts["e1.test"] != 0 {
		t.Fatalf("devre dışı edge'e yönlendirildi: %v", counts)
	}
}

func TestNoHealthyEdgeIsServiceUnavailable(t *testing.T) {
	f := setup(t)
	testdb.Exec(t, `UPDATE edges SET last_seen_at = NULL`)

	rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.ts", f.channel)))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("sağlıklı edge yokken .ts: durum %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Yerel HLS geçidi API'nin içindedir; .ts dağıtıcısı dursa da çalışır.
	if rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.m3u8", f.channel))); rec.Code != http.StatusFound {
		t.Fatalf("yerel HLS yönlendirmesi sürmeliydi: durum %d", rec.Code)
	}

	// Yerel edge de devre dışıysa HLS için de sunucu kalmaz.
	if err := f.store.UpdateEdge(context.Background(), f.local, store.EdgeUpdate{Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.m3u8", f.channel))); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("hiç edge yokken HLS: durum %d", rec.Code)
	}
	// Yetkisiz istek, edge durumundan bağımsız olarak reddedilir.
	if rec := f.get(f.path("ali", "yanlis", fmt.Sprintf("%d.ts", f.channel))); rec.Code != http.StatusForbidden {
		t.Fatalf("hatalı şifre: durum %d", rec.Code)
	}
}
