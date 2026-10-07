package hooks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
	"kanalvo/internal/token"
)

func (f *fixture) addEdge(name, pullIP string) int64 {
	f.t.Helper()
	return must(f.store.CreateEdge(context.Background(), store.NewEdge{
		Name: name, BaseURL: "http://" + name, ControlURL: "http://" + name, Key: "key-" + name, PullIP: pullIP, Weight: 100,
	}))
}

func (f *fixture) edgePost(key, event, body string) int {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/edge/"+key+"/hooks/"+event, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code
}

// eventFrom, verilen adresten gelen bir SRS sorgusunun gövdesidir.
func eventFrom(ip, client, stream, param string) string {
	return fmt.Sprintf(`{"action":"x","client_id":%q,"ip":%q,"vhost":"__defaultVhost__","app":"live","tcUrl":"rtmp://edge/live","stream":%q,"param":%q}`,
		client, ip, stream, param)
}

func TestOriginPlayIsAllowedOnlyFromARegisteredEdgeAddress(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	id := f.addEdge("e1", "10.0.0.5")
	stream := fmt.Sprint(f.channel)
	pull := func(ip, param string) int {
		return f.post(hookSecret, "play-origin", eventFrom(ip, "cek-1", stream, param))
	}
	valid := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))

	if code := pull("10.0.0.5", valid); code != http.StatusOK {
		t.Fatalf("kayıtlı edge'in çekmesi kabul edilmeliydi: %d", code)
	}
	// Edge yeniden bağlanırken ilk izleyicinin süresi dolmuş imzasını gönderebilir.
	if code := pull("10.0.0.5", f.tokenFor(f.viewer, f.channel, now.Add(-time.Hour))); code != http.StatusOK {
		t.Fatalf("edge'in çekmesi imzaya bağlı olmamalı: %d", code)
	}
	for _, ip := range []string{"10.0.0.6", "1.2.3.4", "", "10.0.0.5:1935"} {
		if code := pull(ip, valid); code != http.StatusForbidden {
			t.Errorf("kayıtlı olmayan adres %q geçerli imzayla origin'den izleyebildi: %d", ip, code)
		}
	}
	if n := f.sessions(f.viewer); n != 0 {
		t.Fatalf("origin'den çekme oturum açmamalı: %d", n)
	}

	// Edge'in adresinden gelen istek de bu kanal için bizim imzaladığımız bir .ts imzası taşımalıdır:
	// edge ile aynı adresi paylaşan biri (aynı NAT, aynı makine) imzasız izleyemez.
	other := must(f.store.CreateChannel(context.Background(), f.tenant, "diger", "sek2"))
	hls := "?token=" + f.signer.Sign(token.Claims{Kind: token.KindHLS, ViewerID: f.viewer, ChannelID: f.channel, Session: "a1", ExpiresAt: now.Add(time.Minute)})
	for what, param := range map[string]string{
		"imzasız":              "",
		"sahte imza":           "?token=t.1.1.9999999999.sahte",
		"başka kanalın imzası": f.tokenFor(f.viewer, other, now.Add(time.Minute)),
		"HLS imzası":           hls,
	} {
		if code := pull("10.0.0.5", param); code != http.StatusForbidden {
			t.Errorf("edge adresinden %s ile origin'den izlenebildi: %d", what, code)
		}
	}

	// Devre dışı bırakılan ya da silinen edge artık çekemez.
	if err := f.store.UpdateEdge(context.Background(), id, store.EdgeUpdate{Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if code := pull("10.0.0.5", valid); code != http.StatusForbidden {
		t.Fatalf("devre dışı edge origin'den çekebildi: %d", code)
	}
}

func TestEdgeHooksRequireAValidEdgeKey(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	f.addEdge("e1", "10.0.0.5")
	body := event("p1", "live", fmt.Sprint(f.channel), f.tokenFor(f.viewer, f.channel, now.Add(time.Minute)))

	for _, key := range []string{"yanlis", "key-e", "key-e1x", hookSecret} {
		if code := f.edgePost(key, "play", body); code != http.StatusNotFound {
			t.Errorf("geçersiz edge anahtarı %q: durum %d", key, code)
		}
	}
	if n := f.sessions(f.viewer); n != 0 {
		t.Fatalf("geçersiz anahtarla oturum açıldı: %d", n)
	}
	// Edge anahtarı yalnızca izleme olaylarına izin verir; yayın olayları edge'den gelemez.
	for _, ev := range []string{"publish", "unpublish", "play-origin", "bilinmeyen"} {
		if code := f.edgePost("key-e1", ev, body); code != http.StatusNotFound {
			t.Errorf("edge ucunda %q olayı: durum %d", ev, code)
		}
	}
	// Edge anahtarı, kontrol sunucusunun kendi SRS'lerine ayrılmış uçta geçmez.
	if code := f.post("key-e1", "play", body); code != http.StatusNotFound {
		t.Fatalf("edge anahtarı iç sorgu ucunda kabul edildi: %d", code)
	}
}

func TestEdgePlayOpensAndClosesTheSessionOnThatEdge(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	testdb.Exec(t, `UPDATE viewers SET max_connections = 5`)
	remote := f.addEdge("e1", "10.0.0.5")
	stream := fmt.Sprint(f.channel)
	token := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))

	// Aynı bağlantı kimliği iki edge'de iki ayrı izlemedir.
	if code := f.edgePost("key-e1", "play", event("ayni", "live", stream, token)); code != http.StatusOK {
		t.Fatalf("edge'deki izleme kabul edilmeliydi: %d", code)
	}
	if code := f.playAs("ayni", stream, token); code != http.StatusOK {
		t.Fatalf("yerel izleme kabul edilmeliydi: %d", code)
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE edge_id = $1 AND session_key = 'ayni'`, remote); n != 1 {
		t.Fatalf("oturum uzak edge'e yazılmalıydı: %d", n)
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE edge_id = $1 AND session_key = 'ayni'`, f.local); n != 1 {
		t.Fatalf("oturum yerel edge'e yazılmalıydı: %d", n)
	}

	// Yerel SRS'in "izleme bitti" bildirimi edge'deki oturumu kapatmaz.
	if code := f.post(hookSecret, "stop", event("ayni", "live", stream, token)); code != http.StatusOK {
		t.Fatalf("stop: %d", code)
	}
	if n := f.sessions(f.viewer); n != 1 {
		t.Fatalf("edge'deki oturum sürmeliydi: %d", n)
	}
	if code := f.edgePost("key-e1", "stop", event("ayni", "live", stream, token)); code != http.StatusOK {
		t.Fatalf("edge stop: %d", code)
	}
	if n := f.sessions(f.viewer); n != 0 {
		t.Fatalf("edge'in bildirimiyle oturum kapanmalıydı: %d", n)
	}
}

func TestEdgePlayAppliesTheSameRulesAsLocal(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	f.addEdge("e1", "10.0.0.5")
	stream := fmt.Sprint(f.channel)

	if code := f.edgePost("key-e1", "play", event("p1", "live", stream, "?token=sahte")); code != http.StatusForbidden {
		t.Fatalf("sahte imza: %d", code)
	}
	if code := f.edgePost("key-e1", "play", event("p1", "live", stream, f.tokenFor(f.viewer, f.channel, now.Add(-time.Second)))); code != http.StatusForbidden {
		t.Fatalf("süresi dolmuş imza: %d", code)
	}
	if err := f.store.SetViewerStatus(context.Background(), f.viewer, "suspended"); err != nil {
		t.Fatal(err)
	}
	if code := f.edgePost("key-e1", "play", event("p1", "live", stream, f.tokenFor(f.viewer, f.channel, now.Add(time.Minute)))); code != http.StatusForbidden {
		t.Fatalf("askıdaki izleyici edge'den izleyebildi: %d", code)
	}
}
