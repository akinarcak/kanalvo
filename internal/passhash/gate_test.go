package passhash

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateHashesAndVerifies(t *testing.T) {
	g := NewGate(2, time.Second)
	ctx := context.Background()
	hash, err := g.Hash(ctx, "bir-sifre-123")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := g.Verify(ctx, hash, "bir-sifre-123"); err != nil || !ok {
		t.Fatalf("doğru şifre: %v, %v", ok, err)
	}
	if ok, err := g.Verify(ctx, hash, "yanlis"); err != nil || ok {
		t.Fatalf("yanlış şifre: %v, %v", ok, err)
	}
	if ok, err := g.Verify(ctx, "", "herhangi"); err != nil || ok {
		t.Fatalf("hesap yokken doğrulama başarısız olmalı: %v, %v", ok, err)
	}
}

// Eşzamanlı işlem sayısı sınırı aşılamaz; sırası gelmeyen istek ErrBusy alır.
func TestGateBoundsConcurrentWork(t *testing.T) {
	const slots = 2
	g := NewGate(slots, 50*time.Millisecond)
	for i := 0; i < slots; i++ {
		if err := g.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if _, err := g.Verify(context.Background(), "", "x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("dolu kapıda ErrBusy bekleniyordu, gelen: %v", err)
	}
	if _, err := g.Hash(context.Background(), "x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("dolu kapıda ErrBusy bekleniyordu, gelen: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("bekleme süresi sınırlı olmalı: %s", took)
	}
	g.release()
	if _, err := g.Verify(context.Background(), "", "x"); err != nil {
		t.Fatalf("yer açılınca işlem yürümeli: %v", err)
	}
}

func TestGateNeverRunsMoreThanItsSlots(t *testing.T) {
	const slots = 3
	g := NewGate(slots, 10*time.Second)
	var running, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.acquire(context.Background()); err != nil {
				t.Error(err)
				return
			}
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			g.release()
		}()
	}
	wg.Wait()
	if peak.Load() > slots {
		t.Fatalf("aynı anda %d işlem yürüdü, sınır %d", peak.Load(), slots)
	}
}

func TestGateRespectsCancellation(t *testing.T) {
	g := NewGate(1, time.Minute)
	if err := g.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Verify(ctx, "", "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("iptal edilen istek beklememeli, gelen: %v", err)
	}
}
