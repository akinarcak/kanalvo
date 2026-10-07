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

func TestAdminAccounts(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	id := must(s.CreateAdmin(ctx, "Yonetici@Example.com", "ozet-1"))

	a := must(s.AdminByEmail(ctx, "yonetici@example.COM"))
	if a.ID != id || a.Email != "Yonetici@Example.com" || a.PasswordHash != "ozet-1" {
		t.Fatalf("beklenmeyen yönetici: %+v", a)
	}
	if must(s.AdminByID(ctx, id)).Email != a.Email {
		t.Fatal("AdminByID aynı kaydı dönmeli")
	}
	if _, err := s.CreateAdmin(ctx, "YONETICI@example.com", "x"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("aynı e-posta ikinci kez kullanılamamalı, gelen: %v", err)
	}
	if _, err := s.AdminByEmail(ctx, "yok@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
	if err := s.SetAdminPassword(ctx, id, "ozet-2"); err != nil {
		t.Fatal(err)
	}
	if must(s.AdminByID(ctx, id)).PasswordHash != "ozet-2" {
		t.Fatal("şifre özeti güncellenmeli")
	}
}

func TestTenantAccounts(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	must(s.CreateAdmin(ctx, "yonetici@example.com", "x"))
	id := must(s.CreateTenantAccount(ctx, "Kanal A", "a@example.com", "ozet"))

	got := must(s.TenantByEmail(ctx, "A@EXAMPLE.com"))
	if got.ID != id || got.Name != "Kanal A" || got.Email != "a@example.com" || got.PasswordHash != "ozet" || got.Status != "active" {
		t.Fatalf("beklenmeyen yayıncı: %+v", got)
	}
	if got.Quotas != (store.Quotas{MaxChannels: 10, MaxViewers: 100, MaxConnections: 100}) || got.CreatedAt.IsZero() {
		t.Fatalf("kotalar ve tarih dolu olmalı: %+v", got)
	}
	for _, email := range []string{"a@example.com", "A@example.com", "yonetici@example.com"} {
		if _, err := s.CreateTenantAccount(ctx, "B", email, "x"); !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s yeniden kullanılamamalı, gelen: %v", email, err)
		}
	}

	other := must(s.CreateTenantAccount(ctx, "Kanal B", "b@example.com", "x"))
	if err := s.UpdateTenant(ctx, other, store.TenantUpdate{Profile: &store.TenantProfile{Name: "Kanal B2", Email: "a@example.com"}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("başkasının e-postası alınamamalı, gelen: %v", err)
	}
	if err := s.UpdateTenant(ctx, other, store.TenantUpdate{Profile: &store.TenantProfile{Name: "Kanal B2", Email: "b2@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if b := must(s.TenantByID(ctx, other)); b.Name != "Kanal B2" || b.Email != "b2@example.com" {
		t.Fatalf("profil güncellenmeli: %+v", b)
	}
	if err := s.UpdateTenant(ctx, other+999, store.TenantUpdate{Profile: &store.TenantProfile{Name: "x", Email: "x@example.com"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
	if err := s.SetTenantPassword(ctx, id, "yeni-ozet"); err != nil {
		t.Fatal(err)
	}
	if must(s.TenantByID(ctx, id)).PasswordHash != "yeni-ozet" {
		t.Fatal("şifre özeti güncellenmeli")
	}

	// Paneli olmayan yayıncının e-postası boştur ve e-postayla bulunamaz.
	bare := must(s.CreateTenant(ctx, "panelsiz"))
	if b := must(s.TenantByID(ctx, bare)); b.Email != "" || b.PasswordHash != "" {
		t.Fatalf("panelsiz yayıncı: %+v", b)
	}
	if _, err := s.TenantByEmail(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("boş e-posta kimseyle eşleşmemeli, gelen: %v", err)
	}
}

func TestListTenantsAndStats(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	a := must(s.CreateTenantAccount(ctx, "A", "a@example.com", "x"))
	b := must(s.CreateTenantAccount(ctx, "B", "b@example.com", "x"))
	c1 := must(s.CreateChannel(ctx, a, "c1", "s1"))
	must(s.CreateChannel(ctx, a, "c2", "s2"))
	must(s.CreateChannel(ctx, b, "c3", "s3"))
	v := must(s.CreateViewer(ctx, a, "ali", "pw", 2))
	must(s.MarkLive(ctx, c1, "p1"))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), v, c1, "t1", "1.1.1.1", idle))
	must(s.TouchHLSSession(ctx, testdb.LocalEdge(t), v, c1, "h1", "1.1.1.1", idle))

	list := must(s.ListTenants(ctx, idle))
	if len(list) != 2 || list[0].ID != a || list[1].ID != b {
		t.Fatalf("yayıncılar numara sırasıyla gelmeli: %+v", list)
	}
	if got := fmt.Sprint(list[0].Channels, list[0].Viewers, list[0].LiveChannels, list[0].ActiveSessions); got != "2 1 1 2" {
		t.Fatalf("A sayıları %s, beklenen 2 1 1 2", got)
	}
	if got := fmt.Sprint(list[1].Channels, list[1].Viewers, list[1].LiveChannels, list[1].ActiveSessions); got != "1 0 0 0" {
		t.Fatalf("B sayıları %s, beklenen 1 0 0 0", got)
	}
	if list[0].PasswordHash != "" {
		t.Fatal("liste şifre özetini taşımamalı")
	}

	st := must(s.PlatformStats(ctx, idle))
	if st != (store.PlatformStats{Tenants: 2, Channels: 3, LiveChannels: 1, Viewers: 1, ActiveSessions: 2}) {
		t.Fatalf("platform sayıları: %+v", st)
	}
	u := must(s.TenantUsage(ctx, a, idle))
	if u != (store.Usage{Channels: 2, Viewers: 1, Connections: 2}) {
		t.Fatalf("kullanım: %+v", u)
	}
}

func TestPanelSessions(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	admin := must(s.CreateAdmin(ctx, "y@example.com", "x"))
	tenant := must(s.CreateTenantAccount(ctx, "A", "a@example.com", "x"))
	h1, h2, h3 := []byte("ozet-1"), []byte("ozet-2"), []byte("ozet-3")

	if err := s.CreatePanelSession(ctx, h1, "admin", admin, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePanelSession(ctx, h2, "tenant", tenant, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePanelSession(ctx, h3, "tenant", tenant, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := must(s.PanelSessionByHash(ctx, h1)); got != (store.PanelSession{Role: "admin", SubjectID: admin}) {
		t.Fatalf("yönetici oturumu: %+v", got)
	}
	if got := must(s.PanelSessionByHash(ctx, h2)); got != (store.PanelSession{Role: "tenant", SubjectID: tenant}) {
		t.Fatalf("yayıncı oturumu: %+v", got)
	}
	if _, err := s.PanelSessionByHash(ctx, []byte("yok")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}

	// Şifre değişince diğer oturumlar kapanır, şimdiki kalır.
	if err := s.DeletePanelSessionsOf(ctx, "tenant", tenant, h2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PanelSessionByHash(ctx, h3); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("diğer oturum kapanmalıydı")
	}
	must(s.PanelSessionByHash(ctx, h2))
	must(s.PanelSessionByHash(ctx, h1))

	testdb.Exec(t, `UPDATE panel_sessions SET expires_at = now() - interval '1 second' WHERE role = 'tenant'`)
	if _, err := s.PanelSessionByHash(ctx, h2); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("süresi dolan oturum geçersiz olmalı")
	}
	if n := must(s.DeleteExpiredPanelSessions(ctx)); n != 1 {
		t.Fatalf("1 oturum silinmeliydi: %d", n)
	}
	if err := s.DeletePanelSession(ctx, h1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PanelSessionByHash(ctx, h1); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("çıkış yapılan oturum geçersiz olmalı")
	}
}

// manageFixture: iki yayıncı; her birinin bir kategorisi, bir kanalı ve bir izleyicisi vardır.
type manageFixture struct {
	s                *store.Store
	a, b             int64
	catA, catB       int64
	chanA, chanB     int64
	viewerA, viewerB int64
}

func newManageFixture(t *testing.T) *manageFixture {
	s, ctx := testdb.New(t), context.Background()
	f := &manageFixture{s: s}
	f.a = must(s.CreateTenant(ctx, "A"))
	f.b = must(s.CreateTenant(ctx, "B"))
	f.catA = must(s.CreateCategory(ctx, f.a, "Spor"))
	f.catB = must(s.CreateCategory(ctx, f.b, "Haber"))
	f.chanA = must(s.CreateChannel(ctx, f.a, "A1", "sa"))
	f.chanB = must(s.CreateChannel(ctx, f.b, "B1", "sb"))
	f.viewerA = must(s.CreateViewer(ctx, f.a, "ali", "pa", 1))
	f.viewerB = must(s.CreateViewer(ctx, f.b, "veli", "pb", 1))
	return f
}

func notFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%s: ErrNotFound bekleniyordu, gelen: %v", what, err)
	}
}

func TestChannelManagementIsTenantScoped(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s

	if err := s.UpdateChannel(ctx, f.a, f.chanA, "A1 HD", "https://logo.example/a.png", &f.catA); err != nil {
		t.Fatal(err)
	}
	ch := must(s.ChannelOfTenant(ctx, f.a, f.chanA))
	if ch.Name != "A1 HD" || ch.LogoURL != "https://logo.example/a.png" || ch.CategoryID == nil || *ch.CategoryID != f.catA {
		t.Fatalf("kanal güncellenmeli: %+v", ch)
	}
	if err := s.UpdateChannel(ctx, f.a, f.chanA, "A1", "", nil); err != nil {
		t.Fatal(err)
	}
	if must(s.ChannelOfTenant(ctx, f.a, f.chanA)).CategoryID != nil {
		t.Fatal("kategori kaldırılabilmeli")
	}
	if err := s.SetChannelSecret(ctx, f.a, f.chanA, "yeni"); err != nil {
		t.Fatal(err)
	}
	if must(s.ChannelByID(ctx, f.chanA)).StreamSecret != "yeni" {
		t.Fatal("anahtar yenilenmeli")
	}

	// A yayıncısı B'nin kanalına hiçbir yoldan dokunamaz.
	_, err := s.ChannelOfTenant(ctx, f.a, f.chanB)
	notFound(t, "ChannelOfTenant", err)
	notFound(t, "UpdateChannel", s.UpdateChannel(ctx, f.a, f.chanB, "ele geçirildi", "", nil))
	notFound(t, "UpdateChannel yabancı kategori", s.UpdateChannel(ctx, f.a, f.chanA, "A1", "", &f.catB))
	notFound(t, "SetChannelSecret", s.SetChannelSecret(ctx, f.a, f.chanB, "x"))
	notFound(t, "DeleteChannel", s.DeleteChannel(ctx, f.a, f.chanB))
	notFound(t, "RenameCategory", s.RenameCategory(ctx, f.a, f.catB, "x"))
	if b := must(s.ChannelByID(ctx, f.chanB)); b.Name != "B1" || b.StreamSecret != "sb" {
		t.Fatalf("B'nin kanalı değişmemeli: %+v", b)
	}
	if must(s.ChannelOfTenant(ctx, f.a, f.chanA)).Name != "A1" {
		t.Fatal("reddedilen güncelleme kanalı değiştirmemeli")
	}

	if err := s.RenameCategory(ctx, f.a, f.catA, "Spor HD"); err != nil {
		t.Fatal(err)
	}
	if must(s.CategoriesByTenant(ctx, f.a))[0].Name != "Spor HD" {
		t.Fatal("kategori adı değişmeli")
	}

	// Silinen kanalın oturumları da silinir.
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "t1", "1.1.1.1", idle))
	if err := s.DeleteChannel(ctx, f.a, f.chanA); err != nil {
		t.Fatal(err)
	}
	_, err = s.ChannelByID(ctx, f.chanA)
	notFound(t, "silinen kanal", err)
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 0 {
		t.Fatalf("silinen kanalın oturumu kalmamalı: %d", n)
	}
}

func TestViewerManagementIsTenantScoped(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	list, total, err := s.ViewersByTenant(ctx, f.a, "", store.Page{Limit: 10})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != f.viewerA || list[0].Password != "pa" {
		t.Fatalf("yalnızca A'nın izleyicileri listelenmeli: %+v", list)
	}
	if err := s.UpdateViewer(ctx, f.a, f.viewerA, store.ViewerUpdate{Status: ptr("suspended"), SetExpiry: true, ExpiresAt: &exp, MaxConnections: ptr(3)}); err != nil {
		t.Fatal(err)
	}
	v := must(s.ViewerOfTenant(ctx, f.a, f.viewerA))
	if v.Status != "suspended" || v.MaxConnections != 3 || v.ExpiresAt == nil || !v.ExpiresAt.Equal(exp) {
		t.Fatalf("izleyici güncellenmeli: %+v", v)
	}
	if err := s.UpdateViewer(ctx, f.a, f.viewerA, store.ViewerUpdate{Status: ptr("active"), SetExpiry: true, MaxConnections: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	if must(s.ViewerOfTenant(ctx, f.a, f.viewerA)).ExpiresAt != nil {
		t.Fatal("bitiş tarihi kaldırılabilmeli")
	}
	if err := s.SetViewerPassword(ctx, f.a, f.viewerA, "yeni"); err != nil {
		t.Fatal(err)
	}
	if must(s.ViewerByID(ctx, f.viewerA)).Password != "yeni" {
		t.Fatal("şifre yenilenmeli")
	}

	_, err = s.ViewerOfTenant(ctx, f.a, f.viewerB)
	notFound(t, "ViewerOfTenant", err)
	notFound(t, "UpdateViewer", s.UpdateViewer(ctx, f.a, f.viewerB, store.ViewerUpdate{Status: ptr("suspended"), MaxConnections: ptr(9)}))
	notFound(t, "SetViewerPassword", s.SetViewerPassword(ctx, f.a, f.viewerB, "x"))
	notFound(t, "DeleteViewer", s.DeleteViewer(ctx, f.a, f.viewerB))
	if b := must(s.ViewerByID(ctx, f.viewerB)); b.Status != "active" || b.Password != "pb" || b.MaxConnections != 1 {
		t.Fatalf("B'nin izleyicisi değişmemeli: %+v", b)
	}

	if _, err := s.CreateViewer(ctx, f.a, "veli", "x", 1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("başka yayıncıdaki kullanıcı adı da alınamaz, gelen: %v", err)
	}

	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "t1", "1.1.1.1", idle))
	if err := s.DeleteViewer(ctx, f.a, f.viewerA); err != nil {
		t.Fatal(err)
	}
	_, err = s.ViewerByID(ctx, f.viewerA)
	notFound(t, "silinen izleyici", err)
	if n := testdb.Count(t, `SELECT count(*) FROM sessions`); n != 0 {
		t.Fatalf("silinen izleyicinin oturumu kalmamalı: %d", n)
	}
}

func TestActiveSessionsByTenantAndConnections(t *testing.T) {
	f, ctx := newManageFixture(t), context.Background()
	s := f.s
	must(s.MarkLive(ctx, f.chanA, "yayinci-a"))
	second := must(s.CreateViewer(ctx, f.a, "ayse", "p", 1))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerA, f.chanA, "t1", "1.1.1.1", idle))
	must(s.TouchHLSSession(ctx, testdb.LocalEdge(t), second, f.chanA, "h1", "2.2.2.2", idle))
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewerB, f.chanB, "t9", "9.9.9.9", idle))

	list, total, err := s.ActiveSessionsByTenant(ctx, f.a, idle, store.Page{Limit: 10})
	if err != nil || total != 2 || len(list) != 2 {
		t.Fatalf("yalnızca A'nın etkin oturumları: %+v", list)
	}
	got := fmt.Sprintf("%s %s %s %s | %s %s %s", list[0].Viewer, list[0].Channel, list[0].Kind, list[0].IP, list[1].Viewer, list[1].Kind, list[1].IP)
	if got != "ali A1 ts 1.1.1.1 | ayse hls 2.2.2.2" {
		t.Fatalf("oturum listesi: %s", got)
	}
}

// Yayıncı güncellemesi tek işlemdir: bir parçası uygulanamazsa hiçbiri uygulanmaz.
func TestUpdateTenantIsAllOrNothing(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	must(s.CreateAdmin(ctx, "yonetici@example.com", "ozet"))
	must(s.CreateTenantAccount(ctx, "Diğer", "diger@example.com", "ozet"))
	id := must(s.CreateTenantAccount(ctx, "A", "a@example.com", "ozet"))
	if err := s.CreatePanelSession(ctx, []byte("oturum"), "tenant", id, time.Hour); err != nil {
		t.Fatal(err)
	}
	suspended, quotas := "suspended", store.Quotas{MaxChannels: 1, MaxViewers: 2, MaxConnections: 3}

	for name, email := range map[string]string{"yöneticinin e-postası": "Yonetici@example.com", "başka yayıncının e-postası": "DIGER@example.com"} {
		err := s.UpdateTenant(ctx, id, store.TenantUpdate{
			Status: &suspended, Quotas: &quotas, Profile: &store.TenantProfile{Name: "B", Email: email},
		})
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("%s: ErrConflict bekleniyordu, gelen: %v", name, err)
		}
		got := must(s.TenantByID(ctx, id))
		if got.Name != "A" || got.Email != "a@example.com" || got.Status != "active" || got.Quotas.MaxChannels == 1 {
			t.Fatalf("%s: yarım güncelleme: %+v", name, got)
		}
		if _, err := s.PanelSessionByHash(ctx, []byte("oturum")); err != nil {
			t.Fatalf("%s: uygulanmayan askıya alma panel oturumunu kapattı: %v", name, err)
		}
	}

	if err := s.UpdateTenant(ctx, id, store.TenantUpdate{
		Status: &suspended, Quotas: &quotas, Profile: &store.TenantProfile{Name: "B", Email: "b@example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	got := must(s.TenantByID(ctx, id))
	if got.Name != "B" || got.Email != "b@example.com" || got.Status != "suspended" || got.Quotas != quotas {
		t.Fatalf("güncelleme: %+v", got)
	}
	if _, err := s.PanelSessionByHash(ctx, []byte("oturum")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("askıya alınan yayıncının panel oturumu kapanmalıydı: %v", err)
	}

	// Yalnızca verilen alanlar değişir; etkinleştirme oturum silmez.
	active := "active"
	if err := s.UpdateTenant(ctx, id, store.TenantUpdate{Status: &active}); err != nil {
		t.Fatal(err)
	}
	if got := must(s.TenantByID(ctx, id)); got.Name != "B" || got.Status != "active" || got.Quotas != quotas {
		t.Fatalf("kısmi güncelleme: %+v", got)
	}
	if err := s.UpdateTenant(ctx, 999999, store.TenantUpdate{Status: &active}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("olmayan yayıncı için ErrNotFound bekleniyordu: %v", err)
	}
	if err := s.UpdateTenant(ctx, 999999, store.TenantUpdate{Profile: &store.TenantProfile{Name: "x", Email: "x@example.com"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("olmayan yayıncı için ErrNotFound bekleniyordu: %v", err)
	}
}
