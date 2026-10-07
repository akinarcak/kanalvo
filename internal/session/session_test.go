package session_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"kanalvo/internal/session"
	"kanalvo/internal/srsapi"
	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

// fakeSRS, bir SRS'in bağlantı listesini ve kesme isteklerini taklit eder.
type fakeSRS struct {
	mu      sync.Mutex
	clients []string
	listErr error
	kickErr error
	kicked  []string
	// hang: istekler yanıt vermez, bağlamın süresi dolana kadar bekler.
	hang bool
	// kickDelay: her kesme isteği bu kadar sürer (yavaş ama yanıt veren SRS).
	kickDelay time.Duration
}

func (f *fakeSRS) wait(ctx context.Context) error {
	f.mu.Lock()
	hang := f.hang
	f.mu.Unlock()
	if !hang {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeSRS) ClientIDs(ctx context.Context) ([]string, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clients, f.listErr
}

func (f *fakeSRS) Kick(ctx context.Context, id string) error {
	f.mu.Lock()
	f.kicked = append(f.kicked, id)
	delay := f.kickDelay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := f.wait(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
	// remote: uzak edge'lerin SRS'leri (edge numarasına göre).
	remote  map[int64]*fakeSRS
	local   int64
	tenant  int64
	channel int64
	viewer  int64
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
	f := &fixture{t: t, s: testdb.New(t), ts: &fakeSRS{}, origin: &fakeSRS{}, remote: map[int64]*fakeSRS{}}
	f.local = testdb.LocalEdge(t)
	f.tenant = must(f.s.CreateTenant(ctx, "t"))
	f.channel = must(f.s.CreateChannel(ctx, f.tenant, "c", "sek"))
	f.viewer = must(f.s.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	srsFor := func(e store.Edge) session.SRS {
		if e.Builtin {
			return f.ts
		}
		return f.remote[e.ID]
	}
	f.m = session.New(f.s, srsFor, f.origin, 30*time.Second, 6*time.Hour)
	return f
}

// addEdge, kendi sahte SRS'i olan bir uzak edge kaydeder.
func (f *fixture) addEdge(name string) (int64, *fakeSRS) {
	id := must(f.s.CreateEdge(context.Background(), store.NewEdge{
		Name: name, BaseURL: "http://" + name, ControlURL: "http://" + name, Key: "key-" + name, PullIP: "10.0.0.5", Weight: 100,
	}))
	f.remote[id] = &fakeSRS{}
	return id, f.remote[id]
}

func (f *fixture) healthy(id int64) bool {
	return must(f.s.EdgeByID(context.Background(), id, 15*time.Second)).Healthy
}

func (f *fixture) active() int {
	return must(f.m.ActiveCount(context.Background(), f.viewer))
}

func TestOpeningASessionKicksTheEvictedTSConnection(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "c1", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "c2", "1.1.1.1"))
	if f.ts.got() != "[c1]" {
		t.Fatalf("yerinden edilen .ts bağlantısı kesilmeli: %s", f.ts.got())
	}
	ok(t, f.m.TouchHLS(ctx, f.local, f.viewer, f.channel, "k1", "1.1.1.1"))
	if f.ts.got() != "[c1 c2]" {
		t.Fatalf("HLS oturumu açılınca eski .ts bağlantısı kesilmeli: %s", f.ts.got())
	}
	// Yerinden edilen HLS oturumu için SRS'te kesilecek bir bağlantı yoktur.
	ok(t, f.m.TouchHLS(ctx, f.local, f.viewer, f.channel, "k2", "1.1.1.1"))
	if f.ts.got() != "[c1 c2]" || f.origin.got() != "[]" {
		t.Fatalf("fazladan kesme isteği: ts=%s origin=%s", f.ts.got(), f.origin.got())
	}
	if err := f.m.TouchHLS(ctx, f.local, f.viewer, f.channel, "k1", "1.1.1.1"); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("yerinden edilen HLS oturumu reddedilmeli, gelen: %v", err)
	}
}

func TestKickFailureDoesNotFailTheNewSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.ts.kickErr = errors.New("SRS yanıt vermiyor")
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "c1", "1.1.1.1"))
	if err := f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "c2", "1.1.1.1"); err != nil {
		t.Fatalf("kesme başarısız olsa da yeni oturum açılmalı: %v", err)
	}
}

func TestCloseTS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "c1", "1.1.1.1"))
	if f.active() != 1 {
		t.Fatal("oturum açılmalıydı")
	}
	ok(t, f.m.CloseTS(ctx, f.local, "c1"))
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
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "saglam", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, f.local, banned, f.channel, "askida", "1.1.1.1"))
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
		ok(t, f.m.OpenTS(ctx, f.local, v, theirChannel, fmt.Sprintf("izleyici-%d", i), "1.1.1.1"))
	}
	ok(t, f.s.SetTenantStatus(ctx, other, "suspended"))
	ok(t, f.s.MarkEdgeSeen(ctx, f.local)) // kısa bir kesinti: oturum kayıtları korunur

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
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "izleyen-1", "1.1.1.1"))
	ok(t, f.s.DeleteChannel(ctx, f.tenant, f.channel))

	f.ts.kickErr = errors.New("SRS yanıt vermiyor")
	f.origin.kickErr = errors.New("SRS yanıt vermiyor")
	f.m.Flush(ctx)
	if got := fmt.Sprint(must(f.s.PendingOriginKicks(ctx)), must(f.s.PendingEdgeKicks(ctx, f.local))); got != "[yayinci-1] [izleyen-1]" {
		t.Fatalf("kesilemeyen bağlantılar kuyrukta kalmalı: %s", got)
	}

	f.ts.kickErr, f.origin.kickErr = nil, nil
	ok(t, f.m.EnforceOnce(ctx))
	if f.origin.got() != "[yayinci-1 yayinci-1]" || f.ts.got() != "[izleyen-1 izleyen-1]" {
		t.Fatalf("yeniden denenmeli: origin=%s ts=%s", f.origin.got(), f.ts.got())
	}
	if got := fmt.Sprint(must(f.s.PendingOriginKicks(ctx)), must(f.s.PendingEdgeKicks(ctx, f.local))); got != "[] []" {
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
	if got := must(f.s.PendingOriginKicks(ctx)); len(got) != 0 {
		t.Fatalf("var olmayan bağlantı kuyruktan çıkmalı: %v", got)
	}
}

func TestEnforceRemovesSessionsMissingFromSRS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "kayip", "1.1.1.1"))
	testdb.Exec(t, `UPDATE sessions SET started_at = now() - interval '1 minute'`)

	// SRS'e kısa süredir ulaşılamıyorsa oturumlara dokunulmaz.
	ok(t, f.s.MarkEdgeSeen(ctx, f.local))
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
	ok(t, f.m.TouchHLS(ctx, f.local, f.viewer, f.channel, "k1", "1.1.1.1"))
	ok(t, f.m.TouchHLS(ctx, f.local, f.viewer, f.channel, "k2", "1.1.1.1")) // k1 sonlandırıldı
	testdb.Exec(t, `UPDATE sessions SET last_seen_at = now() - interval '5 hours' WHERE session_key = 'k1'`)
	ok(t, f.m.EnforceOnce(ctx))
	if err := f.m.TouchHLS(ctx, f.local, f.viewer, f.channel, "k1", "1.1.1.1"); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("imza ömrü dolmadan sonlandırılmış oturum kaydı silinmemeli, gelen: %v", err)
	}

	testdb.Exec(t, `UPDATE sessions SET last_seen_at = now() - interval '7 hours' WHERE session_key = 'k1'`)
	ok(t, f.m.EnforceOnce(ctx))
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 1 {
		t.Fatalf("imza ömrü dolan oturum kaydı silinmeli, kalan: %d", n)
	}
}

// Her edge kendi SRS'i üzerinden denetlenir: kesme ve eşitleme başka edge'in bağlantılarına dokunmaz.
func TestEachEdgeIsEnforcedThroughItsOwnSRS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	remote, remoteSRS := f.addEdge("e1")
	banned := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 2))
	ok(t, f.m.OpenTS(ctx, f.local, banned, f.channel, "yerel-askida", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, remote, banned, f.channel, "uzak-askida", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, remote, f.viewer, f.channel, "uzak-kayip", "1.1.1.1"))
	testdb.Exec(t, `UPDATE sessions SET started_at = now() - interval '1 minute'`)
	f.ts.clients = []string{"yerel-askida"}
	// Uzak SRS'te "uzak-kayip" yok; yerel SRS'in listesinde olmaması onu ilgilendirmez.
	remoteSRS.clients = []string{"uzak-askida", "yerel-askida"}
	ok(t, f.s.SetViewerStatus(ctx, banned, "suspended"))

	ok(t, f.m.EnforceOnce(ctx))
	if f.ts.got() != "[yerel-askida]" || remoteSRS.got() != "[uzak-askida]" {
		t.Fatalf("her bağlantı kendi edge'inde kesilmeli: yerel=%s uzak=%s", f.ts.got(), remoteSRS.got())
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE session_key = 'uzak-kayip'`); n != 0 {
		t.Fatal("uzak SRS'te olmayan oturum silinmeliydi")
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 2 {
		t.Fatalf("SRS'lerde süren iki oturum kalmalıydı: %d", n)
	}
	if !f.healthy(f.local) || !f.healthy(remote) {
		t.Fatal("yanıt veren edge'lerin sağlık sinyali kaydedilmeliydi")
	}
}

// Ulaşılamayan edge sağlık sinyali vermez ve diğer edge'lerin denetimini engellemez.
func TestUnreachableEdgeLosesHealthWithoutBlockingOthers(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	remote, remoteSRS := f.addEdge("e1")
	ok(t, f.m.OpenTS(ctx, remote, f.viewer, f.channel, "uzak-c", "1.1.1.1"))
	banned := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	ok(t, f.m.OpenTS(ctx, f.local, banned, f.channel, "yerel-askida", "1.1.1.1"))
	testdb.Exec(t, `UPDATE sessions SET started_at = now() - interval '1 minute'`)
	ok(t, f.s.SetViewerStatus(ctx, banned, "suspended"))
	f.ts.clients = []string{"yerel-askida"}
	remoteSRS.listErr = errors.New("bağlantı reddedildi")
	remoteSRS.kickErr = errors.New("bağlantı reddedildi")

	err := f.m.EnforceOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), `edge "e1"`) {
		t.Fatalf("hata ulaşılamayan edge'i adıyla bildirmeli: %v", err)
	}
	if f.ts.got() != "[yerel-askida]" {
		t.Fatalf("yerel edge'in denetimi sürmeliydi: %s", f.ts.got())
	}
	if !f.healthy(f.local) || f.healthy(remote) {
		t.Fatalf("sağlık: yerel=%v uzak=%v", f.healthy(f.local), f.healthy(remote))
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE session_key = 'uzak-c'`); n != 1 {
		t.Fatal("ulaşılamayan edge'in oturumu silinmemeliydi")
	}

	remoteSRS.listErr, remoteSRS.kickErr = nil, nil
	remoteSRS.clients = []string{"uzak-c"}
	ok(t, f.m.EnforceOnce(ctx))
	if !f.healthy(remote) {
		t.Fatal("yeniden yanıt veren edge sağlıklı sayılmalıydı")
	}
}

// Limit dolunca yerinden edilen bağlantı, bulunduğu edge'in SRS'inde kesilir.
func TestEvictedConnectionIsKickedOnItsOwnEdge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	remote, remoteSRS := f.addEdge("e1")
	ok(t, f.m.OpenTS(ctx, remote, f.viewer, f.channel, "uzak-c", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "yerel-c", "1.1.1.1"))
	if remoteSRS.got() != "[uzak-c]" || f.ts.got() != "[]" {
		t.Fatalf("yerinden edilen bağlantı kendi edge'inde kesilmeli: uzak=%s yerel=%s", remoteSRS.got(), f.ts.got())
	}
}

// Uzun süredir ulaşılamayan bir edge'in .ts oturumları silinir: aksi halde kapanmış bir sunucunun
// izleyicileri yayıncının bağlantı kotasını süresiz doldururdu.
func TestSessionsOnALongUnreachableEdgeStopCounting(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	remote, remoteSRS := f.addEdge("e1")
	ok(t, f.m.OpenTS(ctx, remote, f.viewer, f.channel, "uzak-c", "1.1.1.1"))
	other := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	ok(t, f.m.OpenTS(ctx, f.local, other, f.channel, "yerel-c", "1.1.1.1"))
	ok(t, f.m.TouchHLS(ctx, remote, must(f.s.CreateViewer(ctx, f.tenant, "ayse", "pw", 1)), f.channel, "a1", "1.1.1.1"))
	f.ts.clients = []string{"yerel-c"}
	remoteSRS.listErr = errors.New("bağlantı reddedildi")

	// Kısa bir kesintide oturumlara dokunulmaz.
	testdb.Exec(t, `UPDATE edges SET last_seen_at = now() - interval '30 seconds' WHERE id = $1`, remote)
	_ = f.m.EnforceOnce(ctx)
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE session_key = 'uzak-c'`); n != 1 {
		t.Fatal("kısa süredir ulaşılamayan edge'in oturumu silinmemeliydi")
	}

	testdb.Exec(t, `UPDATE edges SET last_seen_at = now() - interval '3 minutes' WHERE id = $1`, remote)
	_ = f.m.EnforceOnce(ctx)
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE session_key = 'uzak-c'`); n != 0 {
		t.Fatal("uzun süredir ulaşılamayan edge'in .ts oturumu silinmeliydi")
	}
	// Başka edge'in oturumu ve HLS kayıtları (sonlandırılmış adresleri kapalı tutar) kalır.
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 2 {
		t.Fatalf("yerel .ts oturumu ve HLS kaydı kalmalıydı: %d", n)
	}
}

// Yanıt vermeyen bir edge, denetim geçişini kendi süresinden fazla uzatmaz: diğer edge'lerin
// sağlık sinyali ve kesmeleri bir sonraki geçişe zamanında yetişir.
func TestHungEdgeDoesNotStretchThePass(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	session.SetEdgeTimeout(t, 300*time.Millisecond)
	remote, remoteSRS := f.addEdge("e1")
	ok(t, f.m.OpenTS(ctx, remote, f.viewer, f.channel, "uzak-c", "1.1.1.1"))
	remoteSRS.hang = true

	start := time.Now()
	err := f.m.EnforceOnce(ctx)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("yanıt vermeyen edge geçişi %s uzattı", took)
	}
	if err == nil || !strings.Contains(err.Error(), `edge "e1"`) {
		t.Fatalf("hata yanıt vermeyen edge'i adıyla bildirmeli: %v", err)
	}
	if !f.healthy(f.local) || f.healthy(remote) {
		t.Fatalf("sağlık: yerel=%v uzak=%v", f.healthy(f.local), f.healthy(remote))
	}
}

// Ulaşılamadığı bilinen bir edge, panel işlemlerini ve yeni izlemeleri bekletmez: kesmeleri
// denetim döngüsüne bırakılır, edge yeniden yanıt verince istek sırasındaki kesmeler yeniden başlar.
func TestKnownUnreachableEdgeIsLeftToTheLoop(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	remote, remoteSRS := f.addEdge("e1")
	two := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	ok(t, f.m.OpenTS(ctx, remote, two, f.channel, "uzak-1", "1.1.1.1"))
	remoteSRS.listErr = errors.New("bağlantı reddedildi")
	remoteSRS.kickErr = errors.New("bağlantı reddedildi")
	if err := f.m.EnforceOnce(ctx); err == nil {
		t.Fatal("hata bildirilmeliydi")
	}

	// Yerinden edilen bağlantı ulaşılamayan edge'de: istek sırasında denenmez.
	ok(t, f.m.OpenTS(ctx, f.local, two, f.channel, "yerel-1", "1.1.1.1"))
	f.m.Flush(ctx)
	if remoteSRS.got() != "[]" {
		t.Fatalf("ulaşılamayan edge'e istek sırasında gidildi: %s", remoteSRS.got())
	}

	// Döngü denemeyi sürdürür; edge dönünce bağlantı kesilir.
	remoteSRS.listErr, remoteSRS.kickErr = nil, nil
	remoteSRS.clients = []string{"uzak-1"}
	ok(t, f.m.EnforceOnce(ctx))
	if remoteSRS.got() != "[uzak-1]" {
		t.Fatalf("yeniden yanıt veren edge'de bekleyen bağlantı kesilmeli: %s", remoteSRS.got())
	}
	// Edge yanıt verdiğine göre kesmeler yine istek sırasında yapılır.
	ok(t, f.m.OpenTS(ctx, remote, two, f.channel, "uzak-2", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, f.local, two, f.channel, "yerel-2", "1.1.1.1"))
	if remoteSRS.got() != "[uzak-1 uzak-2]" {
		t.Fatalf("yanıt veren edge'de kesme istek sırasında yapılmalı: %s", remoteSRS.got())
	}
}

// Süren bir arızanın metnindeki geçici bağlantı noktaları değişse de log anahtarı aynı kalır.
func TestFailureKeyIgnoresEphemeralPorts(t *testing.T) {
	a := errors.New(`edge "e1": read tcp 172.18.0.3:51234->172.18.0.7:80: read: connection reset by peer`)
	b := errors.New(`edge "e1": read tcp 172.18.0.3:40002->172.18.0.7:80: read: connection reset by peer`)
	c := errors.New(`edge "e2": read tcp 172.18.0.3:40002->172.18.0.7:80: read: connection reset by peer`)
	if session.FailureKey(a) != session.FailureKey(b) {
		t.Fatalf("aynı arıza farklı sayıldı: %q / %q", session.FailureKey(a), session.FailureKey(b))
	}
	if session.FailureKey(a) == session.FailureKey(c) {
		t.Fatal("başka edge'in arızası aynı sayıldı")
	}
	if session.FailureKey(nil) != "" {
		t.Fatal("hata yokken anahtar boş olmalı")
	}
}

// Kesilecek çok bağlantısı olan bir edge sağlıklıdır: kesmeler bir geçişe sığmasa da sağlık sinyali
// kaydedilir, edge yönlendirmeden çıkmaz ve kalan kesmeler sonraki geçişlerde sürer.
func TestKickBacklogDoesNotCostAnEdgeItsHealth(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	session.SetEdgeTimeout(t, 300*time.Millisecond)
	remote, remoteSRS := f.addEdge("e1")
	crowd := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 5))
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("uzak-%d", i)
		ok(t, f.m.OpenTS(ctx, remote, crowd, f.channel, id, "1.1.1.1"))
		remoteSRS.clients = append(remoteSRS.clients, id)
	}
	ok(t, f.s.SetViewerStatus(ctx, crowd, "suspended"))
	remoteSRS.kickDelay = 200 * time.Millisecond

	_ = f.m.EnforceOnce(ctx) // bütçe hepsini kesmeye yetmez
	if !f.healthy(remote) {
		t.Fatal("yanıt veren edge, kesmeleri bitmedi diye sağlıksız sayıldı")
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE edge_id = $1`, remote); n != 5 {
		t.Fatalf("kesilemeyen bağlantıların kayıtları kalmalıydı: %d", n)
	}
	// Edge ulaşılamaz sayılmadığı için panel işlemleri onu atlamaz.
	before := remoteSRS.got()
	f.m.Flush(ctx)
	if remoteSRS.got() == before {
		t.Fatal("yanıt veren edge istek sırasındaki kesmelerde atlandı")
	}
}

// Log anahtarı yalnızca her denemede değişen kısımları atar: geçici bağlantı noktası ve kesilmek
// istenen bağlantının kimliği. Edge adı, hedef adres ve hata kodu korunur.
func TestFailureKeyKeepsWhatIdentifiesTheFailure(t *testing.T) {
	key := func(s string) string { return session.FailureKey(errors.New(s)) }
	if key(`edge "e1": srsapi: DELETE /api/v1/clients/abc123: kod 2049`) != key(`edge "e1": srsapi: DELETE /api/v1/clients/x_9-z: kod 2049`) {
		t.Fatal("yalnızca bağlantı kimliği farklı olan arızalar aynı sayılmalı")
	}
	for name, pair := range map[string][2]string{
		"hata kodu":   {`srsapi: DELETE /api/v1/clients/a: kod 2049`, `srsapi: DELETE /api/v1/clients/a: kod 1000`},
		"edge adı":    {`edge "fra:1": bağlantı reddedildi`, `edge "fra:2": bağlantı reddedildi`},
		"hedef adres": {`dial tcp 10.0.0.5:80: connect: connection refused`, `dial tcp 10.0.0.5:8443: connect: connection refused`},
	} {
		if key(pair[0]) == key(pair[1]) {
			t.Errorf("%s farklıyken arızalar aynı sayıldı: %q", name, key(pair[0]))
		}
	}
}

// Silinen bir edge'in "ulaşılamıyor" kaydı bellekte kalmaz.
func TestDeletedEdgeIsForgotten(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	remote, remoteSRS := f.addEdge("e1")
	remoteSRS.listErr = errors.New("bağlantı reddedildi")
	_ = f.m.EnforceOnce(ctx)
	if session.DownCount(f.m) != 1 {
		t.Fatalf("ulaşılamayan edge kaydedilmeliydi: %d", session.DownCount(f.m))
	}
	ok(t, f.s.DeleteEdge(ctx, remote))
	ok(t, f.m.EnforceOnce(ctx))
	if session.DownCount(f.m) != 0 {
		t.Fatalf("silinen edge'in kaydı kalmamalıydı: %d", session.DownCount(f.m))
	}
}

// Bağlantı listesini veremeyen ama kesme isteklerini yanıtlayan bir SRS'te (ör. liste yanıtı
// bozuk ya da çok yavaş) izleme hakkını yitirenler yine kesilir; edge yalnızca sağlık sinyali vermez.
func TestKicksStillRunWhenOnlyTheClientListFails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	remote, remoteSRS := f.addEdge("e1")
	banned := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	ok(t, f.m.OpenTS(ctx, remote, banned, f.channel, "uzak-askida", "1.1.1.1"))
	ok(t, f.s.SetViewerStatus(ctx, banned, "suspended"))
	remoteSRS.listErr = errors.New("srsapi: bağlantı listesi yanıtında clients alanı yok")

	if err := f.m.EnforceOnce(ctx); err == nil {
		t.Fatal("liste hatası bildirilmeliydi")
	}
	if remoteSRS.got() != "[uzak-askida]" {
		t.Fatalf("liste alınamasa da askıdaki izleyici kesilmeliydi: %s", remoteSRS.got())
	}
	if f.healthy(remote) {
		t.Fatal("listesi alınamayan edge sağlık sinyali vermiş sayılmamalı")
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE session_key = 'uzak-askida'`); n != 1 {
		t.Fatal("liste alınamadan oturum kaydı silinmemeliydi")
	}
}

// Kesilmek istenen bağlantı SRS'te zaten yoksa bu bir arıza değildir; kaydı eşitleme siler.
func TestKickingAnAlreadyGoneConnectionIsNotAFailure(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	banned := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	ok(t, f.m.OpenTS(ctx, f.local, banned, f.channel, "gitmis", "1.1.1.1"))
	testdb.Exec(t, `UPDATE sessions SET started_at = now() - interval '1 minute'`)
	ok(t, f.s.SetViewerStatus(ctx, banned, "suspended"))
	f.ts.kickErr = &srsapi.StatusError{Op: "DELETE", Code: 2049}

	if err := f.m.EnforceOnce(ctx); err != nil {
		t.Fatalf("zaten kopmuş bağlantı arıza sayıldı: %v", err)
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 0 {
		t.Fatalf("SRS'te olmayan oturumun kaydı silinmeliydi: %d", n)
	}
}

// Bağlantı listesi kesmelerden önce alınır. Kesmeler sürerken tanınan süreyi dolduran yeni bir
// oturum, liste alındığında henüz SRS'te görünmüyor olabilir; listede yok diye silinmemelidir.
func TestSlowKicksDoNotCostANewSessionItsGrace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	banned := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 2))
	ok(t, f.m.OpenTS(ctx, f.local, banned, f.channel, "askida-1", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, f.local, banned, f.channel, "askida-2", "1.1.1.1"))
	ok(t, f.m.OpenTS(ctx, f.local, f.viewer, f.channel, "yeni", "1.1.1.1"))
	testdb.Exec(t, `UPDATE sessions SET started_at = now() - interval '8 seconds' WHERE session_key = 'yeni'`)
	ok(t, f.s.SetViewerStatus(ctx, banned, "suspended"))
	f.ts.clients = []string{"askida-1", "askida-2"}
	f.ts.kickDelay = 1500 * time.Millisecond // iki kesme: liste ile eşitleme arasında 3 saniye

	ok(t, f.m.EnforceOnce(ctx))
	if f.ts.got() != "[askida-1 askida-2]" {
		t.Fatalf("askıdaki izleyicinin bağlantıları kesilmeliydi: %s", f.ts.got())
	}
	if n := testdb.Count(t, `SELECT count(*) FROM sessions WHERE session_key = 'yeni'`); n != 1 {
		t.Fatal("liste alındığında süresi dolmamış oturum, kesmeler uzun sürdü diye silindi")
	}
}
