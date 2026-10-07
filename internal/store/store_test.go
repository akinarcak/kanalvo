package store_test

import (
	"context"
	"errors"
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
