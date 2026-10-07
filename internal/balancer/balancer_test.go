package balancer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"kanalvo/internal/balancer"
	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

const window = 15 * time.Second

func edge(name string, weight int, mutate ...func(*store.Edge)) store.Edge {
	e := store.Edge{Name: name, Weight: weight, Enabled: true, Healthy: true,
		TSBaseURL: "http://" + name, HLSBaseURL: "http://" + name}
	for _, m := range mutate {
		m(&e)
	}
	return e
}

func unhealthy(e *store.Edge) { e.Healthy = false }
func disabled(e *store.Edge)  { e.Enabled = false }
func builtin(e *store.Edge)   { e.Builtin = true }

// spread, her olası rastgele değer için seçilen edge'i sayar.
func spread(edges []store.Edge, kind string) map[string]int {
	total := 0
	for _, e := range edges {
		total += e.Weight
	}
	counts := map[string]int{}
	for r := 0; r < total; r++ {
		e, ok := balancer.Choose(edges, kind, func(n int) int { return r % n })
		if ok {
			counts[e.Name]++
		}
	}
	return counts
}

func TestChooseDistributesByWeight(t *testing.T) {
	counts := spread([]store.Edge{edge("a", 100), edge("b", 300)}, balancer.TS)
	if counts["a"] != 100 || counts["b"] != 300 {
		t.Fatalf("ağırlığa göre dağılım: %v", counts)
	}
}

func TestChooseSkipsUnhealthyAndDisabledEdges(t *testing.T) {
	edges := []store.Edge{edge("saglikli", 1), edge("sinyalsiz", 1000, unhealthy), edge("kapali", 1000, disabled)}
	for _, kind := range []string{balancer.TS, balancer.HLS} {
		counts := spread(edges, kind)
		if len(counts) != 1 || counts["saglikli"] == 0 {
			t.Fatalf("%s: yalnızca sağlıklı ve etkin edge seçilmeliydi: %v", kind, counts)
		}
	}
}

func TestChooseReportsWhenNoEdgeIsUsable(t *testing.T) {
	edges := []store.Edge{edge("sinyalsiz", 10, unhealthy), edge("kapali", 10, disabled)}
	if e, ok := balancer.Choose(edges, balancer.TS, func(int) int { return 0 }); ok {
		t.Fatalf("kullanılabilir edge yokken %q seçildi", e.Name)
	}
	if _, ok := balancer.Choose(nil, balancer.TS, func(int) int { return 0 }); ok {
		t.Fatal("boş listeden edge seçildi")
	}
}

// Yerel edge'in HLS'i API'nin içinden verilir; .ts dağıtıcısı dursa da HLS yönlendirmesi sürer.
func TestLocalEdgeServesHLSEvenWithoutAHealthSignal(t *testing.T) {
	edges := []store.Edge{edge("yerel", 100, builtin, unhealthy)}
	if _, ok := balancer.Choose(edges, balancer.HLS, func(int) int { return 0 }); !ok {
		t.Fatal("yerel edge HLS için seçilmeliydi")
	}
	if _, ok := balancer.Choose(edges, balancer.TS, func(int) int { return 0 }); ok {
		t.Fatal("sinyalsiz yerel edge .ts için seçilmemeliydi")
	}
	if _, ok := balancer.Choose([]store.Edge{edge("yerel", 100, builtin, unhealthy, disabled)}, balancer.HLS, func(int) int { return 0 }); ok {
		t.Fatal("devre dışı bırakılmış yerel edge seçilmemeliydi")
	}
}

func remote(t *testing.T, s *store.Store, name, pullIP string) int64 {
	t.Helper()
	id, err := s.CreateEdge(context.Background(), store.NewEdge{Name: name, BaseURL: "http://" + name, ControlURL: "http://" + name,
		Key: "key-" + name, PullIP: pullIP, Weight: 100})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPickFollowsHealthSignal(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	b := balancer.New(s, window, 0)
	if _, err := s.SyncLocalEdge(ctx, "http://yerel-ts", "http://yerel-hls"); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Pick(ctx, balancer.TS); !errors.Is(err, balancer.ErrNoEdge) {
		t.Fatalf("hiçbir edge sinyal vermemişken: %v", err)
	}
	if e, err := b.Pick(ctx, balancer.HLS); err != nil || !e.Builtin {
		t.Fatalf("yerel HLS her zaman seçilebilmeli: %+v %v", e, err)
	}

	id := remote(t, s, "e1", "10.0.0.5")
	if err := s.MarkEdgeSeen(ctx, id); err != nil {
		t.Fatal(err)
	}
	if e, err := b.Pick(ctx, balancer.TS); err != nil || e.ID != id {
		t.Fatalf("sinyal veren edge seçilmeliydi: %+v %v", e, err)
	}

	// Sinyal kesilince yönlendirmeden çıkar, dönünce geri gelir.
	testdb.Exec(t, `UPDATE edges SET last_seen_at = now() - interval '1 minute' WHERE id = $1`, id)
	if _, err := b.Pick(ctx, balancer.TS); !errors.Is(err, balancer.ErrNoEdge) {
		t.Fatalf("sinyali kesilen edge'e yönlendirildi: %v", err)
	}
	if err := s.MarkEdgeSeen(ctx, id); err != nil {
		t.Fatal(err)
	}
	if e, err := b.Pick(ctx, balancer.TS); err != nil || e.ID != id {
		t.Fatalf("sinyali dönen edge yeniden seçilmeliydi: %+v %v", e, err)
	}
}

func TestByKeyRecognisesOnlyRegisteredRemoteEdges(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	b := balancer.New(s, window, 0)
	id := remote(t, s, "e1", "10.0.0.5")

	if e, ok, err := b.ByKey(ctx, "key-e1"); err != nil || !ok || e.ID != id {
		t.Fatalf("kayıtlı anahtar tanınmalıydı: %+v %v %v", e, ok, err)
	}
	for _, key := range []string{"", "key-e", "key-e1x", "baska"} {
		if _, ok, _ := b.ByKey(ctx, key); ok {
			t.Errorf("geçersiz anahtar %q tanındı", key)
		}
	}
	// Yerel edge'in anahtarı yoktur; boş anahtar onu bulmamalı.
	if e, ok, _ := b.ByKey(ctx, ""); ok {
		t.Fatalf("boş anahtar bir edge buldu: %+v", e)
	}
	// Devre dışı bırakılan edge'in bildirimleri gelmeye devam eder.
	if err := s.UpdateEdge(ctx, id, store.EdgeUpdate{Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.ByKey(ctx, "key-e1"); !ok {
		t.Fatal("devre dışı edge'in anahtarı tanınmalıydı")
	}
}

func TestPullAllowedOnlyFromEnabledEdgeAddresses(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	b := balancer.New(s, window, 0)
	id := remote(t, s, "e1", "10.0.0.5")
	remote(t, s, "e2", "2001:db8::5")

	for ip, want := range map[string]bool{
		"10.0.0.5":          true,
		"::ffff:10.0.0.5":   true,
		"2001:db8::5":       true,
		"10.0.0.6":          false,
		"2001:db8::6":       false,
		"":                  false,
		"10.0.0.5, 1.1.1.1": false,
	} {
		if got, err := b.PullAllowed(ctx, ip); err != nil || got != want {
			t.Errorf("PullAllowed(%q) = %v, %v; beklenen %v", ip, got, err, want)
		}
	}

	if err := s.UpdateEdge(ctx, id, store.EdgeUpdate{Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.PullAllowed(ctx, "10.0.0.5"); got {
		t.Fatal("devre dışı bırakılan edge origin'den çekebildi")
	}
}
