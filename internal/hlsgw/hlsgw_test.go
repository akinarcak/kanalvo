package hlsgw_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"streamhub/internal/hlsgw"
	"streamhub/internal/session"
	"streamhub/internal/session/sessiontest"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
	"streamhub/internal/token"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t       *testing.T
	store   *store.Store
	signer  *token.Signer
	mux     *http.ServeMux
	srs     *sessiontest.FakeSRS
	manager *session.Manager
	tenant  int64
	channel int64
	viewer  int64

	mu   sync.Mutex
	seen []string // SRS'e giden isteklerin "yol?sorgu" biçimi
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// setup, SRS yerine geçen sahte bir üst sunucuyla geçidi kurar.
// Üst sunucu /live/1.m3u8 ve /live/1-9.ts dosyalarını bilir, diğerlerine 404 döner.
func setup(t *testing.T) *fixture {
	ctx := context.Background()
	f := &fixture{t: t, store: testdb.New(t), signer: token.NewSigner([]byte(strings.Repeat("k", 32))), mux: http.NewServeMux()}
	f.tenant = must(f.store.CreateTenant(ctx, "t"))
	f.channel = must(f.store.CreateChannel(ctx, f.tenant, "c", "sek"))
	f.viewer = must(f.store.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	must(f.store.MarkLive(ctx, f.channel, "a"))

	srs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		switch r.URL.Path {
		case "/live/1.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n1-9.ts\n")
		case "/live/1-9.ts":
			w.Header().Set("Content-Type", "video/MP2T")
			io.WriteString(w, "TSDATA")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srs.Close)

	f.srs = &sessiontest.FakeSRS{}
	f.manager = session.New(f.store, f.srs, f.srs, 30*time.Second, 6*time.Hour)
	hlsgw.New(f.store, f.signer, f.manager, must(url.Parse(srs.URL)), time.Second, false, func() time.Time { return now }).Register(f.mux)
	return f
}

func (f *fixture) token(viewer, channel int64, exp time.Time) string {
	return f.session(viewer, channel, "a1", exp)
}

// session, verilen oturum anahtarını taşıyan bir HLS imzası üretir.
func (f *fixture) session(viewer, channel int64, key string, exp time.Time) string {
	return f.signer.Sign(token.Claims{Kind: token.KindHLS, ViewerID: viewer, ChannelID: channel, Session: key, ExpiresAt: exp})
}

// getFrom, isteği verilen bağlantı adresinden gönderir.
func (f *fixture) getFrom(remote, tok, file string) int {
	req := httptest.NewRequest(http.MethodGet, "/hls/"+tok+"/"+file, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code
}

func (f *fixture) valid() string { return f.token(f.viewer, f.channel, now.Add(time.Hour)) }

func (f *fixture) get(tok, file string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hls/"+tok+"/"+file+"?x=1", nil))
	return rec
}

func (f *fixture) upstreamHits() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func TestServesPlaylistAndSegmentFromUpstream(t *testing.T) {
	f := setup(t)
	if f.channel != 1 {
		t.Fatalf("test, kanal numarasının 1 olmasına dayanıyor: %d", f.channel)
	}

	rec := f.get(f.valid(), "1.m3u8")
	if rec.Code != http.StatusOK || rec.Body.String() != "#EXTM3U\n1-9.ts\n" {
		t.Fatalf("çalma listesi: durum %d gövde %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("içerik türü aktarılmalı: %q", ct)
	}

	rec = f.get(f.valid(), "1-9.ts")
	if rec.Code != http.StatusOK || rec.Body.String() != "TSDATA" {
		t.Fatalf("parça: durum %d gövde %q", rec.Code, rec.Body.String())
	}

	want := []string{"/live/1.m3u8?", "/live/1-9.ts?"}
	if got := f.upstreamHits(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("SRS'e giden istekler %v, beklenen %v (imza ve sorgu aktarılmamalı)", got, want)
	}
}

func TestPassesThroughUpstreamNotFound(t *testing.T) {
	f := setup(t)
	if rec := f.get(f.valid(), "1-12345.ts"); rec.Code != http.StatusNotFound {
		t.Fatalf("durum %d", rec.Code)
	}
}

func TestRejectsBadTokens(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(f *fixture) string{
		"bozuk imza":    func(f *fixture) string { return "1.1.9999999999.AAAA" },
		"süresi dolmuş": func(f *fixture) string { return f.token(f.viewer, f.channel, now) },
		"başka kanalın imzası": func(f *fixture) string {
			other := must(f.store.CreateChannel(ctx, f.tenant, "c2", "s2"))
			must(f.store.MarkLive(ctx, other, "z"))
			return f.token(f.viewer, other, now.Add(time.Hour))
		},
		"olmayan izleyici": func(f *fixture) string { return f.token(f.viewer+999, f.channel, now.Add(time.Hour)) },
		"kısa ömürlü .ts imzası": func(f *fixture) string {
			return f.signer.Sign(token.Claims{Kind: token.KindTS, ViewerID: f.viewer, ChannelID: f.channel, ExpiresAt: now.Add(time.Hour)})
		},
		"askıda izleyici": func(f *fixture) string {
			f.store.SetViewerStatus(ctx, f.viewer, "suspended")
			return f.valid()
		},
		"süresi dolmuş izleyici": func(f *fixture) string {
			past := now.Add(-time.Hour)
			f.store.SetViewerExpiry(ctx, f.viewer, &past)
			return f.valid()
		},
		"askıda yayıncı": func(f *fixture) string {
			f.store.SetTenantStatus(ctx, f.tenant, "suspended")
			return f.valid()
		},
		"başka yayıncının izleyicisi": func(f *fixture) string {
			t2 := must(f.store.CreateTenant(ctx, "t2"))
			v2 := must(f.store.CreateViewer(ctx, t2, "veli", "pw", 1))
			return f.token(v2, f.channel, now.Add(time.Hour))
		},
	}
	for name, makeToken := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			tok := makeToken(f)
			for _, file := range []string{"1.m3u8", "1-9.ts"} {
				if rec := f.get(tok, file); rec.Code != http.StatusForbidden {
					t.Errorf("%s: durum %d", file, rec.Code)
				}
			}
			if hits := f.upstreamHits(); len(hits) != 0 {
				t.Fatalf("reddedilen istek SRS'e gitmemeli: %v", hits)
			}
		})
	}
}

func TestNewSessionEndsTheOlderOneWhenLimitIsReached(t *testing.T) {
	f := setup(t) // izleyicinin bağlantı limiti 1
	first := f.session(f.viewer, f.channel, "aa", now.Add(time.Hour))
	second := f.session(f.viewer, f.channel, "bb", now.Add(time.Hour))
	const tv = "1.1.1.1:1000"

	for _, file := range []string{"1.m3u8", "1-9.ts"} {
		if code := f.getFrom(tv, first, file); code != http.StatusOK {
			t.Fatalf("ilk oturum %s: durum %d", file, code)
		}
	}
	if code := f.getFrom(tv, second, "1.m3u8"); code != http.StatusOK {
		t.Fatalf("yeni oturum kabul edilmeli (kanal değiştirme): durum %d", code)
	}
	for _, file := range []string{"1.m3u8", "1-9.ts"} {
		if code := f.getFrom(tv, first, file); code != http.StatusForbidden {
			t.Fatalf("yerinden edilen oturum %s: durum %d", file, code)
		}
	}
	if code := f.getFrom(tv, second, "1-9.ts"); code != http.StatusOK {
		t.Fatalf("yeni oturum sürmeli: durum %d", code)
	}
}

func TestSharedLinkFromAnotherAddressTakesTheOnlySlot(t *testing.T) {
	f := setup(t)
	link := f.session(f.viewer, f.channel, "aa", now.Add(time.Hour))
	if code := f.getFrom("1.1.1.1:1000", link, "1.m3u8"); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
	if code := f.getFrom("2.2.2.2:1000", link, "1.m3u8"); code != http.StatusOK {
		t.Fatalf("başka adresten açılan aynı adres ayrı bağlantı sayılır ve kabul edilir: durum %d", code)
	}
	if code := f.getFrom("1.1.1.1:2000", link, "1.m3u8"); code != http.StatusForbidden {
		t.Fatalf("limit 1 iken ilk adres yerinden edilmiş olmalı: durum %d", code)
	}
}

func TestTenantConnectionQuotaFull(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.store.SetTenantQuotas(ctx, f.tenant, store.Quotas{MaxChannels: 10, MaxViewers: 10, MaxConnections: 1}); err != nil {
		t.Fatal(err)
	}
	other := must(f.store.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	if code := f.getFrom("1.1.1.1:1", f.session(other, f.channel, "aa", now.Add(time.Hour)), "1.m3u8"); code != http.StatusOK {
		t.Fatalf("ilk izleyici: durum %d", code)
	}
	before := len(f.upstreamHits())
	if code := f.getFrom("2.2.2.2:1", f.valid(), "1.m3u8"); code != http.StatusForbidden {
		t.Fatalf("kota doluyken ikinci izleyici reddedilmeli: durum %d", code)
	}
	if len(f.upstreamHits()) != before {
		t.Fatal("reddedilen istek SRS'e gitmemeli")
	}
}

func TestRejectedRequestsOpenNoSession(t *testing.T) {
	f := setup(t)
	f.get("1.1.9999999999.AAAA", "1.m3u8")
	f.get(f.valid(), "1.mp4")
	if n := must(f.manager.ActiveCount(context.Background(), f.viewer)); n != 0 {
		t.Fatalf("reddedilen istek oturum açmamalı: %d", n)
	}
	f.get(f.valid(), "1.m3u8")
	if n := must(f.manager.ActiveCount(context.Background(), f.viewer)); n != 1 {
		t.Fatalf("geçerli istek tek oturum açmalı: %d", n)
	}
}

func TestOfflineChannelIsNotFound(t *testing.T) {
	f := setup(t)
	if err := f.store.MarkOffline(context.Background(), f.channel, "a"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"1.m3u8", "1-9.ts"} {
		if rec := f.get(f.valid(), file); rec.Code != http.StatusNotFound {
			t.Errorf("%s: durum %d", file, rec.Code)
		}
	}
	if hits := f.upstreamHits(); len(hits) != 0 {
		t.Fatalf("çevrimdışı kanal için SRS'e gidilmemeli: %v", hits)
	}
}

func TestRejectsUnknownFileNames(t *testing.T) {
	f := setup(t)
	files := []string{
		"1.ts", "1", "1.mp4", "1.m3u8.bak", "1-.ts", "1-a.ts", "1-9.m3u8", "1-9-9.ts",
		"abc.m3u8", "0.m3u8", "-1.m3u8", "1-9.TS", "..%2F..%2Fetc%2Fpasswd", "1-9.ts%2F..%2F..%2Fx",
	}
	for _, file := range files {
		if rec := f.get(f.valid(), file); rec.Code != http.StatusNotFound {
			t.Errorf("%q: durum %d", file, rec.Code)
		}
	}
	if hits := f.upstreamHits(); len(hits) != 0 {
		t.Fatalf("geçersiz dosya adı SRS'e gitmemeli: %v", hits)
	}
}

func TestUpstreamDownIsBadGateway(t *testing.T) {
	f := setup(t)
	mux := http.NewServeMux()
	dead := must(url.Parse("http://127.0.0.1:1"))
	hlsgw.New(f.store, f.signer, f.manager, dead, time.Second, false, func() time.Time { return now }).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hls/"+f.valid()+"/1.m3u8", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("durum %d", rec.Code)
	}
}

func TestHungUpstreamIsBadGateway(t *testing.T) {
	f := setup(t)
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })

	mux := http.NewServeMux()
	hlsgw.New(f.store, f.signer, f.manager, must(url.Parse(hung.URL)), 200*time.Millisecond, false, func() time.Time { return now }).Register(mux)

	start := time.Now()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hls/"+f.valid()+"/1.m3u8", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("durum %d", rec.Code)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("yanıt vermeyen SRS geçidi %s bekletti", took)
	}
}
