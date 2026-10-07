package play_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"kanalvo/internal/auth"
	"kanalvo/internal/balancer"
	"kanalvo/internal/play"
	"kanalvo/internal/ratelimit"
	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
	"kanalvo/internal/token"
)

const (
	tsBase  = "http://ts.test:8081"
	hlsBase = "http://hls.test:8000"
	tsTTL   = 5 * time.Minute
	hlsTTL  = 6 * time.Hour
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t       *testing.T
	store   *store.Store
	signer  *token.Signer
	mux     *http.ServeMux
	tenant  int64
	channel int64
	viewer  int64
	local   int64
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func setup(t *testing.T) *fixture { return setupWithLimit(t, 100) }

// setupWithLimit, IP başına hatalı giriş eşiği verilen değer olan bir kurulum döner.
func setupWithLimit(t *testing.T, maxFailures int) *fixture {
	ctx := context.Background()
	f := &fixture{t: t, store: testdb.New(t), signer: token.NewSigner([]byte(strings.Repeat("k", 32))), mux: http.NewServeMux()}
	f.tenant = must(f.store.CreateTenant(ctx, "t"))
	f.channel = must(f.store.CreateChannel(ctx, f.tenant, "c", "gizli-anahtar"))
	f.viewer = must(f.store.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	must(f.store.MarkLive(ctx, f.channel, "a"))
	// Yerel edge sağlıklıdır: yönlendirmeler ona gider.
	f.local = must(f.store.SyncLocalEdge(ctx, tsBase, hlsBase))
	if err := f.store.MarkEdgeSeen(ctx, f.local); err != nil {
		t.Fatal(err)
	}
	opts := play.Options{TSTokenTTL: tsTTL, HLSTokenTTL: hlsTTL}
	authn := auth.New(f.store, ratelimit.New(maxFailures, time.Minute, time.Now), false)
	edges := balancer.New(f.store, 15*time.Second, 0)
	play.New(f.store, authn, f.signer, edges, opts, func() time.Time { return now }).Register(f.mux)
	return f
}

func (f *fixture) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func (f *fixture) path(user, pass, file string) string {
	return "/live/" + user + "/" + pass + "/" + file
}

func (f *fixture) redirect(file string) (*url.URL, string) {
	f.t.Helper()
	rec := f.get(f.path("ali", "pw", file))
	if rec.Code != http.StatusFound {
		f.t.Fatalf("%s: durum %d", file, rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		f.t.Fatalf("%s: yönlendirme önbelleğe alınmamalı", file)
	}
	raw := rec.Header().Get("Location")
	if strings.Contains(raw, "gizli-anahtar") {
		f.t.Fatalf("%s: adres gizli yayın anahtarını içeriyor", file)
	}
	return must(url.Parse(raw)), raw
}

func (f *fixture) checkClaims(tok string, kind token.Kind, ttl time.Duration) {
	f.t.Helper()
	claims, err := f.signer.Verify(tok, now)
	if err != nil {
		f.t.Fatalf("imza doğrulanamadı: %v", err)
	}
	if claims.Kind != kind || claims.ViewerID != f.viewer || claims.ChannelID != f.channel || !claims.ExpiresAt.Equal(now.Add(ttl)) {
		f.t.Fatalf("beklenmeyen içerik %+v", claims)
	}
}

func TestTSRedirectsToRelayWithShortToken(t *testing.T) {
	f := setup(t)
	loc, _ := f.redirect(fmt.Sprintf("%d.ts", f.channel))
	want := fmt.Sprintf("%s/live/%d.ts", tsBase, f.channel)
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != want {
		t.Fatalf("adres %q, beklenen %q", got, want)
	}
	f.checkClaims(loc.Query().Get("token"), token.KindTS, tsTTL)
}

func TestHLSRedirectsToGatewayWithTokenInPath(t *testing.T) {
	f := setup(t)
	loc, raw := f.redirect(fmt.Sprintf("%d.m3u8", f.channel))
	prefix, suffix := hlsBase+"/hls/", fmt.Sprintf("/%d.m3u8", f.channel)
	if !strings.HasPrefix(raw, prefix) || !strings.HasSuffix(raw, suffix) || loc.RawQuery != "" {
		t.Fatalf("beklenmeyen adres: %s", raw)
	}
	tok := strings.TrimSuffix(strings.TrimPrefix(raw, prefix), suffix)
	if strings.Contains(tok, "/") {
		t.Fatalf("imza tek yol parçası olmalı: %q", tok)
	}
	f.checkClaims(tok, token.KindHLS, hlsTTL)
}

// Her HLS yönlendirmesi ayrı bir oturum anahtarı taşır; bağlantı limiti bunlarla sayılır.
func TestEachHLSRedirectCarriesItsOwnSessionKey(t *testing.T) {
	f := setup(t)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		_, raw := f.redirect(fmt.Sprintf("%d.m3u8", f.channel))
		tok := strings.TrimSuffix(strings.TrimPrefix(raw, hlsBase+"/hls/"), fmt.Sprintf("/%d.m3u8", f.channel))
		claims, err := f.signer.Verify(tok, now)
		if err != nil || len(claims.Session) < 16 {
			t.Fatalf("oturum anahtarı eksik: %+v, %v", claims, err)
		}
		seen[claims.Session] = true
	}
	if len(seen) != 3 {
		t.Fatalf("oturum anahtarları farklı olmalı: %v", seen)
	}
}

func TestRejectsInvalidViewer(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(f *fixture) (user, pass string){
		"yanlış şifre":      func(f *fixture) (string, string) { return "ali", "yanlis" },
		"olmayan kullanıcı": func(f *fixture) (string, string) { return "yok", "pw" },
		"askıda izleyici": func(f *fixture) (string, string) {
			f.store.SetViewerStatus(ctx, f.viewer, "suspended")
			return "ali", "pw"
		},
		"süresi dolmuş": func(f *fixture) (string, string) {
			past := now.Add(-time.Hour)
			f.store.SetViewerExpiry(ctx, f.viewer, &past)
			return "ali", "pw"
		},
		"askıda yayıncı": func(f *fixture) (string, string) {
			f.store.SetTenantStatus(ctx, f.tenant, "suspended")
			return "ali", "pw"
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			user, pass := prepare(f)
			for _, ext := range []string{"ts", "m3u8"} {
				rec := f.get(f.path(user, pass, fmt.Sprintf("%d.%s", f.channel, ext)))
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s: durum %d", ext, rec.Code)
				}
				if rec.Header().Get("Location") != "" {
					t.Fatalf("%s: ret yanıtında yönlendirme olmamalı", ext)
				}
			}
		})
	}
}

func TestUndecodableUsernameIsForbidden(t *testing.T) {
	f := setup(t)
	for _, user := range []string{"%00", "ali%00", "%ff", "%c3%28"} {
		rec := f.get(f.path(user, "pw", fmt.Sprintf("%d.ts", f.channel)))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: durum %d", user, rec.Code)
		}
	}
}

func TestOtherTenantChannelLooksMissing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	t2 := must(f.store.CreateTenant(ctx, "t2"))
	other := must(f.store.CreateChannel(ctx, t2, "c2", "s2"))
	must(f.store.MarkLive(ctx, other, "z"))
	if rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.ts", other))); rec.Code != http.StatusNotFound {
		t.Fatalf("durum %d", rec.Code)
	}
}

func TestOfflineChannelIsNotFound(t *testing.T) {
	f := setup(t)
	if err := f.store.MarkOffline(context.Background(), f.channel, "a"); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.ts", f.channel))); rec.Code != http.StatusNotFound {
		t.Fatalf("durum %d", rec.Code)
	}
}

func TestRejectsUnknownFileNames(t *testing.T) {
	f := setup(t)
	id := fmt.Sprint(f.channel)
	for _, file := range []string{id + ".mp4", id + ".", id + ".ts.ts", "abc.ts", ".ts", "0.ts", "-1.ts", "999999.ts", id + ".TS"} {
		if rec := f.get(f.path("ali", "pw", file)); rec.Code != http.StatusNotFound {
			t.Errorf("%q: durum %d", file, rec.Code)
		}
	}
}

func TestShortAndExtensionlessForms(t *testing.T) {
	f := setup(t)
	id := fmt.Sprint(f.channel)
	cases := map[string]string{
		"/ali/pw/" + id:           tsBase + "/live/" + id + ".ts",
		"/ali/pw/" + id + ".ts":   tsBase + "/live/" + id + ".ts",
		"/live/ali/pw/" + id:      tsBase + "/live/" + id + ".ts",
		"/ali/pw/" + id + ".m3u8": hlsBase + "/hls/",
	}
	for path, wantPrefix := range cases {
		rec := f.get(path)
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), wantPrefix) {
			t.Errorf("%s: durum %d, adres %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	for _, path := range []string{"/ali/yanlis/" + id, "/yok/pw/" + id} {
		if rec := f.get(path); rec.Code != http.StatusForbidden {
			t.Errorf("%s: durum %d", path, rec.Code)
		}
	}
	if rec := f.get("/ali/pw/abc"); rec.Code != http.StatusNotFound {
		t.Errorf("geçersiz kanal: durum %d", rec.Code)
	}
}

func TestTooManyFailedLoginsReturn429(t *testing.T) {
	f := setupWithLimit(t, 2)
	file := fmt.Sprintf("%d.ts", f.channel)
	for i := 0; i < 2; i++ {
		if rec := f.get(f.path("ali", "yanlis", file)); rec.Code != http.StatusForbidden {
			t.Fatalf("%d. deneme: durum %d", i+1, rec.Code)
		}
	}
	rec := f.get(f.path("ali", "pw", file))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Location") != "" {
		t.Fatalf("eşikten sonra 429 bekleniyordu: durum %d", rec.Code)
	}
}

func TestEncodedUsername(t *testing.T) {
	f := setup(t)
	must(f.store.CreateViewer(context.Background(), f.tenant, "a@b c", "p/w", 1))
	rec := f.get(f.path("a%40b%20c", "p%2Fw", fmt.Sprintf("%d.ts", f.channel)))
	if rec.Code != http.StatusFound {
		t.Fatalf("durum %d", rec.Code)
	}
}
