package panelapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"streamhub/internal/panelapi"
	"streamhub/internal/passhash"
	"streamhub/internal/ratelimit"
	"streamhub/internal/session"
	"streamhub/internal/session/sessiontest"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

const (
	adminEmail    = "yonetici@example.com"
	adminPassword = "yonetici-sifresi-1"
	idle          = 30 * time.Second
	maxLoginFails = 5
)

type fixture struct {
	t        *testing.T
	store    *store.Store
	sessions *session.Manager
	mux      *http.ServeMux
	ts       *sessiontest.FakeSRS // .ts dağıtıcısı
	origin   *sessiontest.FakeSRS
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func setup(t *testing.T) *fixture { return setupWith(t, false) }

func setupWith(t *testing.T, secureCookie bool) *fixture {
	f := &fixture{t: t, store: testdb.New(t), mux: http.NewServeMux(), ts: &sessiontest.FakeSRS{}, origin: &sessiontest.FakeSRS{}}
	must(f.store.CreateAdmin(context.Background(), adminEmail, must(passhash.Hash(adminPassword))))
	f.sessions = session.New(f.store, sessiontest.For(f.ts), f.origin, idle, 6*time.Hour)
	limiter := ratelimit.New(maxLoginFails, time.Minute, time.Now)
	panelapi.New(f.store, f.sessions, limiter, panelapi.Config{
		SessionTTL:       time.Hour,
		SecureCookie:     secureCookie,
		IngestURL:        "rtmp://yayin.example.com/live",
		PublicBaseURL:    "http://tv.example.com:8000",
		Idle:             idle,
		EdgeHealthWindow: 15 * time.Second,
	}).Register(f.mux)
	return f
}

// client, bir tarayıcı oturumunu taklit eder: çerezi saklar ve değişiklik isteklerine panel başlığını ekler.
type client struct {
	f      *fixture
	cookie *http.Cookie
	remote string
	noCSRF bool
}

func (f *fixture) anon() *client { return &client{f: f, remote: "192.0.2.10:1000"} }

type response struct {
	code int
	body string
	rec  *httptest.ResponseRecorder
}

func (r response) into(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("JSON çözülemedi: %v\n%s", err, r.body)
	}
}

func (c *client) do(method, path string, body any) response {
	c.f.t.Helper()
	var payload string
	if body != nil {
		if s, ok := body.(string); ok {
			payload = s
		} else {
			payload = string(must(json.Marshal(body)))
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	req.RemoteAddr = c.remote
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && !c.noCSRF {
		req.Header.Set("X-StreamHub-Panel", "1")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.f.mux.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "sh_panel" {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	if strings.Contains(rec.Body.String(), "argon2id") || strings.Contains(rec.Body.String(), "password_hash") {
		c.f.t.Fatalf("%s %s yanıtı şifre özeti içeriyor: %s", method, path, rec.Body.String())
	}
	return response{code: rec.Code, body: rec.Body.String(), rec: rec}
}

func (c *client) get(path string) response          { return c.do(http.MethodGet, path, nil) }
func (c *client) post(path string, b any) response  { return c.do(http.MethodPost, path, b) }
func (c *client) patch(path string, b any) response { return c.do(http.MethodPatch, path, b) }
func (c *client) delete(path string) response       { return c.do(http.MethodDelete, path, nil) }
func (c *client) want(r response, code int) response {
	c.f.t.Helper()
	if r.code != code {
		c.f.t.Fatalf("durum %d, beklenen %d: %s", r.code, code, r.body)
	}
	return r
}

func (f *fixture) login(email, password string) *client {
	f.t.Helper()
	c := f.anon()
	c.want(c.post("/api/login", map[string]string{"email": email, "password": password}), http.StatusOK)
	if c.cookie == nil {
		f.t.Fatal("giriş çerez vermedi")
	}
	return c
}

func (f *fixture) admin() *client { return f.login(adminEmail, adminPassword) }

type tenantJSON struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Status string `json:"status"`
	Quotas struct {
		MaxChannels    int `json:"max_channels"`
		MaxViewers     int `json:"max_viewers"`
		MaxConnections int `json:"max_connections"`
	} `json:"quotas"`
	Channels       int `json:"channels"`
	Viewers        int `json:"viewers"`
	LiveChannels   int `json:"live_channels"`
	ActiveSessions int `json:"active_sessions"`
}

// newTenant, yönetici olarak bir yayıncı oluşturur ve o yayıncı olarak giriş yapmış bir istemci döner.
func (f *fixture) newTenant(name, email string) (*client, tenantJSON, string) {
	f.t.Helper()
	a := f.admin()
	var created struct {
		Tenant   tenantJSON `json:"tenant"`
		Password string     `json:"password"`
	}
	a.want(a.post("/api/admin/tenants", map[string]string{"name": name, "email": email}), http.StatusCreated).into(f.t, &created)
	if len(created.Password) < 16 {
		f.t.Fatalf("üretilen şifre çok kısa: %d karakter", len(created.Password))
	}
	return f.login(email, created.Password), created.Tenant, created.Password
}

type channelJSON struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CategoryID *int64 `json:"category_id"`
	LogoURL    string `json:"logo_url"`
	Live       bool   `json:"live"`
	StreamKey  string `json:"stream_key"`
	IngestURL  string `json:"ingest_url"`
}

type viewerJSON struct {
	ID             int64   `json:"id"`
	Username       string  `json:"username"`
	Password       string  `json:"password"`
	Status         string  `json:"status"`
	ExpiresAt      *string `json:"expires_at"`
	MaxConnections int     `json:"max_connections"`
	PlaylistURL    string  `json:"playlist_url"`
}

type idJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (c *client) channel(name string) channelJSON {
	var ch channelJSON
	c.want(c.post("/api/tenant/channels", map[string]any{"name": name}), http.StatusCreated).into(c.f.t, &ch)
	return ch
}

func (c *client) viewer(username string) viewerJSON {
	var v viewerJSON
	c.want(c.post("/api/tenant/viewers", map[string]any{"username": username}), http.StatusCreated).into(c.f.t, &v)
	return v
}

// --- giriş ve oturum ---

func TestLoginSetsAHardenedCookie(t *testing.T) {
	for _, secure := range []bool{false, true} {
		f := setupWith(t, secure)
		c := f.admin()
		ck := c.cookie
		if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Secure != secure || ck.Path != "/" || ck.MaxAge <= 0 || len(ck.Value) < 40 {
			t.Fatalf("çerez yeterince korunmuyor: %+v", ck)
		}
		var me struct{ Role, Email string }
		c.want(c.get("/api/me"), http.StatusOK).into(t, &me)
		if me.Role != "admin" || me.Email != adminEmail {
			t.Fatalf("beklenmeyen hesap: %+v", me)
		}
	}
}

func TestLoginFailuresLookTheSame(t *testing.T) {
	f := setup(t)
	f.newTenant("A", "a@example.com")
	_, suspended, _ := f.newTenant("Askıda", "askida@example.com")
	if err := f.store.SetTenantStatus(context.Background(), suspended.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, creds := range [][2]string{
		{adminEmail, "yanlis-sifre-123"}, {"a@example.com", "yanlis-sifre-123"}, {"askida@example.com", "yanlis-sifre-123"},
		{"yok@example.com", "herhangi-bir-sey"}, {"", ""},
	} {
		c := f.anon()
		c.remote = fmt.Sprintf("198.51.100.%d:1", len(bodies)+1)
		r := c.want(c.post("/api/login", map[string]string{"email": creds[0], "password": creds[1]}), http.StatusUnauthorized)
		if c.cookie != nil {
			t.Fatal("başarısız giriş çerez vermemeli")
		}
		bodies = append(bodies, r.body)
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("hata yanıtları hesabın var olup olmadığını ele vermemeli:\n%s\n%s", bodies[0], b)
		}
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	f := setup(t)
	c := f.admin()
	old := c.cookie
	c.want(c.post("/api/logout", nil), http.StatusNoContent)
	if c.cookie != nil {
		t.Fatal("çıkış çerezi silmeli")
	}
	c.cookie = old
	c.want(c.get("/api/me"), http.StatusUnauthorized)
}

func TestTooManyFailedLoginsAreBlocked(t *testing.T) {
	f := setup(t)
	c := f.anon()
	for i := 0; i < maxLoginFails; i++ {
		c.want(c.post("/api/login", map[string]string{"email": adminEmail, "password": "yanlis-sifre-123"}), http.StatusUnauthorized)
	}
	c.want(c.post("/api/login", map[string]string{"email": adminEmail, "password": adminPassword}), http.StatusTooManyRequests)
	other := f.anon()
	other.remote = "203.0.113.5:1"
	other.want(other.post("/api/login", map[string]string{"email": adminEmail, "password": adminPassword}), http.StatusOK)
}

func TestProtectedRoutesRequireLogin(t *testing.T) {
	f := setup(t)
	c := f.anon()
	routes := []string{
		"GET /api/me", "POST /api/password", "POST /api/logout",
		"GET /api/admin/stats", "GET /api/admin/tenants", "POST /api/admin/tenants", "GET /api/admin/tenants/1",
		"PATCH /api/admin/tenants/1", "POST /api/admin/tenants/1/reset-password",
		"GET /api/tenant/overview", "GET /api/tenant/categories", "POST /api/tenant/categories",
		"PATCH /api/tenant/categories/1", "DELETE /api/tenant/categories/1",
		"GET /api/tenant/channels", "POST /api/tenant/channels", "PATCH /api/tenant/channels/1", "DELETE /api/tenant/channels/1",
		"POST /api/tenant/channels/1/regenerate-secret",
		"GET /api/tenant/viewers", "POST /api/tenant/viewers", "PATCH /api/tenant/viewers/1", "DELETE /api/tenant/viewers/1",
		"POST /api/tenant/viewers/1/regenerate-password", "GET /api/tenant/sessions",
	}
	for _, route := range routes {
		method, path, _ := strings.Cut(route, " ")
		if r := c.do(method, path, map[string]string{}); r.code != http.StatusUnauthorized {
			t.Errorf("%s: durum %d, beklenen 401", route, r.code)
		}
	}
}

func TestRolesAreSeparated(t *testing.T) {
	f := setup(t)
	tenant, info, _ := f.newTenant("A", "a@example.com")
	admin := f.admin()
	for _, path := range []string{"/api/admin/stats", "/api/admin/tenants", fmt.Sprintf("/api/admin/tenants/%d", info.ID)} {
		tenant.want(tenant.get(path), http.StatusForbidden)
	}
	tenant.want(tenant.patch(fmt.Sprintf("/api/admin/tenants/%d", info.ID), map[string]any{"quotas": map[string]int{"max_channels": 999, "max_viewers": 999, "max_connections": 999}}), http.StatusForbidden)
	tenant.want(tenant.post("/api/admin/tenants", map[string]string{"name": "X", "email": "x@example.com"}), http.StatusForbidden)
	for _, path := range []string{"/api/tenant/overview", "/api/tenant/channels", "/api/tenant/viewers", "/api/tenant/sessions"} {
		admin.want(admin.get(path), http.StatusForbidden)
	}
	if q := must(f.store.TenantQuotas(context.Background(), info.ID)); q.MaxChannels != 10 {
		t.Fatalf("yayıncı kendi kotasını değiştirememeli: %+v", q)
	}
}

func TestChangesRequireThePanelHeader(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	stranger := f.anon()
	stranger.noCSRF = true
	stranger.want(stranger.post("/api/login", map[string]string{"email": adminEmail, "password": adminPassword}), http.StatusForbidden)
	if stranger.cookie != nil {
		t.Fatal("başlıksız giriş isteği oturum açmamalı")
	}

	tenant.noCSRF = true
	tenant.want(tenant.post("/api/tenant/channels", map[string]any{"name": "K"}), http.StatusForbidden)
	tenant.want(tenant.post("/api/logout", nil), http.StatusForbidden)
	tenant.want(tenant.get("/api/tenant/channels"), http.StatusOK)
	tenant.noCSRF = false
	var list []channelJSON
	tenant.want(tenant.get("/api/tenant/channels"), http.StatusOK).into(t, &list)
	if len(list) != 0 {
		t.Fatalf("başlıksız istek kanal oluşturmamalı: %+v", list)
	}
}

func TestPasswordChange(t *testing.T) {
	f := setup(t)
	c, _, password := f.newTenant("A", "a@example.com")
	second := f.login("a@example.com", password)

	c.want(c.post("/api/password", map[string]string{"current": "yanlis-sifre-123", "new": "yepyeni-sifre-456"}), http.StatusForbidden)
	c.want(c.post("/api/password", map[string]string{"current": password, "new": "kisa"}), http.StatusBadRequest)
	c.want(c.post("/api/password", map[string]string{"current": password, "new": "yepyeni-sifre-456"}), http.StatusNoContent)

	c.want(c.get("/api/me"), http.StatusOK)
	second.want(second.get("/api/me"), http.StatusUnauthorized)
	anon := f.anon()
	anon.want(anon.post("/api/login", map[string]string{"email": "a@example.com", "password": password}), http.StatusUnauthorized)
	f.login("a@example.com", "yepyeni-sifre-456")
}

// --- yönetici ---

func TestAdminManagesTenants(t *testing.T) {
	f := setup(t)
	tenant, info, password := f.newTenant("Kanal A", "a@example.com")
	admin := f.admin()
	if info.Name != "Kanal A" || info.Email != "a@example.com" || info.Status != "active" || info.Quotas.MaxChannels != 10 {
		t.Fatalf("beklenmeyen yayıncı: %+v", info)
	}
	tenant.channel("K1")
	tenant.viewer("izleyici1")

	for name, body := range map[string]map[string]string{
		"aynı e-posta":       {"name": "B", "email": "A@example.com"},
		"yönetici e-postası": {"name": "B", "email": adminEmail},
	} {
		if r := admin.post("/api/admin/tenants", body); r.code != http.StatusConflict {
			t.Errorf("%s: durum %d", name, r.code)
		}
	}
	for name, body := range map[string]map[string]string{
		"boş ad":        {"name": "  ", "email": "b@example.com"},
		"bozuk e-posta": {"name": "B", "email": "e-posta-degil"},
		"uzun ad":       {"name": strings.Repeat("a", 101), "email": "b@example.com"},
	} {
		if r := admin.post("/api/admin/tenants", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: durum %d", name, r.code)
		}
	}

	var list []tenantJSON
	admin.want(admin.get("/api/admin/tenants"), http.StatusOK).into(t, &list)
	if len(list) != 1 || list[0].Channels != 1 || list[0].Viewers != 1 {
		t.Fatalf("liste: %+v", list)
	}
	var stats struct{ Tenants, Channels, Viewers int }
	admin.want(admin.get("/api/admin/stats"), http.StatusOK).into(t, &stats)
	if stats.Tenants != 1 || stats.Channels != 1 || stats.Viewers != 1 {
		t.Fatalf("istatistik: %+v", stats)
	}

	path := fmt.Sprintf("/api/admin/tenants/%d", info.ID)
	var updated tenantJSON
	admin.want(admin.patch(path, map[string]any{
		"name": "Kanal A+", "email": "yeni@example.com",
		"quotas": map[string]int{"max_channels": 1, "max_viewers": 2, "max_connections": 3},
	}), http.StatusOK).into(t, &updated)
	if updated.Name != "Kanal A+" || updated.Email != "yeni@example.com" || updated.Quotas.MaxChannels != 1 || updated.Quotas.MaxConnections != 3 {
		t.Fatalf("güncelleme: %+v", updated)
	}
	admin.want(admin.patch(path, map[string]any{"quotas": map[string]int{"max_channels": -1, "max_viewers": 2, "max_connections": 3}}), http.StatusBadRequest)
	admin.want(admin.patch(path, map[string]any{"status": "silindi"}), http.StatusBadRequest)
	admin.want(admin.get("/api/admin/tenants/999999"), http.StatusNotFound)
	admin.want(admin.patch("/api/admin/tenants/abc", map[string]any{"name": "x"}), http.StatusNotFound)

	// Kota artık 1 kanal: ikinci kanal reddedilir.
	tenant.want(tenant.post("/api/tenant/channels", map[string]any{"name": "K2"}), http.StatusConflict)

	// Şifre sıfırlama: eski şifre ve eski oturum geçersiz olur.
	var reset struct{ Password string }
	admin.want(admin.post(path+"/reset-password", nil), http.StatusOK).into(t, &reset)
	if reset.Password == password || len(reset.Password) < 16 {
		t.Fatal("yeni şifre üretilmeli")
	}
	tenant.want(tenant.get("/api/me"), http.StatusUnauthorized)
	anon := f.anon()
	anon.want(anon.post("/api/login", map[string]string{"email": "yeni@example.com", "password": password}), http.StatusUnauthorized)
	f.login("yeni@example.com", reset.Password)
}

func TestSuspendedTenantIsLockedOut(t *testing.T) {
	f := setup(t)
	tenant, info, password := f.newTenant("A", "a@example.com")
	admin := f.admin()
	path := fmt.Sprintf("/api/admin/tenants/%d", info.ID)

	admin.want(admin.patch(path, map[string]any{"status": "suspended"}), http.StatusOK)
	// Askıya alma panel oturumlarını siler; yeniden etkinleştirilince eski çerez geri gelmez.
	tenant.want(tenant.get("/api/tenant/channels"), http.StatusUnauthorized)
	anon := f.anon()
	anon.want(anon.post("/api/login", map[string]string{"email": "a@example.com", "password": password}), http.StatusForbidden)

	admin.want(admin.patch(path, map[string]any{"status": "active"}), http.StatusOK)
	tenant.want(tenant.get("/api/tenant/channels"), http.StatusUnauthorized)
	f.login("a@example.com", password)
}

// --- yayıncı ---

func TestOverview(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("Kanal A", "a@example.com")
	tenant.channel("K1")
	var o struct {
		Name      string `json:"name"`
		IngestURL string `json:"ingest_url"`
		XtreamURL string `json:"xtream_url"`
		Quotas    struct {
			MaxChannels int `json:"max_channels"`
		} `json:"quotas"`
		Usage struct {
			Channels, Viewers, Connections int
		} `json:"usage"`
	}
	tenant.want(tenant.get("/api/tenant/overview"), http.StatusOK).into(t, &o)
	if o.Name != "Kanal A" || o.IngestURL != "rtmp://yayin.example.com/live" || o.XtreamURL != "http://tv.example.com:8000" || o.Quotas.MaxChannels != 10 || o.Usage.Channels != 1 {
		t.Fatalf("genel bakış: %+v", o)
	}
}

func TestChannelLifecycle(t *testing.T) {
	f := setup(t)
	tenant, info, _ := f.newTenant("A", "a@example.com")
	var cat idJSON
	tenant.want(tenant.post("/api/tenant/categories", map[string]string{"name": "Spor"}), http.StatusCreated).into(t, &cat)

	ch := tenant.channel("Kanal 1")
	secret := must(f.store.ChannelByID(context.Background(), ch.ID)).StreamSecret
	if ch.StreamKey != fmt.Sprintf("%d?secret=%s", ch.ID, secret) || len(secret) < 32 || ch.IngestURL != "rtmp://yayin.example.com/live" || ch.Live {
		t.Fatalf("OBS ayarları: %+v", ch)
	}

	path := fmt.Sprintf("/api/tenant/channels/%d", ch.ID)
	var updated channelJSON
	tenant.want(tenant.patch(path, map[string]any{"name": "Kanal 1 HD", "category_id": cat.ID, "logo_url": "https://cdn.example/logo.png"}), http.StatusOK).into(t, &updated)
	if updated.Name != "Kanal 1 HD" || updated.CategoryID == nil || *updated.CategoryID != cat.ID || updated.LogoURL != "https://cdn.example/logo.png" {
		t.Fatalf("güncelleme: %+v", updated)
	}
	for name, body := range map[string]map[string]any{
		"boş ad":           {"name": ""},
		"betik logo":       {"name": "K", "logo_url": "javascript:alert(1)"},
		"göreli logo":      {"name": "K", "logo_url": "/logo.png"},
		"olmayan kategori": {"name": "K", "category_id": 999999},
		"bilinmeyen alan":  {"name": "K", "tenant_id": 1},
	} {
		if r := tenant.patch(path, body); r.code != http.StatusBadRequest && r.code != http.StatusNotFound {
			t.Errorf("%s: durum %d", name, r.code)
		}
	}
	if must(f.store.ChannelByID(context.Background(), ch.ID)).Name != "Kanal 1 HD" {
		t.Fatal("reddedilen güncellemeler kanalı değiştirmemeli")
	}

	// Yayındayken anahtar yenilenirse süren yayın kesilir ve eski anahtar geçersiz olur.
	ctx := context.Background()
	must(f.store.MarkLive(ctx, ch.ID, "yayinci-1"))
	var regenerated channelJSON
	tenant.want(tenant.post(path+"/regenerate-secret", nil), http.StatusOK).into(t, &regenerated)
	if regenerated.StreamKey == ch.StreamKey || f.origin.Kicked() != "[yayinci-1]" {
		t.Fatalf("anahtar yenilenmeli ve yayın kesilmeli: %s / %s", regenerated.StreamKey, f.origin.Kicked())
	}

	// Kanal silinince yayıncı ve .ts izleyicileri kesilir.
	v := tenant.viewer("izleyici1")
	must(f.store.OpenTSSession(ctx, testdb.LocalEdge(f.t), v.ID, ch.ID, "izleyen-1", "1.1.1.1", idle))
	tenant.want(tenant.delete(path), http.StatusNoContent)
	if f.origin.Kicked() != "[yayinci-1 yayinci-1]" || f.ts.Kicked() != "[izleyen-1]" {
		t.Fatalf("silinen kanalın bağlantıları kesilmeli: origin=%s ts=%s", f.origin.Kicked(), f.ts.Kicked())
	}
	tenant.want(tenant.get(path), http.StatusMethodNotAllowed)
	var list []channelJSON
	tenant.want(tenant.get("/api/tenant/channels"), http.StatusOK).into(t, &list)
	if len(list) != 0 {
		t.Fatalf("kanal silinmeliydi: %+v", list)
	}
	_ = info
}

func TestViewerLifecycle(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	other, _, _ := f.newTenant("B", "b@example.com")
	other.viewer("alinmis")

	v := tenant.viewer("izleyici_1")
	if len(v.Password) < 16 || v.Status != "active" || v.MaxConnections != 1 || v.ExpiresAt != nil {
		t.Fatalf("yeni izleyici: %+v", v)
	}
	if want := "http://tv.example.com:8000/get.php?username=izleyici_1&password=" + v.Password + "&type=m3u_plus&output=ts"; v.PlaylistURL != want {
		t.Fatalf("M3U adresi %q, beklenen %q", v.PlaylistURL, want)
	}

	for name, username := range map[string]string{
		"kısa": "ab", "boşluklu": "ali veli", "eğik çizgi": "ali/veli", "ayrılmış hls": "hls", "ayrılmış live": "LIVE",
		"php uzantısı": "get.php", "yalnızca nokta": "...", "uzun": strings.Repeat("a", 33), "Türkçe": "çağrı",
	} {
		if r := tenant.post("/api/tenant/viewers", map[string]any{"username": username}); r.code != http.StatusBadRequest {
			t.Errorf("%s (%q): durum %d", name, username, r.code)
		}
	}
	tenant.want(tenant.post("/api/tenant/viewers", map[string]any{"username": "alinmis"}), http.StatusConflict)
	tenant.want(tenant.post("/api/tenant/viewers", map[string]any{"username": "izleyici_1"}), http.StatusConflict)

	path := fmt.Sprintf("/api/tenant/viewers/%d", v.ID)
	var updated viewerJSON
	tenant.want(tenant.patch(path, map[string]any{"status": "suspended", "expires_at": "2030-01-02T03:04:05Z", "max_connections": 3}), http.StatusOK).into(t, &updated)
	if updated.Status != "suspended" || updated.MaxConnections != 3 || updated.ExpiresAt == nil || *updated.ExpiresAt != "2030-01-02T03:04:05Z" {
		t.Fatalf("güncelleme: %+v", updated)
	}
	tenant.want(tenant.patch(path, map[string]any{"status": "active", "expires_at": nil, "max_connections": 1}), http.StatusOK).into(t, &updated)
	if updated.ExpiresAt != nil {
		t.Fatal("bitiş tarihi kaldırılabilmeli")
	}
	for name, body := range map[string]map[string]any{
		"geçersiz durum": {"status": "silindi", "max_connections": 1},
		"sıfır bağlantı": {"status": "active", "max_connections": 0},
		"çok bağlantı":   {"status": "active", "max_connections": 1000},
		"bozuk tarih":    {"status": "active", "max_connections": 1, "expires_at": "yarın"},
	} {
		if r := tenant.patch(path, body); r.code != http.StatusBadRequest {
			t.Errorf("%s: durum %d", name, r.code)
		}
	}

	var regenerated viewerJSON
	tenant.want(tenant.post(path+"/regenerate-password", nil), http.StatusOK).into(t, &regenerated)
	if regenerated.Password == v.Password || len(regenerated.Password) < 16 {
		t.Fatal("şifre yenilenmeli")
	}

	ctx := context.Background()
	ch := tenant.channel("K")
	must(f.store.OpenTSSession(ctx, testdb.LocalEdge(f.t), v.ID, ch.ID, "izleyen-1", "1.1.1.1", idle))
	var sessions []struct{ Viewer, Channel, Kind, IP string }
	tenant.want(tenant.get("/api/tenant/sessions"), http.StatusOK).into(t, &sessions)
	if len(sessions) != 1 || sessions[0].Viewer != "izleyici_1" || sessions[0].Channel != "K" || sessions[0].Kind != "ts" {
		t.Fatalf("oturumlar: %+v", sessions)
	}
	other.want(other.get("/api/tenant/sessions"), http.StatusOK).into(t, &sessions)
	if len(sessions) != 0 {
		t.Fatalf("başka yayıncı bu oturumu görmemeli: %+v", sessions)
	}

	tenant.want(tenant.delete(path), http.StatusNoContent)
	if f.ts.Kicked() != "[izleyen-1]" {
		t.Fatalf("silinen izleyicinin bağlantısı kesilmeli: %s", f.ts.Kicked())
	}
	tenant.want(tenant.delete(path), http.StatusNotFound)
}

func TestCategories(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	var cat idJSON
	tenant.want(tenant.post("/api/tenant/categories", map[string]string{"name": " Spor "}), http.StatusCreated).into(t, &cat)
	if cat.Name != "Spor" {
		t.Fatalf("ad kırpılmalı: %q", cat.Name)
	}
	tenant.want(tenant.post("/api/tenant/categories", map[string]string{"name": ""}), http.StatusBadRequest)
	path := fmt.Sprintf("/api/tenant/categories/%d", cat.ID)
	tenant.want(tenant.patch(path, map[string]string{"name": "Spor HD"}), http.StatusOK)
	var list []idJSON
	tenant.want(tenant.get("/api/tenant/categories"), http.StatusOK).into(t, &list)
	if len(list) != 1 || list[0].Name != "Spor HD" {
		t.Fatalf("kategoriler: %+v", list)
	}
	tenant.want(tenant.delete(path), http.StatusNoContent)
	tenant.want(tenant.delete(path), http.StatusNotFound)
}

// Bir yayıncı, başka yayıncının hiçbir kaydını kimliğini bilse bile göremez ve değiştiremez.
func TestTenantsCannotTouchEachOthersRecords(t *testing.T) {
	f := setup(t)
	a, _, _ := f.newTenant("A", "a@example.com")
	b, _, _ := f.newTenant("B", "b@example.com")
	var cat idJSON
	b.want(b.post("/api/tenant/categories", map[string]string{"name": "B-kategori"}), http.StatusCreated).into(t, &cat)
	ch := b.channel("B-kanal")
	v := b.viewer("b_izleyici")
	// B'nin kanalı yayında ve izleniyor: saldırılar bu bağlantılara da dokunamamalı.
	bg := context.Background()
	must(f.store.MarkLive(bg, ch.ID, "b-yayinci"))
	must(f.store.OpenTSSession(bg, testdb.LocalEdge(f.t), v.ID, ch.ID, "b-izleyen", "9.9.9.9", idle))

	attacks := []response{
		a.patch(fmt.Sprintf("/api/tenant/channels/%d", ch.ID), map[string]any{"name": "ele geçirildi"}),
		a.delete(fmt.Sprintf("/api/tenant/channels/%d", ch.ID)),
		a.post(fmt.Sprintf("/api/tenant/channels/%d/regenerate-secret", ch.ID), nil),
		a.patch(fmt.Sprintf("/api/tenant/viewers/%d", v.ID), map[string]any{"status": "suspended", "max_connections": 1}),
		a.delete(fmt.Sprintf("/api/tenant/viewers/%d", v.ID)),
		a.post(fmt.Sprintf("/api/tenant/viewers/%d/regenerate-password", v.ID), nil),
		a.patch(fmt.Sprintf("/api/tenant/categories/%d", cat.ID), map[string]string{"name": "x"}),
		a.delete(fmt.Sprintf("/api/tenant/categories/%d", cat.ID)),
	}
	for i, r := range attacks {
		if r.code != http.StatusNotFound {
			t.Errorf("saldırı %d: durum %d, beklenen 404", i, r.code)
		}
		if strings.Contains(r.body, "B-kanal") || strings.Contains(r.body, v.Password) {
			t.Errorf("saldırı %d yanıtı başka yayıncının verisini sızdırıyor: %s", i, r.body)
		}
	}
	mine := a.channel("A-kanal")
	if r := a.patch(fmt.Sprintf("/api/tenant/channels/%d", mine.ID), map[string]any{"name": "A-kanal", "category_id": cat.ID}); r.code != http.StatusNotFound && r.code != http.StatusBadRequest {
		t.Errorf("başka yayıncının kategorisi atanamamalı: durum %d", r.code)
	}

	for _, path := range []string{"/api/tenant/channels", "/api/tenant/viewers", "/api/tenant/categories", "/api/tenant/sessions"} {
		if body := a.want(a.get(path), http.StatusOK).body; strings.Contains(body, "B-") || strings.Contains(body, "b_izleyici") {
			t.Errorf("%s başka yayıncının kaydını listeliyor: %s", path, body)
		}
	}

	ctx := context.Background()
	if c := must(f.store.ChannelByID(ctx, ch.ID)); c.Name != "B-kanal" || fmt.Sprintf("%d?secret=%s", c.ID, c.StreamSecret) != ch.StreamKey {
		t.Fatalf("B'nin kanalı değişmemeli: %+v", c)
	}
	if got := must(f.store.ViewerByID(ctx, v.ID)); got.Status != "active" || got.Password != v.Password {
		t.Fatalf("B'nin izleyicisi değişmemeli: %+v", got)
	}
	if cats := must(f.store.CategoriesByTenant(ctx, must(f.store.TenantByEmail(ctx, "b@example.com")).ID)); len(cats) != 1 || cats[0].Name != "B-kategori" {
		t.Fatalf("B'nin kategorisi değişmemeli: %+v", cats)
	}
	if f.origin.Kicked() != "[]" || f.ts.Kicked() != "[]" {
		t.Fatalf("reddedilen istekler hiçbir bağlantıyı kesmemeli: origin=%s ts=%s", f.origin.Kicked(), f.ts.Kicked())
	}
	if n := testdb.Count(t, `SELECT count(*) FROM pending_kicks`); n != 0 {
		t.Fatalf("reddedilen istekler kesme kuyruğuna kayıt eklememeli: %d", n)
	}
	if n := must(f.store.ActiveSessionCount(ctx, v.ID, idle)); n != 1 {
		t.Fatalf("B'nin izleyicisinin oturumu sürmeli: %d", n)
	}
}

func TestAdminPasswordChangeEndsOtherAdminSessions(t *testing.T) {
	f := setup(t)
	first, second := f.admin(), f.admin()
	first.want(first.post("/api/password", map[string]string{"current": adminPassword, "new": "yeni-yonetici-sifresi"}), http.StatusNoContent)
	first.want(first.get("/api/admin/stats"), http.StatusOK)
	second.want(second.get("/api/admin/stats"), http.StatusUnauthorized)
	f.login(adminEmail, "yeni-yonetici-sifresi")
}

func TestExpiredSessionIsRejected(t *testing.T) {
	f := setup(t)
	c := f.admin()
	testdb.Exec(t, `UPDATE panel_sessions SET expires_at = now() - interval '1 second'`)
	c.want(c.get("/api/me"), http.StatusUnauthorized)
}

// Mevcut şifre denemeleri de sınırlıdır: açık bir oturumu ele geçiren kişi şifreyi deneyerek bulamaz.
func TestWrongCurrentPasswordAttemptsAreLimited(t *testing.T) {
	f := setup(t)
	c, _, password := f.newTenant("A", "a@example.com")
	for i := 0; i < maxLoginFails; i++ {
		c.want(c.post("/api/password", map[string]string{"current": "yanlis-sifre-123", "new": "yepyeni-sifre-456"}), http.StatusForbidden)
	}
	c.want(c.post("/api/password", map[string]string{"current": password, "new": "yepyeni-sifre-456"}), http.StatusTooManyRequests)
	f.login("a@example.com", password)
}

// Yalnızca gönderilen alanlar değişir: bitiş tarihini düzenlemek askıdaki izleyiciyi etkinleştirmez.
func TestViewerPatchChangesOnlyGivenFields(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	v := tenant.viewer("izleyici1")
	path := fmt.Sprintf("/api/tenant/viewers/%d", v.ID)
	var got viewerJSON

	tenant.want(tenant.patch(path, map[string]any{"status": "suspended"}), http.StatusOK).into(t, &got)
	tenant.want(tenant.patch(path, map[string]any{"expires_at": "2031-05-06T07:08:09Z", "max_connections": 4}), http.StatusOK).into(t, &got)
	if got.Status != "suspended" || got.MaxConnections != 4 || got.ExpiresAt == nil || *got.ExpiresAt != "2031-05-06T07:08:09Z" {
		t.Fatalf("askı durumu korunmalı, diğer alanlar değişmeli: %+v", got)
	}
	tenant.want(tenant.patch(path, map[string]any{"max_connections": 2}), http.StatusOK).into(t, &got)
	if got.ExpiresAt == nil || got.MaxConnections != 2 {
		t.Fatalf("gönderilmeyen bitiş tarihi korunmalı: %+v", got)
	}
	tenant.want(tenant.patch(path, map[string]any{"expires_at": nil}), http.StatusOK).into(t, &got)
	if got.ExpiresAt != nil || got.Status != "suspended" {
		t.Fatalf("null bitiş tarihini kaldırmalı: %+v", got)
	}
}

func TestCreateViewerWithExpiryAndLimit(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	var v viewerJSON
	tenant.want(tenant.post("/api/tenant/viewers", map[string]any{"username": "sureli", "max_connections": 3, "expires_at": "2031-05-06T07:08:09Z"}), http.StatusCreated).into(t, &v)
	if v.MaxConnections != 3 || v.ExpiresAt == nil || *v.ExpiresAt != "2031-05-06T07:08:09Z" {
		t.Fatalf("yeni izleyici: %+v", v)
	}
	tenant.want(tenant.post("/api/tenant/viewers", map[string]any{"username": "bozuk", "expires_at": "yarın"}), http.StatusBadRequest)
	var list []viewerJSON
	tenant.want(tenant.get("/api/tenant/viewers"), http.StatusOK).into(t, &list)
	if len(list) != 1 {
		t.Fatalf("reddedilen istek izleyici oluşturmamalı: %+v", list)
	}
}

func TestCreateChannelWithForeignCategoryLeavesNothingBehind(t *testing.T) {
	f := setup(t)
	a, _, _ := f.newTenant("A", "a@example.com")
	b, _, _ := f.newTenant("B", "b@example.com")
	var cat idJSON
	b.want(b.post("/api/tenant/categories", map[string]string{"name": "B-kategori"}), http.StatusCreated).into(t, &cat)
	a.want(a.post("/api/tenant/channels", map[string]any{"name": "K", "category_id": cat.ID}), http.StatusNotFound)
	var list []channelJSON
	a.want(a.get("/api/tenant/channels"), http.StatusOK).into(t, &list)
	if len(list) != 0 {
		t.Fatalf("yarım kalmış kanal bırakılmamalı: %+v", list)
	}
}

// Şifresi yenilenen izleyicinin süren izlemeleri kesilir: sızan şifreyle izleyen de düşer.
func TestRegeneratingAViewerPasswordEndsRunningPlayback(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	ctx := context.Background()
	ch := tenant.channel("K")
	v := tenant.viewer("izleyici1")
	tenant.want(tenant.patch(fmt.Sprintf("/api/tenant/viewers/%d", v.ID), map[string]any{"max_connections": 2}), http.StatusOK)
	must(f.store.OpenTSSession(ctx, testdb.LocalEdge(f.t), v.ID, ch.ID, "izleyen-1", "1.1.1.1", idle))
	must(f.store.TouchHLSSession(ctx, testdb.LocalEdge(f.t), v.ID, ch.ID, "hls-1", "1.1.1.1", idle))

	tenant.want(tenant.post(fmt.Sprintf("/api/tenant/viewers/%d/regenerate-password", v.ID), nil), http.StatusOK)
	if f.ts.Kicked() != "[izleyen-1]" {
		t.Fatalf(".ts izlemesi kesilmeli: %s", f.ts.Kicked())
	}
	if _, err := f.store.TouchHLSSession(ctx, testdb.LocalEdge(f.t), v.ID, ch.ID, "hls-1", "1.1.1.1", idle); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("HLS izlemesi reddedilmeli, gelen: %v", err)
	}
}

// Silme sırasında SRS'e ulaşılamazsa işlem yine tamamlanır ve bağlantı sonradan kesilir.
func TestKickFailureOnDeleteIsRetriedLater(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	ctx := context.Background()
	ch := tenant.channel("K")
	v := tenant.viewer("izleyici1")
	must(f.store.OpenTSSession(ctx, testdb.LocalEdge(f.t), v.ID, ch.ID, "izleyen-1", "1.1.1.1", idle))

	f.ts.KickErr = errors.New("SRS yanıt vermiyor")
	tenant.want(tenant.delete(fmt.Sprintf("/api/tenant/viewers/%d", v.ID)), http.StatusNoContent)
	if got := must(f.store.PendingEdgeKicks(ctx, testdb.LocalEdge(f.t))); fmt.Sprint(got) != "[izleyen-1]" {
		t.Fatalf("kesilemeyen bağlantı kuyrukta kalmalı: %v", got)
	}
	f.ts.KickErr = nil
	if err := f.sessions.EnforceOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := must(f.store.PendingEdgeKicks(ctx, testdb.LocalEdge(f.t))); len(got) != 0 {
		t.Fatalf("sonraki geçişte kesilmeli: %v", got)
	}
	if f.ts.Kicked() != "[izleyen-1 izleyen-1]" {
		t.Fatalf("kesme yeniden denenmeli: %s", f.ts.Kicked())
	}
}

func TestMalformedRequests(t *testing.T) {
	f := setup(t)
	tenant, _, _ := f.newTenant("A", "a@example.com")
	for name, body := range map[string]string{
		"bozuk JSON":      `{"name":`,
		"dizi":            `[]`,
		"fazladan veri":   `{"name":"K"}{"name":"L"}`,
		"çok büyük gövde": `{"name":"` + strings.Repeat("a", 70<<10) + `"}`,
	} {
		if r := tenant.post("/api/tenant/channels", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: durum %d", name, r.code)
		}
	}
	for _, path := range []string{"/api/tenant/channels/abc", "/api/tenant/channels/0", "/api/tenant/channels/-1", "/api/tenant/channels/99999999999999999999"} {
		if r := tenant.delete(path); r.code != http.StatusNotFound {
			t.Errorf("%s: durum %d", path, r.code)
		}
	}
}
