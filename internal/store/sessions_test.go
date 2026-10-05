package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

const idle = 30 * time.Second

type sessionFixture struct {
	t       *testing.T
	s       *store.Store
	tenant  int64
	channel int64
	viewer  int64
}

// newSessionFixture: bir yayıncı, bir kanal ve bağlantı limiti maxConn olan bir izleyici.
func newSessionFixture(t *testing.T, maxConn int) *sessionFixture {
	s, ctx := testdb.New(t), context.Background()
	f := &sessionFixture{t: t, s: s}
	f.tenant = must(s.CreateTenant(ctx, "t"))
	f.channel = must(s.CreateChannel(ctx, f.tenant, "c", "sek"))
	f.viewer = must(s.CreateViewer(ctx, f.tenant, "ali", "pw", maxConn))
	return f
}

func (f *sessionFixture) hls(key, ip string) ([]store.Evicted, error) {
	return f.s.TouchHLSSession(context.Background(), f.viewer, f.channel, key, ip, idle)
}

func (f *sessionFixture) ts(clientID string) ([]store.Evicted, error) {
	return f.s.OpenTSSession(context.Background(), f.viewer, f.channel, clientID, "9.9.9.9", idle)
}

func (f *sessionFixture) active() int {
	return must(f.s.ActiveSessionCount(context.Background(), f.viewer, idle))
}

// age, bir oturumun son görülme zamanını geçmişe çeker.
func (f *sessionFixture) age(key string, by time.Duration) {
	testdb.Exec(f.t, `UPDATE sessions SET last_seen_at = now() - make_interval(secs => $2), started_at = started_at - make_interval(secs => $2) WHERE session_key = $1`,
		key, by.Seconds())
}

func wantEvicted(t *testing.T, got []store.Evicted, want ...store.Evicted) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("yerinden edilen oturumlar %v, beklenen %v", got, want)
	}
}

func TestHLSSessionOpensOnceAndStaysOne(t *testing.T) {
	f := newSessionFixture(t, 1)
	for i := 0; i < 3; i++ {
		evicted, err := f.hls("k1", "1.1.1.1")
		if err != nil {
			t.Fatal(err)
		}
		wantEvicted(t, evicted)
	}
	if n := f.active(); n != 1 {
		t.Fatalf("aynı oturum tek sayılmalı: %d", n)
	}
}

func TestNewSessionEvictsOldestWhenLimitReached(t *testing.T) {
	f := newSessionFixture(t, 1)
	must(f.hls("k1", "1.1.1.1"))
	wantEvicted(t, must(f.hls("k2", "1.1.1.1")), store.Evicted{Kind: "hls", Key: "k1"})

	if _, err := f.hls("k1", "1.1.1.1"); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("yerinden edilen oturum aynı adresle geri dönememeli, gelen: %v", err)
	}
	if _, err := f.hls("k2", "1.1.1.1"); err != nil {
		t.Fatalf("yeni oturum sürmeli: %v", err)
	}
	if n := f.active(); n != 1 {
		t.Fatalf("limit 1 iken etkin oturum sayısı %d", n)
	}
}

func TestSharedLinkFromAnotherAddressIsASeparateSession(t *testing.T) {
	f := newSessionFixture(t, 1)
	must(f.hls("k1", "1.1.1.1"))
	wantEvicted(t, must(f.hls("k1", "2.2.2.2")), store.Evicted{Kind: "hls", Key: "k1"})

	if _, err := f.hls("k1", "1.1.1.1"); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("ilk adres yerinden edilmiş olmalı, gelen: %v", err)
	}
	if _, err := f.hls("k1", "2.2.2.2"); err != nil {
		t.Fatalf("ikinci adres sürmeli: %v", err)
	}
}

func TestLimitTwoKeepsTwoNewest(t *testing.T) {
	f := newSessionFixture(t, 2)
	must(f.hls("k1", "1.1.1.1"))
	f.age("k1", time.Second) // sıralama belirli olsun
	wantEvicted(t, must(f.hls("k2", "1.1.1.1")))
	if n := f.active(); n != 2 {
		t.Fatalf("iki oturum birlikte sürmeli: %d", n)
	}
	wantEvicted(t, must(f.hls("k3", "1.1.1.1")), store.Evicted{Kind: "hls", Key: "k1"})
	if n := f.active(); n != 2 {
		t.Fatalf("limit 2 iken etkin oturum sayısı %d", n)
	}
}

func TestIdleHLSSessionFreesItsSlotAndCanResume(t *testing.T) {
	f := newSessionFixture(t, 1)
	must(f.hls("k1", "1.1.1.1"))
	f.age("k1", time.Minute)
	if n := f.active(); n != 0 {
		t.Fatalf("boşta kalan HLS oturumu sayılmamalı: %d", n)
	}
	// Kanal değiştirme: eski oturum boşta olduğu için kimse yerinden edilmez.
	wantEvicted(t, must(f.hls("k2", "1.1.1.1")))

	// Boşta kalan oturum geri dönerse yeniden kabul edilir ve limit yine uygulanır.
	f.age("k2", time.Second)
	wantEvicted(t, must(f.hls("k1", "1.1.1.1")), store.Evicted{Kind: "hls", Key: "k2"})
	if n := f.active(); n != 1 {
		t.Fatalf("etkin oturum sayısı %d", n)
	}
}

func TestTSSessionLifecycle(t *testing.T) {
	f := newSessionFixture(t, 1)
	wantEvicted(t, must(f.ts("c1")))
	f.age("c1", time.Hour)
	if n := f.active(); n != 1 {
		t.Fatalf(".ts oturumu SRS bitti diyene kadar etkin sayılmalı: %d", n)
	}
	wantEvicted(t, must(f.ts("c2")), store.Evicted{Kind: "ts", Key: "c1"})
	wantEvicted(t, must(f.hls("k1", "1.1.1.1")), store.Evicted{Kind: "ts", Key: "c2"})

	ctx := context.Background()
	for _, id := range []string{"c1", "c2", "bilinmeyen"} {
		if err := f.s.CloseTSSession(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.active(); n != 1 {
		t.Fatalf("yalnızca HLS oturumu kalmalı: %d", n)
	}
}

func TestTenantConnectionQuotaRejectsWithoutEvicting(t *testing.T) {
	f := newSessionFixture(t, 1)
	ctx := context.Background()
	if err := f.s.SetTenantQuotas(ctx, f.tenant, store.Quotas{MaxChannels: 10, MaxViewers: 10, MaxConnections: 1}); err != nil {
		t.Fatal(err)
	}
	other := must(f.s.CreateViewer(ctx, f.tenant, "veli", "pw", 1))
	must(f.hls("k1", "1.1.1.1"))

	if _, err := f.s.TouchHLSSession(ctx, other, f.channel, "k2", "2.2.2.2", idle); !errors.Is(err, store.ErrTenantConnectionLimit) {
		t.Fatalf("yayıncı kotası dolunca yeni izleyici reddedilmeli, gelen: %v", err)
	}
	if _, err := f.s.OpenTSSession(ctx, other, f.channel, "c9", "2.2.2.2", idle); !errors.Is(err, store.ErrTenantConnectionLimit) {
		t.Fatalf(".ts için de reddedilmeli, gelen: %v", err)
	}
	if _, err := f.hls("k1", "1.1.1.1"); err != nil {
		t.Fatalf("süren izleme etkilenmemeli: %v", err)
	}
	// Aynı izleyicinin kanal değiştirmesi kotayı aşmaz: eski oturumu yerinden eder.
	wantEvicted(t, must(f.hls("k3", "1.1.1.1")), store.Evicted{Kind: "hls", Key: "k1"})
}

func TestSessionForUnknownViewer(t *testing.T) {
	f := newSessionFixture(t, 1)
	if _, err := f.s.TouchHLSSession(context.Background(), f.viewer+999, f.channel, "k", "1.1.1.1", idle); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
}

func TestTSSessionsToKick(t *testing.T) {
	f := newSessionFixture(t, 5)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	suspended := must(f.s.CreateViewer(ctx, f.tenant, "askida", "pw", 1))
	expired := must(f.s.CreateViewer(ctx, f.tenant, "dolmus", "pw", 1))
	other := must(f.s.CreateTenant(ctx, "askidaki-yayinci"))
	otherChannel := must(f.s.CreateChannel(ctx, other, "c", "s"))
	orphan := must(f.s.CreateViewer(ctx, other, "yetim", "pw", 1))

	must(f.ts("saglam"))
	must(f.s.OpenTSSession(ctx, suspended, f.channel, "askida-c", "1.1.1.1", idle))
	must(f.s.OpenTSSession(ctx, expired, f.channel, "dolmus-c", "1.1.1.1", idle))
	must(f.s.OpenTSSession(ctx, orphan, otherChannel, "yetim-c", "1.1.1.1", idle))
	limited := must(f.s.CreateViewer(ctx, f.tenant, "limitli", "pw", 1))
	must(f.s.OpenTSSession(ctx, limited, f.channel, "eski-c", "1.1.1.1", idle))
	must(f.s.OpenTSSession(ctx, limited, f.channel, "yeni-c", "1.1.1.1", idle))
	must(f.hls("hls-anahtar", "1.1.1.1"))

	f.s.SetViewerStatus(ctx, suspended, "suspended")
	f.s.SetViewerExpiry(ctx, expired, &past)
	f.s.SetTenantStatus(ctx, other, "suspended")

	got := must(f.s.TSSessionsToKick(ctx))
	want := "[askida-c dolmus-c eski-c yetim-c]"
	if fmt.Sprint(got) != want {
		t.Fatalf("kesilecek bağlantılar %v, beklenen %s", got, want)
	}
}

func TestReconcileTSSessions(t *testing.T) {
	f := newSessionFixture(t, 5)
	ctx := context.Background()
	must(f.ts("yasayan"))
	must(f.ts("olu"))
	must(f.ts("yeni"))
	must(f.hls("hls-anahtar", "1.1.1.1"))
	f.age("yasayan", time.Minute)
	f.age("olu", time.Minute)
	f.age("hls-anahtar", time.Second)

	n := must(f.s.ReconcileTSSessions(ctx, []string{"yasayan"}, 10*time.Second))
	if n != 1 {
		t.Fatalf("yalnızca SRS'te olmayan eski .ts oturumu silinmeli: %d", n)
	}
	if got := f.active(); got != 3 {
		t.Fatalf("kalan etkin oturum %d, beklenen 3 (yasayan, yeni, hls)", got)
	}
	if n := must(f.s.ReconcileTSSessions(ctx, nil, 0)); n != 2 {
		t.Fatalf("boş listeyle 2 .ts oturumu silinmeli: %d", n)
	}
}

func TestDeleteExpiredHLSSessions(t *testing.T) {
	f := newSessionFixture(t, 5)
	ctx := context.Background()
	must(f.hls("taze", "1.1.1.1"))
	must(f.hls("eski", "1.1.1.1"))
	must(f.ts("ts-eski"))
	f.age("eski", 7*time.Hour)
	f.age("ts-eski", 7*time.Hour)

	if n := must(f.s.DeleteExpiredHLSSessions(ctx, 6*time.Hour)); n != 1 {
		t.Fatalf("yalnızca süresi geçmiş HLS oturumu silinmeli: %d", n)
	}
	if got := f.active(); got != 2 {
		t.Fatalf("kalan etkin oturum %d, beklenen 2", got)
	}
}

func TestSuspendedLivePublishers(t *testing.T) {
	f := newSessionFixture(t, 1)
	ctx := context.Background()
	other := must(f.s.CreateTenant(ctx, "t2"))
	theirs := must(f.s.CreateChannel(ctx, other, "c", "s"))
	offline := must(f.s.CreateChannel(ctx, other, "c2", "s2"))
	must(f.s.MarkLive(ctx, f.channel, "saglam-yayinci"))
	must(f.s.MarkLive(ctx, theirs, "askidaki-yayinci"))
	_ = offline

	if got := must(f.s.SuspendedLivePublishers(ctx)); len(got) != 0 {
		t.Fatalf("askıda yayıncı yokken liste boş olmalı: %v", got)
	}
	f.s.SetTenantStatus(ctx, other, "suspended")
	if got := must(f.s.SuspendedLivePublishers(ctx)); fmt.Sprint(got) != "[askidaki-yayinci]" {
		t.Fatalf("beklenmeyen liste: %v", got)
	}
}

func TestChannelAndViewerQuotas(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	if err := s.SetTenantQuotas(ctx, tid, store.Quotas{MaxChannels: 2, MaxViewers: 1, MaxConnections: 5}); err != nil {
		t.Fatal(err)
	}
	must(s.CreateChannel(ctx, tid, "c1", "s1"))
	must(s.CreateChannel(ctx, tid, "c2", "s2"))
	if _, err := s.CreateChannel(ctx, tid, "c3", "s3"); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("kanal kotası aşılamamalı, gelen: %v", err)
	}
	must(s.CreateViewer(ctx, tid, "v1", "pw", 1))
	if _, err := s.CreateViewer(ctx, tid, "v2", "pw", 1); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("izleyici kotası aşılamamalı, gelen: %v", err)
	}
	if n := len(must(s.ChannelsByTenant(ctx, tid))); n != 2 {
		t.Fatalf("kanal sayısı %d", n)
	}

	// Kota başka yayıncıyı etkilemez; olmayan yayıncı ErrNotFound döner.
	other := must(s.CreateTenant(ctx, "t2"))
	must(s.CreateViewer(ctx, other, "v3", "pw", 1))
	if _, err := s.CreateChannel(ctx, tid+999, "c", "s"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
	if err := s.SetTenantQuotas(ctx, tid+999, store.Quotas{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}

	q := must(s.TenantQuotas(ctx, tid))
	if q != (store.Quotas{MaxChannels: 2, MaxViewers: 1, MaxConnections: 5}) {
		t.Fatalf("kotalar %+v", q)
	}
	if d := must(s.TenantQuotas(ctx, other)); d != (store.Quotas{MaxChannels: 10, MaxViewers: 100, MaxConnections: 100}) {
		t.Fatalf("varsayılan kotalar %+v", d)
	}
}
