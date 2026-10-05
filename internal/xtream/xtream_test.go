package xtream_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"streamhub/internal/auth"
	"streamhub/internal/ratelimit"
	"streamhub/internal/session"
	"streamhub/internal/session/sessiontest"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
	"streamhub/internal/xtream"
)

const maxFailures = 5

var now = time.Date(2026, 10, 5, 12, 30, 45, 0, time.UTC)

// fixture: t1 yayıncısında "Spor" kategorisi, kategorili K1 ve kategorisiz K2 kanalları ve
// "ali" izleyicisi; t2 yayıncısında başka bir kategori, kanal ve "veli" izleyicisi vardır.
type fixture struct {
	t       *testing.T
	store   *store.Store
	mux     *http.ServeMux
	manager *session.Manager
	t1, t2  int64
	spor    int64
	k1, k2  int64
	ali     int64
	other   int64 // t2'nin kategorisi
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func setup(t *testing.T) *fixture {
	ctx := context.Background()
	f := &fixture{t: t, store: testdb.New(t), mux: http.NewServeMux()}
	s := f.store
	f.t1 = must(s.CreateTenant(ctx, "t1"))
	f.t2 = must(s.CreateTenant(ctx, "t2"))
	f.spor = must(s.CreateCategory(ctx, f.t1, "Spor"))
	f.other = must(s.CreateCategory(ctx, f.t2, "Başka"))
	f.k1 = must(s.CreateChannel(ctx, f.t1, "K1", "s1"))
	f.k2 = must(s.CreateChannel(ctx, f.t1, "K2", "s2"))
	foreign := must(s.CreateChannel(ctx, f.t2, "Yabancı", "s3"))
	if err := s.SetChannelCategory(ctx, f.k1, &f.spor); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChannelCategory(ctx, foreign, &f.other); err != nil {
		t.Fatal(err)
	}
	f.ali = must(s.CreateViewer(ctx, f.t1, "ali", "pw", 2))
	must(s.CreateViewer(ctx, f.t2, "veli", "pw2", 1))

	base := must(url.Parse("http://tv.example.com:8000"))
	authn := auth.New(s, ratelimit.New(maxFailures, time.Minute, time.Now), false)
	srs := &sessiontest.FakeSRS{}
	f.manager = session.New(s, sessiontest.For(srs), srs, 30*time.Second, 6*time.Hour)
	xtream.New(s, authn, f.manager, base, func() time.Time { return now }).Register(f.mux)
	return f
}

func (f *fixture) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// api, ali olarak player_api.php çağırır ve gövdeyi döner.
func (f *fixture) api(extra string) string {
	f.t.Helper()
	rec := f.get("/player_api.php?username=ali&password=pw" + extra)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("durum %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		f.t.Fatalf("içerik türü %q", ct)
	}
	return strings.TrimSpace(rec.Body.String())
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("JSON çözülemedi: %v\n%s", err, body)
	}
	return v
}

type account struct {
	UserInfo   map[string]any `json:"user_info"`
	ServerInfo map[string]any `json:"server_info"`
}

func TestAccountInfo(t *testing.T) {
	f := setup(t)
	a := decode[account](t, f.api(""))

	wantUser := map[string]any{
		"username": "ali", "password": "pw", "message": "", "auth": float64(1), "status": "Active",
		"exp_date": nil, "is_trial": "0", "active_cons": "0", "max_connections": "2",
	}
	for k, want := range wantUser {
		if got, ok := a.UserInfo[k]; !ok || got != want {
			t.Errorf("user_info.%s = %#v, beklenen %#v", k, got, want)
		}
	}
	if created, _ := a.UserInfo["created_at"].(string); created == "" || created == "0" {
		t.Errorf("created_at unix metin olmalı: %#v", a.UserInfo["created_at"])
	}
	if got := fmt.Sprint(a.UserInfo["allowed_output_formats"]); got != "[m3u8 ts]" {
		t.Errorf("allowed_output_formats = %s", got)
	}

	wantServer := map[string]any{
		"url": "tv.example.com", "port": "8000", "https_port": "443", "server_protocol": "http",
		"rtmp_port": "1935", "timezone": "UTC",
		"timestamp_now": float64(now.Unix()), "time_now": "2026-10-05 12:30:45",
	}
	for k, want := range wantServer {
		if got, ok := a.ServerInfo[k]; !ok || got != want {
			t.Errorf("server_info.%s = %#v, beklenen %#v", k, got, want)
		}
	}
}

func TestAccountInfoReportsActiveConnections(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.manager.TouchHLS(ctx, testdb.LocalEdge(t), f.ali, f.k1, "aa", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.OpenTS(ctx, testdb.LocalEdge(t), f.ali, f.k2, "c1", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	a := decode[account](t, f.api(""))
	if a.UserInfo["active_cons"] != "2" {
		t.Fatalf("active_cons = %#v, beklenen \"2\"", a.UserInfo["active_cons"])
	}
}

func TestLoginWithPOSTBody(t *testing.T) {
	f := setup(t)
	body := strings.NewReader("username=ali&password=pw&action=get_live_categories")
	req := httptest.NewRequest(http.MethodPost, "/player_api.php", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"category_name":"Spor"`) {
		t.Fatalf("durum %d: %s", rec.Code, rec.Body.String())
	}
}

func TestInvalidLoginIsAuthZero(t *testing.T) {
	f := setup(t)
	for _, q := range []string{"username=ali&password=yanlis", "username=yok&password=pw", "", "username=ali"} {
		rec := f.get("/player_api.php?" + q + "&action=get_live_streams")
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"user_info":{"auth":0}}` {
			t.Errorf("%q: durum %d gövde %s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestUnusableViewer(t *testing.T) {
	ctx := context.Background()
	past := now.Add(-time.Hour)
	cases := map[string]struct {
		prepare func(f *fixture)
		status  string
	}{
		"süresi dolmuş":   {func(f *fixture) { f.store.SetViewerExpiry(ctx, f.ali, &past) }, "Expired"},
		"askıda izleyici": {func(f *fixture) { f.store.SetViewerStatus(ctx, f.ali, "suspended") }, "Banned"},
		"askıda yayıncı":  {func(f *fixture) { f.store.SetTenantStatus(ctx, f.t1, "suspended") }, "Disabled"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			c.prepare(f)

			a := decode[account](t, f.api(""))
			if a.UserInfo["auth"] != float64(1) || a.UserInfo["status"] != c.status {
				t.Errorf("giriş yanıtı durumu bildirmeli: %#v", a.UserInfo)
			}
			if c.status == "Expired" && a.UserInfo["exp_date"] != fmt.Sprint(past.Unix()) {
				t.Errorf("exp_date = %#v", a.UserInfo["exp_date"])
			}
			for _, action := range []string{"get_live_categories", "get_live_streams"} {
				if body := f.api("&action=" + action); body != "[]" {
					t.Errorf("%s boş liste dönmeli: %s", action, body)
				}
			}
			for _, path := range []string{"/get.php", "/xmltv.php"} {
				if rec := f.get(path + "?username=ali&password=pw"); rec.Code != http.StatusForbidden {
					t.Errorf("%s: durum %d", path, rec.Code)
				}
			}
		})
	}
}

func TestLiveCategories(t *testing.T) {
	f := setup(t)
	want := fmt.Sprintf(`[{"category_id":"%d","category_name":"Spor","parent_id":0},{"category_id":"0","category_name":"Genel","parent_id":0}]`, f.spor)
	if got := f.api("&action=get_live_categories"); got != want {
		t.Fatalf("kategoriler:\n%s\nbeklenen:\n%s", got, want)
	}

	// Tüm kanallar kategorili olunca sanal "Genel" kategorisi görünmez.
	if err := f.store.SetChannelCategory(context.Background(), f.k2, &f.spor); err != nil {
		t.Fatal(err)
	}
	if got := f.api("&action=get_live_categories"); strings.Contains(got, "Genel") {
		t.Fatalf("gereksiz Genel kategorisi: %s", got)
	}
}

type stream struct {
	Num          int     `json:"num"`
	Name         string  `json:"name"`
	StreamType   string  `json:"stream_type"`
	StreamID     int64   `json:"stream_id"`
	StreamIcon   string  `json:"stream_icon"`
	EPGChannelID *string `json:"epg_channel_id"`
	Added        string  `json:"added"`
	CategoryID   string  `json:"category_id"`
	CategoryIDs  []int64 `json:"category_ids"`
}

func TestLiveStreams(t *testing.T) {
	f := setup(t)
	body := f.api("&action=get_live_streams")
	if strings.Contains(body, "Yabancı") || strings.Contains(body, "s1") {
		t.Fatalf("başka yayıncının kanalı veya gizli anahtar sızdı: %s", body)
	}
	streams := decode[[]stream](t, body)
	if len(streams) != 2 {
		t.Fatalf("2 kanal bekleniyordu: %s", body)
	}
	k1, k2 := streams[0], streams[1]
	if k1.Num != 1 || k1.Name != "K1" || k1.StreamType != "live" || k1.StreamID != f.k1 || k1.EPGChannelID != nil || k1.Added == "" {
		t.Errorf("K1 alanları: %+v", k1)
	}
	if k1.CategoryID != fmt.Sprint(f.spor) || len(k1.CategoryIDs) != 1 || k1.CategoryIDs[0] != f.spor {
		t.Errorf("K1 kategorisi: %+v", k1)
	}
	if k2.Num != 2 || k2.StreamID != f.k2 || k2.CategoryID != "0" || len(k2.CategoryIDs) != 1 || k2.CategoryIDs[0] != 0 {
		t.Errorf("kategorisiz kanal sanal kategoride olmalı: %+v", k2)
	}
	for _, key := range []string{`"custom_sid":""`, `"tv_archive":0`, `"direct_source":""`, `"tv_archive_duration":0`, `"epg_channel_id":null`} {
		if !strings.Contains(body, key) {
			t.Errorf("eksik alan %s: %s", key, body)
		}
	}
}

func TestLiveStreamsCategoryFilter(t *testing.T) {
	f := setup(t)
	one := decode[[]stream](t, f.api(fmt.Sprintf("&action=get_live_streams&category_id=%d", f.spor)))
	if len(one) != 1 || one[0].StreamID != f.k1 {
		t.Errorf("Spor süzgeci: %+v", one)
	}
	general := decode[[]stream](t, f.api("&action=get_live_streams&category_id=0"))
	if len(general) != 1 || general[0].StreamID != f.k2 {
		t.Errorf("Genel süzgeci: %+v", general)
	}
	for _, id := range []string{fmt.Sprint(f.other), "999", "abc"} {
		if body := f.api("&action=get_live_streams&category_id=" + id); body != "[]" {
			t.Errorf("category_id=%s boş liste dönmeli: %s", id, body)
		}
	}
}

func TestEmptyAndUnsupportedActions(t *testing.T) {
	f := setup(t)
	for _, action := range []string{"get_vod_categories", "get_vod_streams", "get_series_categories", "get_series"} {
		if body := f.api("&action=" + action); body != "[]" {
			t.Errorf("%s: %s", action, body)
		}
	}
	for _, action := range []string{"get_short_epg", "get_simple_data_table"} {
		if body := f.api("&action=" + action + "&stream_id=1"); body != `{"epg_listings":[]}` {
			t.Errorf("%s: %s", action, body)
		}
	}
	if a := decode[account](t, f.api("&action=bilinmeyen")); a.UserInfo["auth"] != float64(1) || a.ServerInfo == nil {
		t.Errorf("bilinmeyen eylem hesap bilgisini dönmeli: %+v", a)
	}

	// Kanalı olmayan yayıncı için de null değil boş liste dönmeli.
	t3 := must(f.store.CreateTenant(context.Background(), "t3"))
	must(f.store.CreateViewer(context.Background(), t3, "bos", "pw", 1))
	for _, action := range []string{"get_live_categories", "get_live_streams"} {
		rec := f.get("/player_api.php?username=bos&password=pw&action=" + action)
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Errorf("%s: %s", action, rec.Body.String())
		}
	}
}

func TestPlaylist(t *testing.T) {
	f := setup(t)
	rec := f.get("/get.php?username=ali&password=pw&type=m3u_plus&output=ts")
	if rec.Code != http.StatusOK {
		t.Fatalf("durum %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "audio/x-mpegurl") {
		t.Errorf("içerik türü %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("şifre içeren liste önbelleğe alınmamalı")
	}
	want := strings.Join([]string{
		"#EXTM3U",
		`#EXTINF:-1 tvg-id="" tvg-name="K1" tvg-logo="" group-title="Spor",K1`,
		fmt.Sprintf("http://tv.example.com:8000/live/ali/pw/%d.ts", f.k1),
		`#EXTINF:-1 tvg-id="" tvg-name="K2" tvg-logo="" group-title="Genel",K2`,
		fmt.Sprintf("http://tv.example.com:8000/live/ali/pw/%d.ts", f.k2),
		"",
	}, "\n")
	if got := rec.Body.String(); got != want {
		t.Fatalf("liste:\n%s\nbeklenen:\n%s", got, want)
	}
}

func TestPlaylistOutputAndType(t *testing.T) {
	f := setup(t)
	for _, output := range []string{"m3u8", "hls"} {
		body := f.get("/get.php?username=ali&password=pw&type=m3u_plus&output=" + output).Body.String()
		if !strings.Contains(body, fmt.Sprintf("/live/ali/pw/%d.m3u8\n", f.k1)) || strings.Contains(body, ".ts") {
			t.Errorf("output=%s: %s", output, body)
		}
	}
	for _, q := range []string{"", "&output=mpegts", "&type=m3u_plus"} {
		body := f.get("/get.php?username=ali&password=pw" + q).Body.String()
		if !strings.Contains(body, fmt.Sprintf("/live/ali/pw/%d.ts\n", f.k1)) {
			t.Errorf("%q için varsayılan çıkış .ts olmalı: %s", q, body)
		}
	}
	plain := f.get("/get.php?username=ali&password=pw&type=m3u").Body.String()
	if !strings.Contains(plain, "#EXTINF:-1,K1\n") || strings.Contains(plain, "tvg-name") {
		t.Errorf("type=m3u öznitelik içermemeli: %s", plain)
	}
}

func TestPlaylistEscapesNamesAndCredentials(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	t4 := must(f.store.CreateTenant(ctx, "t4"))
	cat := must(f.store.CreateCategory(ctx, t4, `Film "HD"`))
	ch := must(f.store.CreateChannel(ctx, t4, "A \"B\", C\r\n#EXTINF:-1,sahte\nhttp://kotu.example/x", "s"))
	if err := f.store.SetChannelCategory(ctx, ch, &cat); err != nil {
		t.Fatal(err)
	}
	must(f.store.CreateViewer(ctx, t4, "a b@c", "p/w?x", 1))

	rec := f.get("/get.php?username=" + url.QueryEscape("a b@c") + "&password=" + url.QueryEscape("p/w?x") + "&type=m3u_plus")
	lines := strings.Split(strings.TrimSuffix(rec.Body.String(), "\n"), "\n")
	if rec.Code != http.StatusOK || len(lines) != 3 {
		t.Fatalf("tek kanal tam 3 satır üretmeli (durum %d):\n%s", rec.Code, rec.Body.String())
	}
	// Özniteliklerde çift tırnak tek tırnağa döner; satır sonları boşluk olur.
	wantInfo := `#EXTINF:-1 tvg-id="" tvg-name="A 'B', C #EXTINF:-1,sahte http://kotu.example/x" tvg-logo="" group-title="Film 'HD'",` +
		`A "B", C #EXTINF:-1,sahte http://kotu.example/x`
	if lines[1] != wantInfo {
		t.Errorf("bilgi satırı:\n%s\nbeklenen:\n%s", lines[1], wantInfo)
	}
	if want := fmt.Sprintf("http://tv.example.com:8000/live/a%%20b@c/p%%2Fw%%3Fx/%d.ts", ch); lines[2] != want {
		t.Errorf("adres %q, beklenen %q", lines[2], want)
	}
}

func TestPlaylistAndGuideRequireValidLogin(t *testing.T) {
	f := setup(t)
	for _, path := range []string{"/get.php", "/xmltv.php"} {
		if rec := f.get(path + "?username=ali&password=yanlis"); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "K1") {
			t.Errorf("%s: durum %d", path, rec.Code)
		}
	}
	veli := f.get("/get.php?username=veli&password=pw2").Body.String()
	if !strings.Contains(veli, "Yabancı") || strings.Contains(veli, "K1") {
		t.Errorf("veli yalnızca kendi yayıncısının kanallarını görmeli: %s", veli)
	}
}

func TestGuideIsEmptyXMLTV(t *testing.T) {
	f := setup(t)
	rec := f.get("/xmltv.php?username=ali&password=pw")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("durum %d, tür %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, `<?xml version="1.0" encoding="UTF-8"?>`) || !strings.Contains(body, "<tv") || !strings.Contains(body, "</tv>") {
		t.Fatalf("beklenmeyen gövde: %s", body)
	}
}

func TestTooManyFailedLoginsReturn429(t *testing.T) {
	f := setup(t)
	for i := 0; i < maxFailures; i++ {
		f.get("/player_api.php?username=ali&password=yanlis")
	}
	for _, path := range []string{"/player_api.php", "/get.php", "/xmltv.php"} {
		if rec := f.get(path + "?username=ali&password=pw"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("%s: durum %d", path, rec.Code)
		}
	}
}

func TestHTTPSBaseURL(t *testing.T) {
	f := setup(t)
	mux := http.NewServeMux()
	authn := auth.New(f.store, ratelimit.New(maxFailures, time.Minute, time.Now), false)
	xtream.New(f.store, authn, f.manager, must(url.Parse("https://tv.example.com")), func() time.Time { return now }).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/player_api.php?username=ali&password=pw", nil))
	a := decode[account](t, rec.Body.String())
	for k, want := range map[string]any{"url": "tv.example.com", "port": "80", "https_port": "443", "server_protocol": "https"} {
		if a.ServerInfo[k] != want {
			t.Errorf("server_info.%s = %#v, beklenen %#v", k, a.ServerInfo[k], want)
		}
	}
}
