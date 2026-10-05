package session_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"streamhub/internal/session"
	"streamhub/internal/srsapi"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

// fakeSRS, bir SRS'in bağlantı listesini ve kesme isteklerini taklit eder.
type fakeSRS struct {
	mu      sync.Mutex
	clients []string
	listErr error
	kickErr error
	kicked  []string
}

func (f *fakeSRS) ClientIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clients, f.listErr
}

func (f *fakeSRS) Kick(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kicked = append(f.kicked, id)
	return f.kickErr
}

func (f *fakeSRS) got() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprint(f.kicked)
}

type fixture struct {
	t          *testing.T
	s          *store.Store
	m          *session.Manager
	ts, origin *fakeSRS
	tenant     int64
	channel    int64
	viewer     int64
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *fixture {
	ctx := context.Background()
	f := &fixture{t: t, s: testdb.New(t), ts: &fakeSRS{}, origin: &fakeSRS{}}
	f.tenant = must(f.s.CreateTenant(ctx, "t"))
	f.channel = must(f.s.CreateChannel(ctx, f.tenant, "c", "sek"))
	f.viewer = must(f.s.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	f.m = session.New(f.s, f.ts, f.origin, 30*time.Second, 6*time.Hour)
	return f
}

func (f *fixture) active() int {
	return must(f.m.ActiveCount(context.Background(), f.viewer))
}

func TestOpeningASessionKicksTheEvictedTSConnection(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ok(t, f.m.OpenTS(ctx, f.viewer, f.channel, "c1", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, f.viewer, f.channel, "c2", "1.1.1.1"))
	if f.ts.got() != "[c1]" {
		t.Fatalf("yerinden edilen .ts bağlantısı kesilmeli: %s", f.ts.got())
	}
	ok(t, f.m.TouchHLS(ctx, f.viewer, f.channel, "k1", "1.1.1.1"))
	if f.ts.got() != "[c1 c2]" {
		t.Fatalf("HLS oturumu açılınca eski .ts bağlantısı kesilmeli: %s", f.ts.got())
	}
	// Yerinden edilen HLS oturumu için SRS'te kesilecek bir bağlantı yoktur.
	ok(t, f.m.TouchHLS(ctx, f.viewer, f.channel, "k2", "1.1.1.1"))
	if f.ts.got() != "[c1 c2]" || f.origin.got() != "[]" {
		t.Fatalf("fazladan kesme isteği: ts=%s origin=%s", f.ts.got(), f.origin.got())
	}
	if err := f.m.TouchHLS(ctx, f.viewer, f.channel, "k1", "1.1.1.1"); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("yerinden edilen HLS oturumu reddedilmeli, gelen: %v", err)
	}
}

func TestKickFailureDoesNotFailTheNewSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.ts.kickErr = errors.New("SRS yanıt vermiyor")
	ok(t, f.m.OpenTS(ctx, f.viewer, f.channel, "c1", "1.1.1.1"))
	if err := f.m.OpenTS(ctx, f.viewer, f.channel, "c2", "1.1.1.1"); err != nil {
		t.Fatalf("kesme başarısız olsa da yeni oturum açılmalı: %v", err)
	}
}

func TestCloseTS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ok(t, f.m.OpenTS(ctx, f.viewer, f.channel, "c1", "1.1.1.1"))
	if f.active() != 1 {
		t.Fatal("oturum açılmalıydı")
	}
	ok(t, f.m.CloseTS(ctx, "c1"))
	if f.active() != 0 {
		t.Fatal("oturum kapanmalıydı")
	}
}

func TestEnforceKicksRevokedAndUnentitledConnections(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := must(f.s.CreateTenant(ctx, "askidaki"))
	theirChannel := must(f.s.CreateChannel(ctx, other, "c", "s"))
	must(f.s.MarkLive(ctx, theirChannel, "yayinci-1"))
	must(f.s.MarkLive(ctx, f.channel, "yayinci-saglam"))
	banned := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	ok(t, f.m.OpenTS(ctx, f.viewer, f.channel, "saglam", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, banned, f.channel, "askida", "1.1.1.1"))
	f.ts.clients = []string{"saglam", "askida"}

	ok(t, f.m.EnforceOnce(ctx))
	if f.ts.got() != "[]" || f.origin.got() != "[]" {
		t.Fatalf("herkes yetkiliyken kimse kesilmemeli: ts=%s origin=%s", f.ts.got(), f.origin.got())
	}

	ok(t, f.s.SetViewerStatus(ctx, banned, "suspended"))
	ok(t, f.s.SetTenantStatus(ctx, other, "suspended"))
	ok(t, f.m.EnforceOnce(ctx))
	if f.ts.got() != "[askida]" {
		t.Fatalf("askıdaki izleyicinin bağlantısı kesilmeli: %s", f.ts.got())
	}
	if f.origin.got() != "[yayinci-1]" {
		t.Fatalf("askıdaki yayıncının yayını kesilmeli: %s", f.origin.got())
	}
}

// .ts dağıtıcısı yanıt vermese de askıdaki yayıncının yayını origin'de kesilmeli.
func TestEnforceStepsAreIndependent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := must(f.s.CreateTenant(ctx, "askidaki"))
	theirChannel := must(f.s.CreateChannel(ctx, other, "c", "s"))
	must(f.s.MarkLive(ctx, theirChannel, "yayinci-1"))
	for i, name := range []string{"a", "b", "c"} {
		v := must(f.s.CreateViewer(ctx, other, name, "pw", 1))
		ok(t, f.m.OpenTS(ctx, v, theirChannel, fmt.Sprintf("izleyici-%d", i), "1.1.1.1"))
	}
	ok(t, f.s.SetTenantStatus(ctx, other, "suspended"))

	f.ts.kickErr = errors.New("SRS yanıt vermiyor")
	f.ts.listErr = errors.New("SRS yanıt vermiyor")
	if err := f.m.EnforceOnce(ctx); err == nil {
		t.Fatal("hata bildirilmeliydi")
	}
	if f.origin.got() != "[yayinci-1]" {
		t.Fatalf("origin adımı .ts dağıtıcısındaki hatadan etkilenmemeli: %s", f.origin.got())
	}
	// Yanıt vermeyen bir SRS'e aynı geçişte tekrar tekrar gidilmez.
	if f.ts.got() != "[izleyici-0]" {
		t.Fatalf("ilk hatadan sonra o SRS için kesme denemeleri durmalı: %s", f.ts.got())
	}
}

// Silinen bir kanalın bağlantıları kuyrukta bekler: SRS'e ulaşılamazsa sonraki geçişte yeniden denenir.
func TestQueuedKicksAreRetriedUntilTheySucceed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	must(f.s.MarkLive(ctx, f.channel, "yayinci-1"))
	ok(t, f.m.OpenTS(ctx, f.viewer, f.channel, "izleyen-1", "1.1.1.1"))
	ok(t, f.s.DeleteChannel(ctx, f.tenant, f.channel))

	f.ts.kickErr = errors.New("SRS yanıt vermiyor")
	f.origin.kickErr = errors.New("SRS yanıt vermiyor")
	f.m.Flush(ctx)
	if got := fmt.Sprint(must(f.s.PendingKicks(ctx, "origin")), must(f.s.PendingKicks(ctx, "ts"))); got != "[yayinci-1] [izleyen-1]" {
		t.Fatalf("kesilemeyen bağlantılar kuyrukta kalmalı: %s", got)
	}

	f.ts.kickErr, f.origin.kickErr = nil, nil
	ok(t, f.m.EnforceOnce(ctx))
	if f.origin.got() != "[yayinci-1 yayinci-1]" || f.ts.got() != "[izleyen-1 izleyen-1]" {
		t.Fatalf("yeniden denenmeli: origin=%s ts=%s", f.origin.got(), f.ts.got())
	}
	if got := fmt.Sprint(must(f.s.PendingKicks(ctx, "origin")), must(f.s.PendingKicks(ctx, "ts"))); got != "[] []" {
		t.Fatalf("kesilen bağlantılar kuyruktan çıkmalı: %s", got)
	}
	ok(t, f.m.EnforceOnce(ctx))
	if f.origin.got() != "[yayinci-1 yayinci-1]" {
		t.Fatalf("kesilmiş bağlantı yeniden kesilmemeli: %s", f.origin.got())
	}
}

// SRS "böyle bir bağlantı yok" derse iş bitmiştir; kayıt kuyrukta kalıp sonsuza dek denenmez.
func TestQueuedKickForAGoneConnectionIsResolved(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	must(f.s.MarkLive(ctx, f.channel, "yayinci-1"))
	ok(t, f.s.DeleteChannel(ctx, f.tenant, f.channel))
	f.origin.kickErr = &srsapi.StatusError{Op: "DELETE", Code: 2049}
	f.m.Flush(ctx)
	if got := must(f.s.PendingKicks(ctx, "origin")); len(got) != 0 {
		t.Fatalf("var olmayan bağlantı kuyruktan çıkmalı: %v", got)
	}
}

func TestEnforceRemovesSessionsMissingFromSRS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ok(t, f.m.OpenTS(ctx, f.viewer, f.channel, "kayip", "1.1.1.1"))
	testdb.Exec(t, `UPDATE sessions SET started_at = now() - interval '1 minute'`)

	// SRS'e ulaşılamazsa oturumlara dokunulmaz.
	f.ts.listErr = errors.New("SRS yanıt vermiyor")
	if err := f.m.EnforceOnce(ctx); err == nil {
		t.Fatal("SRS hatası bildirilmeliydi")
	}
	if f.active() != 1 {
		t.Fatal("SRS'e ulaşılamadığında oturum silinmemeli")
	}

	f.ts.listErr = nil
	f.ts.clients = []string{"baska"}
	ok(t, f.m.EnforceOnce(ctx))
	if f.active() != 0 {
		t.Fatal("SRS'te olmayan .ts oturumu silinmeli")
	}
}

func TestEnforceDeletesExpiredHLSSessions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ok(t, f.m.TouchHLS(ctx, f.viewer, f.channel, "k1", "1.1.1.1"))
	ok(t, f.m.TouchHLS(ctx, f.viewer, f.channel, "k2", "1.1.1.1")) // k1 sonlandırıldı
	testdb.Exec(t, `UPDATE sessions SET last_seen_at = now() - interval '5 hours' WHERE session_key = 'k1'`)
	ok(t, f.m.EnforceOnce(ctx))
	if err := f.m.TouchHLS(ctx, f.viewer, f.channel, "k1", "1.1.1.1"); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("imza ömrü dolmadan sonlandırılmış oturum kaydı silinmemeli, gelen: %v", err)
	}

	testdb.Exec(t, `UPDATE sessions SET last_seen_at = now() - interval '7 hours' WHERE session_key = 'k1'`)
	ok(t, f.m.EnforceOnce(ctx))
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 1 {
		t.Fatalf("imza ömrü dolan oturum kaydı silinmeli, kalan: %d", n)
	}
}
