package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

func enrolling(name string) store.NewEdge {
	return store.NewEdge{Name: name, BaseURL: "http://203.0.113.20", ControlURL: "http://203.0.113.20", Key: "kurulum-" + name, PullIP: "203.0.113.20", Weight: 100}
}

func TestEnrollmentCodeRegistersADisabledServerOnce(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	id := must(s.CreateEdgeEnrollment(ctx, "kod-1", 30*time.Minute))

	if open := must(s.EdgeEnrollmentOpen(ctx, "kod-1")); !open {
		t.Fatal("yeni kod açık olmalı")
	}
	if open := must(s.EdgeEnrollmentOpen(ctx, "baska-kod")); open {
		t.Fatal("bilinmeyen kod açık sayıldı")
	}
	if n := testdb.Count(t, `SELECT count(*) FROM edge_enrollments WHERE token_hash = 'kod-1'`); n != 0 {
		t.Fatal("kodun kendisi veritabanına yazılmamalı")
	}
	if e := must(s.EdgeEnrollmentByID(ctx, id)); e.UsedAt != nil || e.EdgeID != nil || e.Expired {
		t.Fatalf("kullanılmamış kod: %+v", e)
	}

	edgeID := must(s.RedeemEdgeEnrollment(ctx, "kod-1", enrolling("Sunucu 203.0.113.20")))
	edge := must(s.EdgeByID(ctx, edgeID, healthWindow))
	if edge.Enabled || edge.Name != "Sunucu 203.0.113.20" || edge.PullIP != "203.0.113.20" || edge.TSBaseURL != "http://203.0.113.20" || edge.HLSBaseURL != edge.TSBaseURL {
		t.Fatalf("kodla kaydolan sunucu onaylanana kadar devre dışı olmalı: %+v", edge)
	}
	if e := must(s.EdgeEnrollmentByID(ctx, id)); e.UsedAt == nil || e.EdgeID == nil || *e.EdgeID != edgeID || e.Expired {
		t.Fatalf("kullanılmış kod: %+v", e)
	}

	// Kod tek kullanımlıktır.
	if open := must(s.EdgeEnrollmentOpen(ctx, "kod-1")); open {
		t.Fatal("kullanılmış kod açık sayıldı")
	}
	if _, err := s.RedeemEdgeEnrollment(ctx, "kod-1", enrolling("ikinci")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("kullanılmış kod yeniden geçti: %v", err)
	}
	if n := len(must(s.Edges(ctx, healthWindow))); n != 2 {
		t.Fatalf("yerel ve yeni sunucu bekleniyordu: %d", n)
	}
}

func TestExpiredEnrollmentCodeIsRefused(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	id := must(s.CreateEdgeEnrollment(ctx, "kod-1", 30*time.Minute))
	testdb.Exec(t, `UPDATE edge_enrollments SET expires_at = now() - interval '1 second'`)

	if open := must(s.EdgeEnrollmentOpen(ctx, "kod-1")); open {
		t.Fatal("süresi dolmuş kod açık sayıldı")
	}
	if _, err := s.RedeemEdgeEnrollment(ctx, "kod-1", enrolling("gec")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("süresi dolmuş kod geçti: %v", err)
	}
	if e := must(s.EdgeEnrollmentByID(ctx, id)); !e.Expired {
		t.Fatalf("kod süresi dolmuş görünmeli: %+v", e)
	}
	if n := len(must(s.Edges(ctx, healthWindow))); n != 1 {
		t.Fatalf("sunucu kaydedilmemeliydi: %d", n)
	}
}

// Aynı adresten ikinci kez kurulum yapılırsa ad çakışması kaydı engellemez.
func TestEnrollmentPicksAFreeName(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	newRemoteEdge(t, s, "Sunucu 203.0.113.20")
	must(s.CreateEdgeEnrollment(ctx, "kod-1", 30*time.Minute))

	edgeID := must(s.RedeemEdgeEnrollment(ctx, "kod-1", enrolling("Sunucu 203.0.113.20")))
	if name := must(s.EdgeByID(ctx, edgeID, healthWindow)).Name; name == "Sunucu 203.0.113.20" || name == "" {
		t.Fatalf("çakışan ad değiştirilmeliydi: %q", name)
	}
}

// Eski kodlar yeni kod alınırken temizlenir; süren ve yeni dolmuş kodlar kalır.
func TestOldEnrollmentCodesAreCleanedUp(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	must(s.CreateEdgeEnrollment(ctx, "eski", 30*time.Minute))
	testdb.Exec(t, `UPDATE edge_enrollments SET expires_at = now() - interval '2 days'`)
	must(s.CreateEdgeEnrollment(ctx, "dolmus", 30*time.Minute))
	testdb.Exec(t, `UPDATE edge_enrollments SET expires_at = now() - interval '1 hour' WHERE expires_at > now()`)

	must(s.CreateEdgeEnrollment(ctx, "yeni", 30*time.Minute))
	if n := testdb.Count(t, `SELECT count(*) FROM edge_enrollments`); n != 2 {
		t.Fatalf("yalnızca çok eski kod silinmeliydi, kalan: %d", n)
	}
}
