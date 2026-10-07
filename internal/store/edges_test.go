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

const healthWindow = 15 * time.Second

func newRemoteEdge(t *testing.T, s *store.Store, name string) int64 {
	t.Helper()
	return must(s.CreateEdge(context.Background(), store.NewEdge{
		Name: name, BaseURL: "http://" + name + ".example", ControlURL: "http://" + name + ".internal",
		Key: "key-" + name, PullIP: "10.0.0.9", Weight: 50,
	}))
}

func TestLocalEdgeExistsAndTakesItsAddressesFromSettings(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()

	id := must(s.SyncLocalEdge(ctx, "http://ts.example", "http://hls.example"))
	if id != testdb.LocalEdge(t) {
		t.Fatalf("yerel edge numarası %d", id)
	}
	list := must(s.Edges(ctx, healthWindow))
	if len(list) != 1 {
		t.Fatalf("yalnızca yerel edge bekleniyordu: %+v", list)
	}
	e := list[0]
	if !e.Builtin || !e.Enabled || e.TSBaseURL != "http://ts.example" || e.HLSBaseURL != "http://hls.example" || e.Weight != 100 {
		t.Fatalf("yerel edge: %+v", e)
	}
}

func TestEdgeIsHealthyOnlyWhileItsSignalIsFresh(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	id := newRemoteEdge(t, s, "e1")

	if e := must(s.EdgeByID(ctx, id, healthWindow)); e.Healthy || e.LastSeenAt != nil {
		t.Fatalf("hiç sinyal vermemiş edge sağlıklı sayıldı: %+v", e)
	}
	if err := s.MarkEdgeSeen(ctx, id); err != nil {
		t.Fatal(err)
	}
	if e := must(s.EdgeByID(ctx, id, healthWindow)); !e.Healthy {
		t.Fatalf("sinyal vermiş edge sağlıksız: %+v", e)
	}
	testdb.Exec(t, `UPDATE edges SET last_seen_at = now() - interval '16 seconds' WHERE id = $1`, id)
	if e := must(s.EdgeByID(ctx, id, healthWindow)); e.Healthy {
		t.Fatalf("sinyali eskimiş edge sağlıklı sayıldı: %+v", e)
	}
}

func TestCreateUpdateAndDeleteRemoteEdge(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	id := newRemoteEdge(t, s, "e1")

	e := must(s.EdgeByID(ctx, id, healthWindow))
	got := fmt.Sprintf("%s %v %s %s %s %s %s %d %v", e.Name, e.Builtin, e.TSBaseURL, e.HLSBaseURL, e.ControlURL, e.Key, e.PullIP, e.Weight, e.Enabled)
	if got != "e1 false http://e1.example http://e1.example http://e1.internal key-e1 10.0.0.9 50 true" {
		t.Fatalf("oluşturulan edge: %s", got)
	}
	if _, err := s.CreateEdge(ctx, store.NewEdge{Name: "e1", BaseURL: "http://x", Key: "k2", Weight: 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("aynı adla ikinci edge: %v", err)
	}

	// Yalnızca verilen alanlar değişir.
	if err := s.UpdateEdge(ctx, id, store.EdgeUpdate{Weight: ptr(300), Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	e = must(s.EdgeByID(ctx, id, healthWindow))
	if e.Weight != 300 || e.Enabled || e.Name != "e1" || e.TSBaseURL != "http://e1.example" || e.PullIP != "10.0.0.9" {
		t.Fatalf("kısmi güncelleme: %+v", e)
	}
	if err := s.UpdateEdge(ctx, id, store.EdgeUpdate{BaseURL: ptr("http://yeni.example"), PullIP: ptr("10.0.0.7")}); err != nil {
		t.Fatal(err)
	}
	e = must(s.EdgeByID(ctx, id, healthWindow))
	if e.TSBaseURL != "http://yeni.example" || e.HLSBaseURL != "http://yeni.example" || e.PullIP != "10.0.0.7" || e.Weight != 300 {
		t.Fatalf("adres güncellemesi: %+v", e)
	}

	if err := s.DeleteEdge(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EdgeByID(ctx, id, healthWindow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("silinen edge hâlâ duruyor: %v", err)
	}
	notFound(t, "olmayan edge'i silmek", s.DeleteEdge(ctx, id))
	notFound(t, "olmayan edge'i güncellemek", s.UpdateEdge(ctx, id, store.EdgeUpdate{Weight: ptr(1)}))
}

func TestLocalEdgeCannotBeDeletedOrReaddressed(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	local := must(s.SyncLocalEdge(ctx, "http://ts.example", "http://hls.example"))

	if err := s.DeleteEdge(ctx, local); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("yerel edge silinebildi: %v", err)
	}
	if err := s.UpdateEdge(ctx, local, store.EdgeUpdate{BaseURL: ptr("http://kotu.example"), Weight: ptr(7), Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	e := must(s.EdgeByID(ctx, local, healthWindow))
	if e.TSBaseURL != "http://ts.example" || e.HLSBaseURL != "http://hls.example" {
		t.Fatalf("yerel edge'in adresi panelden değişti: %+v", e)
	}
	if e.Weight != 7 || e.Enabled {
		t.Fatalf("yerel edge'in ağırlığı ve durumu değişmeliydi: %+v", e)
	}
}

// Aynı SRS bağlantı kimliği iki edge'de birbirinden bağımsız iki oturumdur.
func TestTSSessionsAreSeparatePerEdge(t *testing.T) {
	f, ctx := newSessionFixture(t, 5), context.Background()
	s := f.s
	local, remote := testdb.LocalEdge(t), newRemoteEdge(t, s, "e1")

	must(s.OpenTSSession(ctx, local, f.viewer, f.channel, "ayni", "1.1.1.1", idle))
	must(s.OpenTSSession(ctx, remote, f.viewer, f.channel, "ayni", "2.2.2.2", idle))
	must(s.OpenTSSession(ctx, remote, f.viewer, f.channel, "uzak-2", "2.2.2.2", idle))
	if n := f.active(); n != 3 {
		t.Fatalf("üç ayrı oturum bekleniyordu: %d", n)
	}
	counts := must(s.EdgeSessionCounts(ctx, idle))
	if counts[local] != 1 || counts[remote] != 2 {
		t.Fatalf("edge başına oturum sayısı: %v", counts)
	}

	// Yerel edge'in eşitlemesi uzak edge'in oturumlarını silmez.
	testdb.Exec(t, `UPDATE sessions SET started_at = now() - interval '1 minute'`)
	if n := must(s.ReconcileTSSessions(ctx, local, nil, 10*time.Second)); n != 1 {
		t.Fatalf("yalnızca yerel oturum silinmeliydi: %d", n)
	}
	if n := f.active(); n != 2 {
		t.Fatalf("uzak edge'in oturumları kalmalıydı: %d", n)
	}

	// Yerel edge'den gelen "izleme bitti" uzak edge'deki aynı kimlikli oturumu kapatmaz.
	if err := s.CloseTSSession(ctx, local, "ayni"); err != nil {
		t.Fatal(err)
	}
	if n := f.active(); n != 2 {
		t.Fatalf("başka edge'in bildirimi oturumu kapattı: %d", n)
	}
	if err := s.CloseTSSession(ctx, remote, "ayni"); err != nil {
		t.Fatal(err)
	}
	if n := f.active(); n != 1 {
		t.Fatalf("kendi edge'inin bildirimi oturumu kapatmalıydı: %d", n)
	}
}

func TestKicksAreListedPerEdge(t *testing.T) {
	f, ctx := newSessionFixture(t, 1), context.Background()
	s := f.s
	local, remote := testdb.LocalEdge(t), newRemoteEdge(t, s, "e1")

	// Limit 1: uzak edge'deki ikinci izleme, yerel edge'deki ilkini yerinden eder.
	must(s.OpenTSSession(ctx, local, f.viewer, f.channel, "yerel-c", "1.1.1.1", idle))
	evicted := must(s.OpenTSSession(ctx, remote, f.viewer, f.channel, "uzak-c", "2.2.2.2", idle))
	wantEvicted(t, evicted, store.Evicted{Kind: "ts", Key: "yerel-c", EdgeID: local})

	if got := fmt.Sprint(must(s.TSSessionsToKick(ctx, local)), must(s.TSSessionsToKick(ctx, remote))); got != "[yerel-c] []" {
		t.Fatalf("kesilecek bağlantılar edge'e göre ayrılmalı: %s", got)
	}

	// İzleyici silinince bekleyen kesmeler de edge'e göre ayrılır.
	if err := s.DeleteViewer(ctx, f.tenant, f.viewer); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(must(s.PendingEdgeKicks(ctx, local)), must(s.PendingEdgeKicks(ctx, remote))); got != "[yerel-c] [uzak-c]" {
		t.Fatalf("bekleyen kesmeler: %s", got)
	}
	if err := s.ResolveEdgeKick(ctx, local, "uzak-c"); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(must(s.PendingEdgeKicks(ctx, remote))); got != "[uzak-c]" {
		t.Fatalf("başka edge adına çözülen kesme kuyruktan çıktı: %s", got)
	}
	if err := s.ResolveEdgeKick(ctx, remote, "uzak-c"); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(must(s.PendingEdgeKicks(ctx, remote))); got != "[]" {
		t.Fatalf("çözülen kesme kuyrukta kaldı: %s", got)
	}
}

func TestDeletingAnEdgeRemovesItsSessionsAndKicks(t *testing.T) {
	f, ctx := newSessionFixture(t, 5), context.Background()
	s := f.s
	remote := newRemoteEdge(t, s, "e1")
	must(s.OpenTSSession(ctx, remote, f.viewer, f.channel, "uzak-c", "2.2.2.2", idle))
	must(s.TouchHLSSession(ctx, remote, f.viewer, f.channel, "hls-k", "2.2.2.2", idle))
	testdb.Exec(t, `INSERT INTO pending_kicks (target, edge_id, client_id) VALUES ('ts', $1, 'bekleyen')`, remote)

	if err := s.DeleteEdge(ctx, remote); err != nil {
		t.Fatal(err)
	}
	if n := f.active(); n != 0 {
		t.Fatalf("silinen edge'in oturumları kaldı: %d", n)
	}
	if n := testdb.Count(t, `SELECT count(*) FROM pending_kicks`); n != 0 {
		t.Fatalf("silinen edge'in bekleyen kesmeleri kaldı: %d", n)
	}
}

func TestSessionListNamesTheEdge(t *testing.T) {
	f, ctx := newSessionFixture(t, 5), context.Background()
	s := f.s
	remote := newRemoteEdge(t, s, "e1")
	must(s.OpenTSSession(ctx, testdb.LocalEdge(t), f.viewer, f.channel, "c1", "1.1.1.1", idle))
	must(s.TouchHLSSession(ctx, remote, f.viewer, f.channel, "k1", "2.2.2.2", idle))

	list, _, err := s.ActiveSessionsByTenant(ctx, f.tenant, idle, store.Page{Limit: 10})
	if err != nil || len(list) != 2 || list[0].Edge != "Yerel" || list[1].Edge != "e1" {
		t.Fatalf("oturum listesindeki sunucu adları: %+v", list)
	}
}
