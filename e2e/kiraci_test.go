//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// tenantFixture, panelden kurulmuş bir yayıncıdır: bir kategori, o kategoride bir kanal ve bir
// izleyici. Her yayıncının sahte yayını farklı boyuttadır; izlenen görüntünün boyutu, onun hangi
// yayıncıdan geldiğini gösterir.
type tenantFixture struct {
	ID       int64
	Owner    *panelClient
	Category struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	Channel struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		StreamKey string `json:"stream_key"`
	}
	Viewer struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	Size string
}

// newAdmin, komut satırından bir yönetici oluşturur ve panele giriş yapar.
func newAdmin(t *testing.T, label string) *panelClient {
	t.Helper()
	var created struct{ Email, Password string }
	email := fmt.Sprintf("%s-%d@example.com", label, time.Now().UnixNano())
	if err := json.Unmarshal(compose(t, "exec", "-T", "api", "kanalvo", "create-admin", email), &created); err != nil {
		t.Fatalf("create-admin çıktısı çözülemedi: %v", err)
	}
	admin := newPanelClient(t)
	admin.login(created.Email, created.Password)
	return admin
}

// newTenant, yönetici panelinden bir yayıncı açar; yayıncı kendi panelinden kategori, kanal ve
// izleyici oluşturur.
func newTenant(t *testing.T, admin *panelClient, label, size string) *tenantFixture {
	t.Helper()
	stamp := time.Now().UnixNano()
	var created struct {
		Tenant   struct{ ID int64 }
		Password string
	}
	email := fmt.Sprintf("%s-%d@example.com", label, stamp)
	admin.call("POST", "/api/admin/tenants", map[string]string{"name": "Yayıncı " + label, "email": email}, &created, http.StatusCreated)
	f := &tenantFixture{ID: created.Tenant.ID, Owner: newPanelClient(t), Size: size}
	f.Owner.login(email, created.Password)
	f.Owner.call("POST", "/api/tenant/categories", map[string]any{"name": "Kategori " + label}, &f.Category, http.StatusCreated)
	f.Owner.call("POST", "/api/tenant/channels", map[string]any{"name": "Kanal " + label, "category_id": f.Category.ID}, &f.Channel, http.StatusCreated)
	f.Owner.call("POST", "/api/tenant/viewers", map[string]any{"username": fmt.Sprintf("%s%d", label, stamp%1000000), "max_connections": 10}, &f.Viewer, http.StatusCreated)
	return f
}

func (f *tenantFixture) player(t *testing.T) *player {
	return newPlayer(t, f.Viewer.Username, f.Viewer.Password)
}

func (f *tenantFixture) waitLive(t *testing.T) {
	t.Helper()
	waitFor(t, f.Channel.Name+" kanalının yayında görünmesi", 30*time.Second, func() bool {
		var list []struct {
			ID   int64 `json:"id"`
			Live bool  `json:"live"`
		}
		f.Owner.call("GET", "/api/tenant/channels", nil, &list, http.StatusOK)
		for _, c := range list {
			if c.ID == f.Channel.ID {
				return c.Live
			}
		}
		return false
	})
}

// ids, panel listesindeki kayıtların numaralarını döner.
func ids(t *testing.T, c *panelClient, path string) []int64 {
	t.Helper()
	var list []struct {
		ID int64 `json:"id"`
	}
	c.call("GET", path, nil, &list, http.StatusOK)
	out := make([]int64, 0, len(list))
	for _, e := range list {
		out = append(out, e.ID)
	}
	return out
}

type sessionRow struct {
	Viewer, Channel, Kind, Edge, IP string
}

func sessionsOf(t *testing.T, c *panelClient) []sessionRow {
	t.Helper()
	var list []sessionRow
	c.call("GET", "/api/tenant/sessions", nil, &list, http.StatusOK)
	return list
}

// Başarı ölçütü 2-4: izleyici yalnızca kendi yayıncısının kanallarını görür ve izler; .ts ve
// .m3u8 adreslerinden gerçekten görüntü gelir; bir yayıncı başka bir yayıncının kanalını,
// izleyicisini veya oturumunu hiçbir yoldan göremez ve değiştiremez.
func TestTenantsAreIsolated(t *testing.T) {
	requireFFprobe(t)
	admin := newAdmin(t, "kiraci-yonetici")
	a := newTenant(t, admin, "a", "640x360")
	b := newTenant(t, admin, "b", "320x180")
	publish(t, project+"-e2e-kiraci-a", a.Channel.StreamKey, a.Size)
	publish(t, project+"-e2e-kiraci-b", b.Channel.StreamKey, b.Size)
	a.waitLive(t)
	b.waitLive(t)

	// Oynatıcı: giriş, kategoriler, kanallar, M3U; listedeki her adresten görüntü.
	for _, f := range []*tenantFixture{a, b} {
		p := f.player(t)
		if acc := p.login(); acc.UserInfo.Auth != 1 || acc.UserInfo.Status != "Active" {
			t.Fatalf("%s: giriş reddedildi: %+v", f.Viewer.Username, acc)
		}
		cats := p.categories()
		if len(cats) != 1 || cats[0].CategoryID != fmt.Sprint(f.Category.ID) {
			t.Fatalf("%s: yalnızca kendi kategorisini görmeli: %+v", f.Viewer.Username, cats)
		}
		streams := p.streams()
		if len(streams) != 1 || streams[0].StreamID != f.Channel.ID || streams[0].CategoryID != fmt.Sprint(f.Category.ID) {
			t.Fatalf("%s: yalnızca kendi kanalını görmeli: %+v", f.Viewer.Username, streams)
		}
		for _, output := range []string{"ts", "m3u8"} {
			list := p.playlist(output)
			if len(list) != 1 || list[0].Name != f.Channel.Name || !strings.HasSuffix(list[0].URL, fmt.Sprintf("/%d.%s", f.Channel.ID, output)) {
				t.Fatalf("%s: get.php output=%s yalnızca kendi kanalını içermeli: %+v", f.Viewer.Username, output, list)
			}
			v := waitForVideo(t, fmt.Sprintf("%s: .%s adresinden görüntü", f.Viewer.Username, output), list[0].URL, f.Size, 10)
			t.Logf("%s .%s: %d kare, %s, %d anahtar kare", f.Viewer.Username, output, v.Frames, v.size(), v.KeyFrames)
		}
	}

	// Başka yayıncının kanalı izleyici adreslerinden istenemez.
	pa := a.player(t)
	for _, u := range []string{
		pa.streamURL(b.Channel.ID, "ts"),
		pa.streamURL(b.Channel.ID, "m3u8"),
		fmt.Sprintf("%s/%s/%s/%d", apiURL, a.Viewer.Username, a.Viewer.Password, b.Channel.ID),
	} {
		if code, _ := probe(t, u); code != http.StatusNotFound {
			t.Errorf("başka yayıncının kanalı istendi: durum %d, beklenen 404 (%s)", code, strings.Replace(u, a.Viewer.Password, "***", 1))
		}
	}
	// A'nın imzası B'nin kanalına taşınamaz: imza kanal numarasını içerir.
	_, hlsLoc := probe(t, pa.streamURL(a.Channel.ID, "m3u8"))
	swapped := strings.Replace(hlsLoc, fmt.Sprintf("/%d.m3u8", a.Channel.ID), fmt.Sprintf("/%d.m3u8", b.Channel.ID), 1)
	if code := status(swapped); code != http.StatusForbidden {
		t.Errorf("A'nın HLS imzası B'nin kanalında: durum %d, beklenen 403", code)
	}
	_, tsLoc := probe(t, pa.streamURL(a.Channel.ID, "ts"))
	tsToken := tsLoc[strings.Index(tsLoc, "?token=")+len("?token="):]
	if body, _ := fetch(fmt.Sprintf("%s/live/%d.ts?token=%s", tsURL, b.Channel.ID, tsToken), 3*188); isMPEGTS(body) {
		t.Error("A'nın .ts imzası B'nin kanalında yayın döndürdü")
	}

	// İki izleyici de izlerken her yayıncı yalnızca kendi oturumunu görür.
	for _, f := range []*tenantFixture{a, b} {
		started, _ := watch(f.player(t).streamURL(f.Channel.ID, "ts"))
		select {
		case <-started:
		case <-time.After(20 * time.Second):
			t.Fatalf("%s: .ts izlemesi başlamadı", f.Viewer.Username)
		}
	}
	for _, f := range []*tenantFixture{a, b} {
		waitFor(t, f.Viewer.Username+" oturumunun panelde görünmesi", 15*time.Second, func() bool {
			for _, s := range sessionsOf(t, f.Owner) {
				if s.Viewer == f.Viewer.Username {
					return true
				}
			}
			return false
		})
	}
	for _, pair := range [][2]*tenantFixture{{a, b}, {b, a}} {
		self, other := pair[0], pair[1]
		for _, s := range sessionsOf(t, self.Owner) {
			if s.Viewer != self.Viewer.Username || s.Channel != self.Channel.Name {
				t.Errorf("%s yayıncısı başka bir yayıncının oturumunu görüyor: %+v", self.Category.Name, s)
			}
		}
		for path, own := range map[string]int64{
			"/api/tenant/channels":   self.Channel.ID,
			"/api/tenant/categories": self.Category.ID,
			"/api/tenant/viewers":    self.Viewer.ID,
		} {
			got := ids(t, self.Owner, path)
			if !slices.Contains(got, own) || slices.ContainsFunc(got, func(id int64) bool {
				return id == other.Channel.ID && path == "/api/tenant/channels" ||
					id == other.Category.ID && path == "/api/tenant/categories" ||
					id == other.Viewer.ID && path == "/api/tenant/viewers"
			}) {
				t.Errorf("%s listesi yalnızca kendi kayıtlarını içermeli: %v", path, got)
			}
		}
		if got := ids(t, self.Owner, "/api/tenant/viewers?q="+other.Viewer.Username); len(got) != 0 {
			t.Errorf("aramada başka yayıncının izleyicisi bulundu: %v", got)
		}
	}

	// A, B'nin kayıtlarını numarasını bilse de değiştiremez ve silemez.
	rename := map[string]any{"name": "ele geçirildi"}
	suspend := map[string]any{"status": "suspended"}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", fmt.Sprintf("/api/tenant/channels/%d", b.Channel.ID), rename},
		{"DELETE", fmt.Sprintf("/api/tenant/channels/%d", b.Channel.ID), nil},
		{"POST", fmt.Sprintf("/api/tenant/channels/%d/regenerate-secret", b.Channel.ID), nil},
		{"PATCH", fmt.Sprintf("/api/tenant/categories/%d", b.Category.ID), rename},
		{"DELETE", fmt.Sprintf("/api/tenant/categories/%d", b.Category.ID), nil},
		{"PATCH", fmt.Sprintf("/api/tenant/viewers/%d", b.Viewer.ID), suspend},
		{"DELETE", fmt.Sprintf("/api/tenant/viewers/%d", b.Viewer.ID), nil},
		{"POST", fmt.Sprintf("/api/tenant/viewers/%d/regenerate-password", b.Viewer.ID), nil},
	} {
		if code := a.Owner.status(c.method, c.path, c.body); code != http.StatusNotFound {
			t.Errorf("A, B'nin kaydında %s %s: durum %d, beklenen 404", c.method, c.path, code)
		}
	}
	// B'nin kategorisine kanal açılamaz.
	if code := a.Owner.status("POST", "/api/tenant/channels", map[string]any{"name": "Sızma", "category_id": b.Category.ID}); code == http.StatusCreated {
		t.Error("A, B'nin kategorisinde kanal açabildi")
	}
	// İstekle gelen yayıncı numarası kabul edilmez: bilinmeyen alan olarak reddedilir.
	for path, body := range map[string]map[string]any{
		"/api/tenant/channels":   {"name": "Kimlik denemesi", "tenant_id": b.ID},
		"/api/tenant/categories": {"name": "Kimlik denemesi", "tenant_id": b.ID},
		"/api/tenant/viewers":    {"username": fmt.Sprintf("kimlik%d", time.Now().UnixNano()%1000000), "tenant_id": b.ID},
	} {
		if code := a.Owner.status("POST", path, body); code != http.StatusBadRequest {
			t.Errorf("tenant_id alanıyla %s: durum %d, beklenen 400", path, code)
		}
	}
	for path, want := range map[string]int{"/api/tenant/channels": 1, "/api/tenant/categories": 1, "/api/tenant/viewers": 1} {
		if got := ids(t, b.Owner, path); len(got) != want {
			t.Errorf("B'nin %s listesi değişti: %v", path, got)
		}
	}
	// Yayıncı yönetici uçlarını kullanamaz.
	if code := a.Owner.status("GET", fmt.Sprintf("/api/admin/tenants/%d", b.ID), nil); code != http.StatusForbidden {
		t.Errorf("yayıncı yönetici ucunu kullandı: durum %d", code)
	}

	// B etkilenmedi: kanalı yayında, izleyicisi aynı şifreyle izliyor.
	pb := b.player(t)
	if acc := pb.login(); acc.UserInfo.Auth != 1 || acc.UserInfo.Status != "Active" {
		t.Fatalf("B'nin izleyicisi etkilendi: %+v", acc)
	}
	waitForVideo(t, "B'nin yayını", pb.streamURL(b.Channel.ID, "ts"), b.Size, 5)
}
