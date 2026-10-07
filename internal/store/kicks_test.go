package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

func pending(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	return fmt.Sprintf("origin=%v ts=%v", must(s.PendingOriginKicks(ctx)), must(s.PendingEdgeKicks(ctx, testdb.LocalEdge(t))))
}

// Silinen kanalın yayıncısı ve .ts izleyicileri, silme ile aynı işlemde kesilmek üzere kaydedilir.
func TestDeletingAChannelQueuesItsConnections(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	must(s.MarkLive(ctx, f.chanA, "yayinci-a"))
	must(s.MarkLive(ctx, f.chanB, "yayinci-b"))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "izleyen-a", "1.1.1.1", idle))
	must(s.TouchHLSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "hls-a", "1.1.1.1", idle))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerB, f.chanB, "izleyen-b", "1.1.1.1", idle))

	notFound(t, "başka yayıncının kanalı", s.DeleteChannel(ctx, f.a, f.chanB))
	if got := pending(t, s); got != "origin=[] ts=[]" {
		t.Fatalf("reddedilen silme hiçbir bağlantıyı kuyruğa almamalı: %s", got)
	}

	if err := s.DeleteChannel(ctx, f.a, f.chanA); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); got != "origin=[yayinci-a] ts=[izleyen-a]" {
		t.Fatalf("kuyruk: %s", got)
	}
}

func TestDeletingAnOfflineChannelQueuesNoPublisher(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	if err := f.s.DeleteChannel(ctx, f.a, f.chanA); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, f.s); got != "origin=[] ts=[]" {
		t.Fatalf("kuyruk boş olmalı: %s", got)
	}
}

func TestDeletingAViewerQueuesItsTSConnections(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	if err := s.SetTenantQuotas(ctx, f.a, store.Quotas{MaxChannels: 10, MaxViewers: 10, MaxConnections: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateViewer(ctx, f.a, f.viewerA, store.ViewerUpdate{MaxConnections: ptr(3)}); err != nil {
		t.Fatal(err)
	}
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "izleyen-1", "1.1.1.1", idle))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "izleyen-2", "1.1.1.1", idle))
	must(s.TouchHLSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "hls-1", "1.1.1.1", idle))

	notFound(t, "başka yayıncının izleyicisi", s.DeleteViewer(ctx, f.a, f.viewerB))
	if err := s.DeleteViewer(ctx, f.a, f.viewerA); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); got != "origin=[] ts=[izleyen-1 izleyen-2]" {
		t.Fatalf("kuyruk: %s", got)
	}
}

func TestRegeneratingAStreamSecretQueuesTheRunningPublisher(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	if err := s.SetChannelSecret(ctx, f.a, f.chanA, "yeni-1"); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); got != "origin=[] ts=[]" {
		t.Fatalf("yayında olmayan kanal için kuyruk boş olmalı: %s", got)
	}
	must(s.MarkLive(ctx, f.chanA, "yayinci-a"))
	if err := s.SetChannelSecret(ctx, f.a, f.chanA, "yeni-2"); err != nil {
		t.Fatal(err)
	}
	// Aynı bağlantı iki kez kuyruğa girmez.
	if err := s.SetChannelSecret(ctx, f.a, f.chanA, "yeni-3"); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); got != "origin=[yayinci-a] ts=[]" {
		t.Fatalf("kuyruk: %s", got)
	}
	notFound(t, "başka yayıncının kanalı", s.SetChannelSecret(ctx, f.a, f.chanB, "x"))
}

// Şifresi yenilenen izleyicinin süren tüm izlemeleri sonlandırılır: sızan şifreyle izleyen de düşer.
func TestRegeneratingAViewerPasswordRevokesItsSessions(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	if err := s.UpdateViewer(ctx, f.a, f.viewerA, store.ViewerUpdate{MaxConnections: ptr(2)}); err != nil {
		t.Fatal(err)
	}
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "izleyen-1", "1.1.1.1", idle))
	must(s.TouchHLSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "hls-1", "1.1.1.1", idle))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerB, f.chanB, "izleyen-b", "1.1.1.1", idle))

	if err := s.SetViewerPassword(ctx, f.a, f.viewerA, "yeni"); err != nil {
		t.Fatal(err)
	}
	if ids := must(s.TSSessionsToKick(ctx, testdb.LocalEdge(t))); fmt.Sprint(ids) != "[izleyen-1]" {
		t.Fatalf("yalnızca o izleyicinin .ts bağlantısı kesilmeli: %v", ids)
	}
	if _, err := s.TouchHLSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "hls-1", "1.1.1.1", idle); !errors.Is(err, store.ErrSessionRevoked) {
		t.Fatalf("eski HLS oturumu reddedilmeli, gelen: %v", err)
	}
	if n := must(s.ActiveSessionCount(ctx, f.viewerB, idle)); n != 1 {
		t.Fatalf("başka izleyici etkilenmemeli: %d", n)
	}
	notFound(t, "başka yayıncının izleyicisi", s.SetViewerPassword(ctx, f.a, f.viewerB, "x"))
}

func TestResolvingAndExpiringPendingKicks(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	must(s.MarkLive(ctx, f.chanA, "yayinci-a"))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "izleyen-a", "1.1.1.1", idle))
	if err := s.DeleteChannel(ctx, f.a, f.chanA); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveOriginKick(ctx, "yayinci-a"); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); got != "origin=[] ts=[izleyen-a]" {
		t.Fatalf("kuyruk: %s", got)
	}
	if n := must(s.DeleteStaleKicks(ctx, time.Hour)); n != 0 {
		t.Fatalf("yeni kayıt silinmemeli: %d", n)
	}
	testdb.Exec(t, `UPDATE pending_kicks SET created_at = now() - interval '2 hours'`)
	if n := must(s.DeleteStaleKicks(ctx, time.Hour)); n != 1 {
		t.Fatalf("eskiyen kayıt silinmeli: %d", n)
	}
}

func TestCreateWithDetailsIsASingleStep(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	exp := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)

	id := must(s.CreateChannelWith(ctx, f.a, store.NewChannel{Name: "A2", Secret: "s", LogoURL: "https://l.example/a.png", CategoryID: &f.catA}))
	ch := must(s.ChannelOfTenant(ctx, f.a, id))
	if ch.Name != "A2" || ch.LogoURL != "https://l.example/a.png" || ch.CategoryID == nil || *ch.CategoryID != f.catA {
		t.Fatalf("kanal: %+v", ch)
	}
	before := len(must(s.ChannelsByTenant(ctx, f.a)))
	if _, err := s.CreateChannelWith(ctx, f.a, store.NewChannel{Name: "A3", Secret: "s", CategoryID: &f.catB}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("başka yayıncının kategorisiyle kanal oluşturulamamalı, gelen: %v", err)
	}
	if after := len(must(s.ChannelsByTenant(ctx, f.a))); after != before {
		t.Fatal("reddedilen oluşturma yarım kanal bırakmamalı")
	}

	vid := must(s.CreateViewerWith(ctx, f.a, store.NewViewer{Username: "sureli", Password: "p", MaxConnections: 2, ExpiresAt: &exp}))
	v := must(s.ViewerOfTenant(ctx, f.a, vid))
	if v.MaxConnections != 2 || v.ExpiresAt == nil || !v.ExpiresAt.Equal(exp) || v.Status != "active" {
		t.Fatalf("izleyici: %+v", v)
	}
}

func TestViewerUpdateChangesOnlyGivenFields(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	exp := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := s.UpdateViewer(ctx, f.a, f.viewerA, store.ViewerUpdate{Status: ptr("suspended"), SetExpiry: true, ExpiresAt: &exp, MaxConnections: ptr(4)}); err != nil {
		t.Fatal(err)
	}
	// Yalnızca limit değişir; askı durumu ve bitiş tarihi korunur.
	if err := s.UpdateViewer(ctx, f.a, f.viewerA, store.ViewerUpdate{MaxConnections: ptr(2)}); err != nil {
		t.Fatal(err)
	}
	v := must(s.ViewerOfTenant(ctx, f.a, f.viewerA))
	if v.Status != "suspended" || v.MaxConnections != 2 || v.ExpiresAt == nil || !v.ExpiresAt.Equal(exp) {
		t.Fatalf("verilmeyen alanlar değişmemeli: %+v", v)
	}
	if err := s.UpdateViewer(ctx, f.a, f.viewerA, store.ViewerUpdate{SetExpiry: true}); err != nil {
		t.Fatal(err)
	}
	if v := must(s.ViewerOfTenant(ctx, f.a, f.viewerA)); v.ExpiresAt != nil || v.Status != "suspended" {
		t.Fatalf("bitiş tarihi kaldırılmalı, durum korunmalı: %+v", v)
	}
	notFound(t, "başka yayıncının izleyicisi", s.UpdateViewer(ctx, f.a, f.viewerB, store.ViewerUpdate{Status: ptr("suspended")}))
}

func ptr[T any](v T) *T { return &v }
