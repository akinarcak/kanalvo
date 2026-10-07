package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"kanalvo/internal/balancer"
	"kanalvo/internal/config"
	"kanalvo/internal/hooks"
	"kanalvo/internal/session"
	"kanalvo/internal/session/sessiontest"
	"kanalvo/internal/testdb"
	"kanalvo/internal/token"
)

// İzleyicilere açık tüm uçlar tek bir yönlendiricide birlikte kaydedilir. Bu test, kısa yayın
// adresinin (/{kullanıcı}/{şifre}/{kanal}) diğer uçları gölgelemediğini doğrular.
func TestPublicRoutesDoNotShadowEachOther(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	tenant, err := st.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateChannel(ctx, tenant, "K", "sek")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateViewer(ctx, tenant, "ali", "pw", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkLive(ctx, channel, "a"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		HookSecret:         "hook-secret-0123456789",
		PublicBaseURL:      "http://tv.example.com",
		EdgeTSBaseURL:      "http://ts.example.com",
		EdgeHLSBaseURL:     "http://hls.example.com",
		TokenTTL:           time.Minute,
		HLSTokenTTL:        time.Hour,
		LoginMaxFailures:   1,
		LoginFailureWindow: time.Minute,
	}
	upstream, _ := url.Parse("http://127.0.0.1:1")
	public, _ := url.Parse(cfg.PublicBaseURL)
	srs := &sessiontest.FakeSRS{}
	sessions := session.New(st, sessiontest.For(srs), srs, 30*time.Second, time.Hour)
	signer := token.NewSigner([]byte(strings.Repeat("k", 32)))
	localEdge, err := st.SyncLocalEdge(ctx, cfg.EdgeTSBaseURL, cfg.EdgeHLSBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkEdgeSeen(ctx, localEdge); err != nil {
		t.Fatal(err)
	}
	edges := balancer.New(st, 15*time.Second, 0)
	hook := hooks.New(st, signer, sessions, edges, time.Now)
	mux := publicMux(st, signer, sessions, edges, hook, localEdge, cfg, public, upstream)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/healthz"); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("/healthz: durum %d gövde %q", rec.Code, rec.Body.String())
	}
	// HLS geçidine giden istekler giriş denemesi sayılmamalı (eşik 1: sayılsaydı sonraki giriş 429 alırdı).
	for _, path := range []string{"/hls/sahte-imza/1.m3u8", "/hls/sahte-imza/1-9.ts"} {
		if rec := get(path); rec.Code != http.StatusForbidden {
			t.Errorf("%s: durum %d", path, rec.Code)
		}
	}
	if rec := get("/hooks/srs/hook-secret-0123456789/publish"); rec.Code != http.StatusNotFound {
		t.Errorf("SRS sorgu ucu izleyici yönlendiricisinde olmamalı: durum %d", rec.Code)
	}
	// Uzak edge'lerin uçları anahtarsız kullanılamaz ve izleyici yollarıyla karışmaz.
	for _, path := range []string{"/edge/hls/sahte-imza/1.m3u8", "/edge/segment/1.m3u8"} {
		if rec := get(path); rec.Code != http.StatusForbidden {
			t.Errorf("%s: anahtarsız istek durum %d", path, rec.Code)
		}
	}
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/edge/sahte-anahtar/hooks/play", strings.NewReader("{}")))
	if post.Code != http.StatusNotFound {
		t.Errorf("geçersiz edge anahtarıyla izleme sorgusu: durum %d", post.Code)
	}
	if rec := get("/a/b/c"); rec.Code != http.StatusNotFound {
		t.Errorf("kanal numarası olmayan yol 404 olmalı: durum %d", rec.Code)
	}

	if rec := get("/player_api.php?username=ali&password=pw"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auth":1`) {
		t.Errorf("player_api.php: durum %d gövde %s", rec.Code, rec.Body.String())
	}
	if rec := get("/get.php?username=ali&password=pw"); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "#EXTM3U") {
		t.Errorf("get.php: durum %d", rec.Code)
	}
	if rec := get("/xmltv.php?username=ali&password=pw"); rec.Code != http.StatusOK {
		t.Errorf("xmltv.php: durum %d", rec.Code)
	}
	for _, path := range []string{"/ali/pw/1", "/live/ali/pw/1.ts"} {
		if rec := get(path); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "http://ts.example.com/live/1.ts") {
			t.Errorf("%s: durum %d adres %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := get("/ali/pw/1.m3u8"); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "http://hls.example.com/hls/") {
		t.Errorf("kısa HLS adresi: durum %d", rec.Code)
	}
}
