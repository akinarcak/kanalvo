package panelapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"streamhub/internal/testdb"
)

type edgeJSON struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Builtin        bool    `json:"builtin"`
	BaseURL        string  `json:"base_url"`
	HLSBaseURL     string  `json:"hls_base_url"`
	ControlURL     string  `json:"control_url"`
	PullIP         string  `json:"pull_ip"`
	Weight         int     `json:"weight"`
	Enabled        bool    `json:"enabled"`
	Healthy        bool    `json:"healthy"`
	LastSeenAt     *string `json:"last_seen_at"`
	ActiveSessions int     `json:"active_sessions"`
	Setup          string  `json:"setup"`
}

func (c *client) edges() []edgeJSON {
	c.f.t.Helper()
	var list []edgeJSON
	c.want(c.get("/api/admin/edges"), http.StatusOK).into(c.f.t, &list)
	return list
}

func TestAdminManagesEdges(t *testing.T) {
	f := setup(t)
	admin := f.admin()
	ctx := context.Background()
	if _, err := f.store.SyncLocalEdge(ctx, "http://tv.example.com:8081", "http://tv.example.com:8000"); err != nil {
		t.Fatal(err)
	}

	list := admin.edges()
	if len(list) != 1 || !list[0].Builtin || list[0].BaseURL != "http://tv.example.com:8081" || list[0].HLSBaseURL != "http://tv.example.com:8000" || list[0].Setup != "" {
		t.Fatalf("başlangıçta yalnızca yerel edge: %+v", list)
	}
	local := list[0]

	// Yeni edge: anahtarı üretilir, sinyal verene kadar sağlıksızdır.
	var e edgeJSON
	admin.want(admin.post("/api/admin/edges", map[string]any{
		"name": " Frankfurt ", "base_url": "http://edge1.example.com:8090/", "pull_ip": "203.0.113.20",
	}), http.StatusCreated).into(t, &e)
	if e.Name != "Frankfurt" || e.BaseURL != "http://edge1.example.com:8090" || e.HLSBaseURL != e.BaseURL || e.ControlURL != e.BaseURL ||
		e.PullIP != "203.0.113.20" || e.Weight != 100 || !e.Enabled || e.Healthy || e.Builtin {
		t.Fatalf("oluşturulan edge: %+v", e)
	}
	key := testdb.Count(t, `SELECT length(edge_key) FROM edges WHERE id = $1`, e.ID)
	if key < 32 {
		t.Fatalf("edge anahtarı en az 32 karakter olmalı: %d", key)
	}
	// Kurulum bilgisi: kontrol sunucusunun adresi, edge anahtarı ve origin'in RTMP adresi.
	for _, want := range []string{"CONTROL_URL=http://tv.example.com:8000\n", "EDGE_KEY=", "ORIGIN_RTMP=yayin.example.com:1935\n"} {
		if !strings.Contains(e.Setup, want) {
			t.Fatalf("kurulum bilgisinde %q yok:\n%s", want, e.Setup)
		}
	}

	admin.want(admin.post("/api/admin/edges", map[string]any{"name": "Frankfurt", "base_url": "http://x.example.com", "pull_ip": "203.0.113.21"}), http.StatusConflict)

	// Sinyal verince sağlıklı görünür; oturumları sayılır.
	if err := f.store.MarkEdgeSeen(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	tenant := must(f.store.CreateTenant(ctx, "t"))
	ch := must(f.store.CreateChannel(ctx, tenant, "c", "sek"))
	v := must(f.store.CreateViewer(ctx, tenant, "ali", "pw", 5))
	must(f.store.OpenTSSession(ctx, e.ID, v, ch, "c1", "1.1.1.1", idle))
	must(f.store.OpenTSSession(ctx, e.ID, v, ch, "c2", "1.1.1.1", idle))
	must(f.store.OpenTSSession(ctx, local.ID, v, ch, "c3", "1.1.1.1", idle))
	list = admin.edges()
	if len(list) != 2 || !list[1].Healthy || list[1].LastSeenAt == nil || list[1].ActiveSessions != 2 || list[0].ActiveSessions != 1 {
		t.Fatalf("edge listesi: %+v", list)
	}

	// Yalnızca gönderilen alanlar değişir.
	admin.want(admin.patch(pathOf(e.ID), map[string]any{"weight": 250, "enabled": false}), http.StatusOK).into(t, &e)
	if e.Weight != 250 || e.Enabled || e.Name != "Frankfurt" || e.PullIP != "203.0.113.20" || e.BaseURL != "http://edge1.example.com:8090" {
		t.Fatalf("kısmi güncelleme: %+v", e)
	}
	admin.want(admin.patch(pathOf(e.ID), map[string]any{
		"name": "Frankfurt 2", "base_url": "https://edge1.example.com", "control_url": "http://10.0.0.5:8090", "pull_ip": "2001:db8::5",
	}), http.StatusOK).into(t, &e)
	if e.Name != "Frankfurt 2" || e.BaseURL != "https://edge1.example.com" || e.ControlURL != "http://10.0.0.5:8090" || e.PullIP != "2001:db8::5" || e.Weight != 250 {
		t.Fatalf("adres güncellemesi: %+v", e)
	}

	admin.want(admin.delete(pathOf(e.ID)), http.StatusNoContent)
	admin.want(admin.delete(pathOf(e.ID)), http.StatusNotFound)
	admin.want(admin.patch(pathOf(e.ID), map[string]any{"weight": 1}), http.StatusNotFound)
	if list = admin.edges(); len(list) != 1 || list[0].ActiveSessions != 1 {
		t.Fatalf("silme sonrası liste: %+v", list)
	}
}

func pathOf(id int64) string { return "/api/admin/edges/" + itoa(id) }

func itoa(id int64) string {
	const digits = "0123456789"
	if id == 0 {
		return "0"
	}
	var b []byte
	for ; id > 0; id /= 10 {
		b = append([]byte{digits[id%10]}, b...)
	}
	return string(b)
}

func TestLocalEdgeCanBeWeightedOrDisabledButNotDeletedOrReaddressed(t *testing.T) {
	f := setup(t)
	admin := f.admin()
	if _, err := f.store.SyncLocalEdge(context.Background(), "http://ts.example.com", "http://hls.example.com"); err != nil {
		t.Fatal(err)
	}
	local := admin.edges()[0]

	admin.want(admin.delete(pathOf(local.ID)), http.StatusConflict)
	for _, body := range []map[string]any{
		{"base_url": "http://kotu.example.com"},
		{"control_url": "http://kotu.example.com"},
		{"pull_ip": "10.0.0.1"},
	} {
		admin.want(admin.patch(pathOf(local.ID), body), http.StatusConflict)
	}
	var e edgeJSON
	admin.want(admin.patch(pathOf(local.ID), map[string]any{"weight": 10, "enabled": false}), http.StatusOK).into(t, &e)
	if e.Weight != 10 || e.Enabled || e.BaseURL != "http://ts.example.com" || e.HLSBaseURL != "http://hls.example.com" {
		t.Fatalf("yerel edge: %+v", e)
	}
}

func TestEdgeInputIsValidated(t *testing.T) {
	f := setup(t)
	admin := f.admin()
	valid := func() map[string]any {
		return map[string]any{"name": "e1", "base_url": "http://edge1.example.com", "pull_ip": "203.0.113.20"}
	}
	for field, bad := range map[string][]any{
		"name":        {"", "   ", strings.Repeat("a", 101)},
		"base_url":    {"", "edge1.example.com", "ftp://edge1.example.com", "http://edge1.example.com/yol", "http://edge1.example.com?x=1", "http://kullanici:sifre@edge1.example.com", "http://"},
		"control_url": {"edge1", "http://edge1.example.com/yol", "javascript:alert(1)"},
		"pull_ip":     {"", "edge1.example.com", "203.0.113.20/24", "203.0.113.20:1935", "999.1.1.1"},
		"weight":      {0, -1, 1001},
	} {
		for _, value := range bad {
			body := valid()
			body[field] = value
			if r := admin.post("/api/admin/edges", body); r.code != http.StatusBadRequest {
				t.Errorf("%s=%v: durum %d", field, value, r.code)
			}
		}
	}
	if n := len(admin.edges()); n != 1 {
		t.Fatalf("geçersiz istekler edge oluşturdu: %d", n)
	}

	var e edgeJSON
	admin.want(admin.post("/api/admin/edges", valid()), http.StatusCreated).into(t, &e)
	for _, body := range []map[string]any{
		{"weight": 0}, {"weight": 1001}, {"name": " "}, {"base_url": "edge"}, {"control_url": "edge"}, {"pull_ip": "x"},
	} {
		if r := admin.patch(pathOf(e.ID), body); r.code != http.StatusBadRequest {
			t.Errorf("güncelleme %v: durum %d", body, r.code)
		}
	}
}

// Edge yönetimi yalnızca yöneticiye açıktır; yayıncı edge anahtarlarını göremez.
func TestTenantsCannotSeeOrManageEdges(t *testing.T) {
	f := setup(t)
	admin := f.admin()
	var e edgeJSON
	admin.want(admin.post("/api/admin/edges", map[string]any{"name": "e1", "base_url": "http://edge1.example.com", "pull_ip": "203.0.113.20"}), http.StatusCreated).into(t, &e)
	tenant, _, _ := f.newTenant("Yayıncı", "yayinci@example.com")

	for _, r := range []response{
		tenant.get("/api/admin/edges"),
		tenant.post("/api/admin/edges", map[string]any{"name": "e2", "base_url": "http://edge2.example.com", "pull_ip": "203.0.113.21"}),
		tenant.patch(pathOf(e.ID), map[string]any{"enabled": false}),
		tenant.delete(pathOf(e.ID)),
	} {
		if r.code != http.StatusForbidden || strings.Contains(r.body, "EDGE_KEY") {
			t.Errorf("yayıncı edge ucuna erişti: durum %d %s", r.code, r.body)
		}
	}
	for _, r := range []response{f.anon().get("/api/admin/edges"), f.anon().delete(pathOf(e.ID))} {
		if r.code != http.StatusUnauthorized {
			t.Errorf("girişsiz istek: durum %d", r.code)
		}
	}
	if n := len(admin.edges()); n != 2 {
		t.Fatalf("edge kayıtları değişmemeliydi: %d", n)
	}
}

func TestTenantSessionListShowsTheServerName(t *testing.T) {
	f := setup(t)
	admin := f.admin()
	var e edgeJSON
	admin.want(admin.post("/api/admin/edges", map[string]any{"name": "Frankfurt", "base_url": "http://edge1.example.com", "pull_ip": "203.0.113.20"}), http.StatusCreated).into(t, &e)
	tenant, info, _ := f.newTenant("Yayıncı", "yayinci@example.com")
	ch, v := tenant.channel("Kanal"), tenant.viewer("ali")
	ctx := context.Background()
	must(f.store.MarkLive(ctx, ch.ID, "yayinci-1"))
	_ = info
	must(f.store.OpenTSSession(ctx, e.ID, v.ID, ch.ID, "c1", "1.1.1.1", idle))

	var sessions []struct {
		Edge string `json:"edge"`
	}
	tenant.want(tenant.get("/api/tenant/sessions"), http.StatusOK).into(t, &sessions)
	if len(sessions) != 1 || sessions[0].Edge != "Frankfurt" {
		t.Fatalf("oturum listesi: %+v", sessions)
	}
}
