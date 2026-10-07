package hooks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kanalvo/internal/balancer"
	"kanalvo/internal/hooks"
	"kanalvo/internal/session"
	"kanalvo/internal/session/sessiontest"
	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
	"kanalvo/internal/token"
)

const hookSecret = "hook-secret-0123456789"

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t       *testing.T
	store   *store.Store
	signer  *token.Signer
	mux     *http.ServeMux
	srs     *sessiontest.FakeSRS
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

func setup(t *testing.T) *fixture {
	ctx := context.Background()
	f := &fixture{t: t, store: testdb.New(t), signer: token.NewSigner([]byte(strings.Repeat("k", 32))), mux: http.NewServeMux()}
	f.tenant = must(f.store.CreateTenant(ctx, "t"))
	f.channel = must(f.store.CreateChannel(ctx, f.tenant, "c", "sek"))
	f.viewer = must(f.store.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	f.srs = &sessiontest.FakeSRS{}
	sessions := session.New(f.store, sessiontest.For(f.srs), f.srs, 30*time.Second, 6*time.Hour)
	f.local = testdb.LocalEdge(t)
	h := hooks.New(f.store, f.signer, sessions, balancer.New(f.store, 15*time.Second, 0), func() time.Time { return now })
	h.Register(f.mux, hookSecret, f.local)
	h.RegisterEdge(f.mux)
	return f
}

func (f *fixture) post(secret, event, body string) int {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/srs/"+secret+"/"+event, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK && strings.TrimSpace(rec.Body.String()) != `{"code":0}` {
		f.t.Fatalf("kabul gövdesi beklenmedik: %q", rec.Body.String())
	}
	return rec.Code
}

// event, SRS 5.0.225'in gönderdiği gövdenin biçimidir (bkz. docs/srs-findings.md).
func event(client, app, stream, param string) string {
	return fmt.Sprintf(`{"server_id":"vid-1","action":"x","client_id":%q,"ip":"1.2.3.4","vhost":"__defaultVhost__","app":%q,"tcUrl":"rtmp://srs:1935/live","stream":%q,"param":%q,"stream_url":"/live/1","stream_id":"vid-2"}`,
		client, app, stream, param)
}

func (f *fixture) publish(client, param string) int {
	return f.post(hookSecret, "publish", event(client, "live", fmt.Sprint(f.channel), param))
}

func (f *fixture) live() bool {
	return must(f.store.ChannelByID(context.Background(), f.channel)).Live
}

func (f *fixture) tokenFor(viewer, channel int64, exp time.Time) string {
	return "?token=" + f.signer.Sign(token.Claims{Kind: token.KindTS, ViewerID: viewer, ChannelID: channel, ExpiresAt: exp})
}

func (f *fixture) play(stream, param string) int {
	return f.playAs("p1", stream, param)
}

func (f *fixture) playAs(client, stream, param string) int {
	return f.post(hookSecret, "play", event(client, "live", stream, param))
}

func (f *fixture) sessions(viewer int64) int {
	return must(f.store.ActiveSessionCount(context.Background(), viewer, 30*time.Second))
}

func TestPublishAcceptsValidSecret(t *testing.T) {
	f := setup(t)
	if code := f.publish("a", "?secret=sek"); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
	if !f.live() {
		t.Fatal("kanal yayında olmalı")
	}
}

func TestPublishAcceptsParamWithoutQuestionMarkAndExtraParams(t *testing.T) {
	f := setup(t)
	if code := f.publish("a", "secret=sek&foo=bar"); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}

func TestPublishRejectsWrongSecret(t *testing.T) {
	f := setup(t)
	for _, param := range []string{"?secret=yanlis", "?secret=", "", "?other=sek"} {
		if code := f.publish("a", param); code != http.StatusForbidden {
			t.Errorf("param %q: durum %d", param, code)
		}
	}
	if f.live() {
		t.Fatal("kanal çevrimdışı kalmalı")
	}
}

func TestPublishRejectsMalformedInput(t *testing.T) {
	f := setup(t)
	ch := fmt.Sprint(f.channel)
	cases := map[string]struct {
		body string
		want int
	}{
		"sayısal olmayan kanal": {event("a", "live", "abc", "?secret=sek"), http.StatusForbidden},
		"uzantılı kanal adı":    {event("a", "live", ch+".ts", "?secret=sek"), http.StatusForbidden},
		"sıfır kanal":           {event("a", "live", "0", "?secret=sek"), http.StatusForbidden},
		"negatif kanal":         {event("a", "live", "-1", "?secret=sek"), http.StatusForbidden},
		"olmayan kanal":         {event("a", "live", "999999", "?secret=sek"), http.StatusForbidden},
		"yanlış uygulama":       {event("a", "other", ch, "?secret=sek"), http.StatusForbidden},
		"bozuk parametre":       {event("a", "live", ch, "?secret=%zz"), http.StatusForbidden},
		"bozuk JSON":            {`{"app":`, http.StatusBadRequest},
		"boş gövde":             {``, http.StatusBadRequest},
	}
	for name, c := range cases {
		if code := f.post(hookSecret, "publish", c.body); code != c.want {
			t.Errorf("%s: durum %d, beklenen %d", name, code, c.want)
		}
	}
	if f.live() {
		t.Fatal("kanal çevrimdışı kalmalı")
	}
}

func TestRejectsWrongHookSecretAndUnknownEvent(t *testing.T) {
	f := setup(t)
	body := event("a", "live", fmt.Sprint(f.channel), "?secret=sek")
	if code := f.post("yanlis-sir", "publish", body); code != http.StatusNotFound {
		t.Fatalf("yanlış sır: durum %d", code)
	}
	if code := f.post(hookSecret, "bilinmeyen", body); code != http.StatusNotFound {
		t.Fatalf("bilinmeyen olay: durum %d", code)
	}
	if f.live() {
		t.Fatal("kanal çevrimdışı kalmalı")
	}
}

func TestPublishRejectsSuspendedTenant(t *testing.T) {
	f := setup(t)
	if err := f.store.SetTenantStatus(context.Background(), f.tenant, "suspended"); err != nil {
		t.Fatal(err)
	}
	if code := f.publish("a", "?secret=sek"); code != http.StatusForbidden {
		t.Fatalf("durum %d", code)
	}
}

func TestSecondPublisherRejectedAndCannotTakeChannelOffline(t *testing.T) {
	f := setup(t)
	ch := fmt.Sprint(f.channel)
	if code := f.publish("a", "?secret=sek"); code != http.StatusOK {
		t.Fatalf("ilk yayıncı: durum %d", code)
	}
	if code := f.publish("b", "?secret=sek"); code != http.StatusForbidden {
		t.Fatalf("ikinci yayıncı: durum %d", code)
	}
	if code := f.post(hookSecret, "unpublish", event("b", "live", ch, "")); code != http.StatusOK {
		t.Fatalf("unpublish her zaman kabul edilmeli: durum %d", code)
	}
	if !f.live() {
		t.Fatal("reddedilen bağlantının bildirimi süren yayını çevrimdışı yapmamalı")
	}
	f.post(hookSecret, "unpublish", event("a", "live", ch, ""))
	if f.live() {
		t.Fatal("yayıncının kendi bildirimi kanalı çevrimdışı yapmalı")
	}
}

func TestUnpublishWithMalformedStreamIsAccepted(t *testing.T) {
	f := setup(t)
	if code := f.post(hookSecret, "unpublish", event("a", "live", "abc", "")); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}

func TestPlayAcceptsValidToken(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	param := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute)) + "&foo=bar"
	if code := f.play(fmt.Sprint(f.channel), param); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}

func TestPlayRejectsTokenForOtherChannel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := must(f.store.CreateChannel(ctx, f.tenant, "c2", "sek2"))
	must(f.store.MarkLive(ctx, other, "z"))
	f.publish("a", "?secret=sek")
	param := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
	if code := f.play(fmt.Sprint(other), param); code != http.StatusForbidden {
		t.Fatalf("durum %d", code)
	}
}

func TestPlayRejectsHLSToken(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	hls := f.signer.Sign(token.Claims{Kind: token.KindHLS, ViewerID: f.viewer, ChannelID: f.channel, ExpiresAt: now.Add(time.Hour)})
	if code := f.play(fmt.Sprint(f.channel), "?token="+hls); code != http.StatusForbidden {
		t.Fatalf("uzun ömürlü HLS imzası .ts izlemesinde geçmemeli: durum %d", code)
	}
}

func TestPlayRejectsMalformedInput(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	ch := fmt.Sprint(f.channel)
	param := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
	cases := map[string]string{
		"sayısal olmayan kanal": event("p1", "live", "abc", param),
		"uzantılı kanal adı":    event("p1", "live", ch+".m3u8", param),
		"yanlış uygulama":       event("p1", "other", ch, param),
		"bozuk parametre":       event("p1", "live", ch, "?token=%zz"),
	}
	for name, body := range cases {
		if code := f.post(hookSecret, "play", body); code != http.StatusForbidden {
			t.Errorf("%s: durum %d", name, code)
		}
	}
}

func TestPlayRejections(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(f *fixture) string{
		"imza yok":         func(f *fixture) string { return "" },
		"bozuk imza":       func(f *fixture) string { return "?token=1.1.1.AAAA" },
		"süresi dolmuş":    func(f *fixture) string { return f.tokenFor(f.viewer, f.channel, now) },
		"olmayan izleyici": func(f *fixture) string { return f.tokenFor(f.viewer+999, f.channel, now.Add(time.Minute)) },
		"askıda izleyici": func(f *fixture) string {
			f.store.SetViewerStatus(ctx, f.viewer, "suspended")
			return f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
		},
		"süresi dolmuş izleyici": func(f *fixture) string {
			past := now.Add(-time.Hour)
			f.store.SetViewerExpiry(ctx, f.viewer, &past)
			return f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
		},
		"başka yayıncının izleyicisi": func(f *fixture) string {
			t2 := must(f.store.CreateTenant(ctx, "t2"))
			v2 := must(f.store.CreateViewer(ctx, t2, "veli", "pw", 1))
			return f.tokenFor(v2, f.channel, now.Add(time.Minute))
		},
	}
	for name, makeParam := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.publish("a", "?secret=sek")
			if code := f.play(fmt.Sprint(f.channel), makeParam(f)); code != http.StatusForbidden {
				t.Fatalf("durum %d", code)
			}
		})
	}
}

func TestPlayRejectsOfflineChannel(t *testing.T) {
	f := setup(t)
	param := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
	if code := f.play(fmt.Sprint(f.channel), param); code != http.StatusForbidden {
		t.Fatalf("durum %d", code)
	}
}

func TestPlayOpensASessionAndStopClosesIt(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	ch := fmt.Sprint(f.channel)
	if code := f.play(ch, f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
	if n := f.sessions(f.viewer); n != 1 {
		t.Fatalf("izleme başlayınca oturum açılmalı: %d", n)
	}
	if code := f.post(hookSecret, "stop", event("p1", "live", ch, "")); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
	if n := f.sessions(f.viewer); n != 0 {
		t.Fatalf("izleme bitince oturum kapanmalı: %d", n)
	}
}

func TestRejectedPlayOpensNoSession(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	f.play(fmt.Sprint(f.channel), "?token=1.1.1.AAAA")
	if n := f.sessions(f.viewer); n != 0 {
		t.Fatalf("reddedilen izleme oturum açmamalı: %d", n)
	}
}

func TestPlayBeyondConnectionLimitKicksTheOldestConnection(t *testing.T) {
	f := setup(t) // izleyicinin bağlantı limiti 1
	f.publish("a", "?secret=sek")
	ch, param := fmt.Sprint(f.channel), f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
	for _, client := range []string{"p1", "p2"} {
		if code := f.playAs(client, ch, param); code != http.StatusOK {
			t.Fatalf("%s: durum %d", client, code)
		}
	}
	if f.srs.Kicked() != "[p1]" {
		t.Fatalf("eski bağlantı kesilmeli: %s", f.srs.Kicked())
	}
	if n := f.sessions(f.viewer); n != 1 {
		t.Fatalf("limit 1 iken etkin oturum sayısı %d", n)
	}
}

func TestPlayRejectedWhenTenantConnectionQuotaIsFull(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.publish("a", "?secret=sek")
	if err := f.store.SetTenantQuotas(ctx, f.tenant, store.Quotas{MaxChannels: 10, MaxViewers: 10, MaxConnections: 1}); err != nil {
		t.Fatal(err)
	}
	other := must(f.store.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	ch := fmt.Sprint(f.channel)
	if code := f.playAs("p1", ch, f.tokenFor(other, f.channel, now.Add(time.Minute))); code != http.StatusOK {
		t.Fatalf("ilk izleyici: durum %d", code)
	}
	if code := f.playAs("p2", ch, f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))); code != http.StatusForbidden {
		t.Fatalf("kota doluyken ikinci izleyici reddedilmeli: durum %d", code)
	}
	if f.srs.Kicked() != "[]" {
		t.Fatalf("kota yüzünden kimse yerinden edilmemeli: %s", f.srs.Kicked())
	}
}

// Origin'e RTMP ile bağlanıp izlemek bir izleyici yolu değildir; geçerli imzayla bile reddedilir.
func TestOriginPlayIsRejectedForViewers(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	body := event("p1", "live", fmt.Sprint(f.channel), f.tokenFor(f.viewer, f.channel, now.Add(time.Minute)))
	if code := f.post(hookSecret, "play-origin", body); code != http.StatusForbidden {
		t.Fatalf("durum %d", code)
	}
	if n := f.sessions(f.viewer); n != 0 {
		t.Fatalf("oturum açılmamalı: %d", n)
	}
}

func TestStopIsAccepted(t *testing.T) {
	f := setup(t)
	if code := f.post(hookSecret, "stop", event("p1", "live", fmt.Sprint(f.channel), "")); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}
