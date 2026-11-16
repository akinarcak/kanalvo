package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestChannelByID(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	cid := must(s.CreateChannel(ctx, tid, "Kanal", "sek"))

	ch := must(s.ChannelByID(ctx, cid))
	if ch.TenantID != tid || ch.Name != "Kanal" || ch.StreamSecret != "sek" || ch.Live || ch.TenantStatus != "active" {
		t.Fatalf("beklenmeyen kanal: %+v", ch)
	}
	if _, err := s.ChannelByID(ctx, cid+999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
}

func TestViewerLookupAndUniqueUsername(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	vid := must(s.CreateViewer(ctx, tid, "ali", "pw", 2))

	byName := must(s.ViewerByUsername(ctx, "ali"))
	byID := must(s.ViewerByID(ctx, vid))
	if byName.ID != vid || byID.Username != "ali" || byName.Password != "pw" || byName.MaxConnections != 2 {
		t.Fatalf("beklenmeyen izleyici: %+v / %+v", byName, byID)
	}
	if _, err := s.ViewerByUsername(ctx, "yok"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
	other := must(s.CreateTenant(ctx, "t2"))
	if _, err := s.CreateViewer(ctx, other, "ali", "pw2", 1); err == nil {
		t.Fatal("aynı kullanıcı adı ikinci kez oluşturulabilmemeli")
	}
}

func TestViewerUsable(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	cases := []struct {
		name string
		v    store.Viewer
		want bool
	}{
		{"aktif, süresiz", store.Viewer{Status: "active", TenantStatus: "active"}, true},
		{"aktif, süresi ileride", store.Viewer{Status: "active", TenantStatus: "active", ExpiresAt: &future}, true},
		{"süresi dolmuş", store.Viewer{Status: "active", TenantStatus: "active", ExpiresAt: &past}, false},
		{"süresi tam şimdi", store.Viewer{Status: "active", TenantStatus: "active", ExpiresAt: &now}, false},
		{"askıda izleyici", store.Viewer{Status: "suspended", TenantStatus: "active"}, false},
		{"askıda yayıncı", store.Viewer{Status: "active", TenantStatus: "suspended"}, false},
	}
	for _, c := range cases {
		if got := c.v.Usable(now); got != c.want {
			t.Errorf("%s: %v, beklenen %v", c.name, got, c.want)
		}
	}
}

func TestMarkLiveIsExclusive(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	cid := must(s.CreateChannel(ctx, tid, "c", "sek"))

	if !must(s.MarkLive(ctx, cid, "a")) {
		t.Fatal("ilk yayıncı kabul edilmeliydi")
	}
	if must(s.MarkLive(ctx, cid, "b")) {
		t.Fatal("ikinci yayıncı reddedilmeliydi")
	}
	if err := s.MarkOffline(ctx, cid, "b"); err != nil {
		t.Fatal(err)
	}
	if !must(s.ChannelByID(ctx, cid)).Live {
		t.Fatal("başka bağlantının bildirimi kanalı çevrimdışı yapmamalı")
	}
	if err := s.MarkOffline(ctx, cid, "a"); err != nil {
		t.Fatal(err)
	}
	if must(s.ChannelByID(ctx, cid)).Live {
		t.Fatal("yayıncının kendi bildirimi kanalı çevrimdışı yapmalı")
	}
}

// Kanal kotası yayın kabulünde eşzamanlı canlı kanal üst sınırıdır (tasarım §7).
func TestMarkLiveEnforcesConcurrentChannelQuota(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	a := must(s.CreateChannel(ctx, tid, "a", "s1"))
	b := must(s.CreateChannel(ctx, tid, "b", "s2"))
	c := must(s.CreateChannel(ctx, tid, "c", "s3"))
	// Başka yayıncının canlı kanalları bu yayıncının kotasından düşmez.
	other := must(s.CreateTenant(ctx, "başka"))
	must(s.MarkLive(ctx, must(s.CreateChannel(ctx, other, "x", "sx")), "x"))
	must(s.MarkLive(ctx, must(s.CreateChannel(ctx, other, "y", "sy")), "y"))
	setQuota := func(n int) {
		t.Helper()
		if err := s.SetTenantQuotas(ctx, tid, store.Quotas{MaxChannels: n, MaxViewers: 10, MaxConnections: 10}); err != nil {
			t.Fatal(err)
		}
	}
	live := func(id int64) bool { return must(s.ChannelByID(ctx, id)).Live }
	offline := func(id int64, client string) {
		t.Helper()
		if err := s.MarkOffline(ctx, id, client); err != nil {
			t.Fatal(err)
		}
	}
	setQuota(3) // 3 kanal oluşturulabilsin; yayın sınırı aşağıda 2'ye düşer
	setQuota(2)

	if !must(s.MarkLive(ctx, a, "ca")) || !must(s.MarkLive(ctx, b, "cb")) {
		t.Fatal("kota içindeki iki yayın kabul edilmeliydi")
	}
	if ok, err := s.MarkLive(ctx, c, "cc"); ok || !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("üçüncü yayın kotayla reddedilmeliydi: %v, %v", ok, err)
	}
	if live(c) {
		t.Fatal("reddedilen kanal çevrimdışı kalmalı")
	}
	// Zaten canlı kanala ikinci bağlantı kota hatası değil, tekil yayın reddidir.
	if ok, err := s.MarkLive(ctx, a, "ca2"); ok || err != nil {
		t.Fatalf("canlı kanala ikinci yayın false, nil dönmeli: %v, %v", ok, err)
	}

	offline(a, "ca")
	if !must(s.MarkLive(ctx, c, "cc")) {
		t.Fatal("bir yayın kapanınca üçüncü kanal açılabilmeli")
	}

	// Kota düşürülünce süren yayınlar sürer, yenisi reddedilir.
	setQuota(1)
	if !live(b) || !live(c) {
		t.Fatal("kota düşürülünce süren yayınlara dokunulmamalı")
	}
	if _, err := s.MarkLive(ctx, a, "ca3"); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("kota düşürülünce yeni yayın reddedilmeli: %v", err)
	}
	// İki yayın da kapanınca yalnızca biri açılabilir.
	offline(b, "cb")
	if _, err := s.MarkLive(ctx, a, "ca4"); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("canlı sayısı hâlâ kotaya eşitken yeni yayın reddedilmeli: %v", err)
	}
	offline(c, "cc")
	if !must(s.MarkLive(ctx, a, "ca5")) {
		t.Fatal("kota boşalınca yayın açılabilmeli")
	}

	// Kanal oluşturmadaki kuralla aynı: 0 hiç yayına izin vermez.
	offline(a, "ca5")
	setQuota(0)
	if _, err := s.MarkLive(ctx, a, "ca6"); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("0 kotada yayın reddedilmeli: %v", err)
	}
	if _, err := s.CreateChannel(ctx, tid, "d", "s4"); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("0 kotada kanal da oluşturulamamalı: %v", err)
	}
}

func TestConcurrentMarkLiveCannotExceedChannelQuota(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	const quota, attempts = 2, 8
	if err := s.SetTenantQuotas(ctx, tid, store.Quotas{MaxChannels: attempts, MaxViewers: 10, MaxConnections: 10}); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := range attempts {
		ids = append(ids, must(s.CreateChannel(ctx, tid, fmt.Sprint("k", i), fmt.Sprint("s", i))))
	}
	if err := s.SetTenantQuotas(ctx, tid, store.Quotas{MaxChannels: quota, MaxViewers: 10, MaxConnections: 10}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan error, attempts)
	start := make(chan struct{})
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := s.MarkLive(ctx, id, fmt.Sprint("c", i))
			if err == nil && !ok {
				err = errors.New("beklenmeyen false")
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case !errors.Is(err, store.ErrQuotaExceeded):
			t.Fatalf("beklenmeyen hata: %v", err)
		}
	}
	if accepted != quota {
		t.Fatalf("eşzamanlı %d denemeden %d kabul edildi, beklenen %d", attempts, accepted, quota)
	}
	var live int
	for _, id := range ids {
		if must(s.ChannelByID(ctx, id)).Live {
			live++
		}
	}
	if live != quota {
		t.Fatalf("canlı kanal sayısı %d, beklenen %d", live, quota)
	}
}

func TestReconcileLive(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	stale := must(s.CreateChannel(ctx, tid, "eski", "s1"))
	active := must(s.CreateChannel(ctx, tid, "süren", "s2"))
	must(s.MarkLive(ctx, stale, "a"))
	must(s.MarkLive(ctx, active, "b"))

	if n := must(s.ReconcileLive(ctx, []int64{active}, time.Hour)); n != 0 {
		t.Fatalf("bekleme süresi içindeki kanala dokunulmamalı, temizlenen: %d", n)
	}
	if n := must(s.ReconcileLive(ctx, []int64{active}, 0)); n != 1 {
		t.Fatalf("1 kanal temizlenmeliydi, temizlenen: %d", n)
	}
	if must(s.ChannelByID(ctx, stale)).Live || !must(s.ChannelByID(ctx, active)).Live {
		t.Fatal("yalnızca SRS'te olmayan kanal çevrimdışı olmalı")
	}
	if !must(s.MarkLive(ctx, stale, "c")) {
		t.Fatal("temizlenen kanala yeniden yayın açılabilmeli")
	}
	if n := must(s.ReconcileLive(ctx, nil, 0)); n != 2 {
		t.Fatalf("boş listeyle 2 kanal temizlenmeliydi, temizlenen: %d", n)
	}
}

func TestStatusSetters(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	vid := must(s.CreateViewer(ctx, tid, "ali", "pw", 1))
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := s.SetViewerStatus(ctx, vid, "suspended"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewerExpiry(ctx, vid, &at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTenantStatus(ctx, tid, "suspended"); err != nil {
		t.Fatal(err)
	}
	v := must(s.ViewerByID(ctx, vid))
	if v.Status != "suspended" || v.TenantStatus != "suspended" || v.ExpiresAt == nil || !v.ExpiresAt.Equal(at) {
		t.Fatalf("beklenmeyen izleyici: %+v", v)
	}
}
