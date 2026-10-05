package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest(max int, window time.Duration) (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	return New(max, window, c.now), c
}

// fail, sonucu hatalı çıkan bir denemeyi temsil eder: izin alınır ve geri verilmez.
func fail(l *Limiter, key string) bool { return l.Allow(key) }

func TestBlocksAfterMaxFailuresWithinWindow(t *testing.T) {
	l, _ := newTest(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !fail(l, "a") {
			t.Fatalf("%d. denemeye izin verilmeliydi", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("3 hatadan sonra deneme engellenmeli")
	}
}

func TestSuccessDoesNotConsumeAnAttempt(t *testing.T) {
	l, _ := newTest(2, time.Minute)
	for i := 0; i < 50; i++ {
		if !l.Allow("a") {
			t.Fatalf("%d. başarılı deneme engellendi", i+1)
		}
		l.Success("a")
	}
	fail(l, "a")
	fail(l, "a")
	if l.Allow("a") {
		t.Fatal("başarılı denemeler hata sayacını sıfırlamamalı")
	}
	l.Success("a")
	l.Success("a")
	l.Success("yok")
	if n := l.len(); n > 1 {
		t.Fatalf("Success yeni kayıt oluşturmamalı: %d", n)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l, _ := newTest(1, time.Minute)
	fail(l, "a")
	if l.Allow("a") || !l.Allow("b") {
		t.Fatal("yalnızca hata yapan anahtar engellenmeli")
	}
}

func TestUnblocksWhenWindowEnds(t *testing.T) {
	l, c := newTest(2, time.Minute)
	fail(l, "a")
	c.t = c.t.Add(30 * time.Second)
	fail(l, "a")
	c.t = c.t.Add(29 * time.Second)
	if l.Allow("a") {
		t.Fatal("pencere bitmeden engel kalkmamalı")
	}
	c.t = c.t.Add(time.Second)
	if !fail(l, "a") {
		t.Fatal("engel, ilk hatadan bir pencere sonra kalkmalı")
	}
	if !l.Allow("a") {
		t.Fatal("yeni pencerede sayaç sıfırdan başlamalı")
	}
}

// Eşzamanlı istekler eşiği aşamamalı: izin alma ve sayma tek adımdır.
func TestConcurrentAttemptsCannotExceedMax(t *testing.T) {
	const max = 20
	l, _ := newTest(max, time.Minute)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 500; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("saldırgan") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != max {
		t.Fatalf("%d denemeye izin verildi, eşik %d", got, max)
	}
}

func TestTableIsBoundedAndFailsOpenWhenFull(t *testing.T) {
	l, c := newTest(1, time.Minute)
	fail(l, "kurban")
	start := time.Now()
	for i := 0; i < maxEntries*2; i++ {
		fail(l, fmt.Sprintf("k%d", i))
	}
	// Tablo doluyken her deneme tüm tabloyu taramamalı; aksi halde dolu tablo bir yavaşlatma saldırısı olur.
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("dolu tabloda %d deneme %s sürdü", maxEntries*2, took)
	}
	if n := l.len(); n > maxEntries {
		t.Fatalf("kayıt sayısı %d, sınır %d", n, maxEntries)
	}
	if l.Allow("kurban") {
		t.Fatal("tablo dolsa da izlenen anahtarın engeli sürmeli")
	}
	// Bilinen sınır: tablo doluyken yeni anahtarlar izlenemez ve sınırsız deneyebilir.
	if !l.Allow("izlenemeyen") || !l.Allow("izlenemeyen") {
		t.Fatal("tablo doluyken yeni anahtara izin verilmeli (bellek sınırı için bilinçli tercih)")
	}

	c.t = c.t.Add(2 * time.Minute)
	fail(l, "yeni")
	if n := l.len(); n != 1 {
		t.Fatalf("süresi dolan kayıtlar temizlenmeli, kalan: %d", n)
	}
}
