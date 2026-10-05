package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest(max int, window time.Duration) (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	return New(max, window, c.now), c
}

func TestBlocksAfterMaxFailuresWithinWindow(t *testing.T) {
	l, _ := newTest(3, time.Minute)
	for i := 0; i < 2; i++ {
		l.Fail("a")
		if l.Blocked("a") {
			t.Fatalf("%d hatadan sonra engellenmemeli", i+1)
		}
	}
	l.Fail("a")
	if !l.Blocked("a") {
		t.Fatal("3. hatadan sonra engellenmeli")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l, _ := newTest(1, time.Minute)
	l.Fail("a")
	if !l.Blocked("a") || l.Blocked("b") {
		t.Fatal("yalnızca hata yapan anahtar engellenmeli")
	}
}

func TestUnblocksWhenWindowEnds(t *testing.T) {
	l, c := newTest(2, time.Minute)
	l.Fail("a")
	l.Fail("a")
	c.t = c.t.Add(59 * time.Second)
	if !l.Blocked("a") {
		t.Fatal("pencere bitmeden engel kalkmamalı")
	}
	c.t = c.t.Add(time.Second)
	if l.Blocked("a") {
		t.Fatal("pencere bitince engel kalkmalı")
	}
	l.Fail("a")
	if l.Blocked("a") {
		t.Fatal("yeni pencerede sayaç sıfırdan başlamalı")
	}
}

func TestFailuresWhileBlockedDoNotExtendTheBlock(t *testing.T) {
	l, c := newTest(1, time.Minute)
	l.Fail("a")
	c.t = c.t.Add(30 * time.Second)
	l.Fail("a")
	c.t = c.t.Add(30 * time.Second)
	if l.Blocked("a") {
		t.Fatal("engel, ilk hatadan bir pencere sonra kalkmalı")
	}
}

func TestEntryCountIsBounded(t *testing.T) {
	l, c := newTest(1, time.Minute)
	l.Fail("kurban")
	start := time.Now()
	for i := 0; i < maxEntries*2; i++ {
		l.Fail(fmt.Sprintf("k%d", i))
	}
	// Tablo doluyken her hata tüm tabloyu taramamalı; aksi halde dolu tablo bir yavaşlatma saldırısı olur.
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("dolu tabloda %d hata %s sürdü", maxEntries*2, took)
	}
	if n := l.len(); n > maxEntries {
		t.Fatalf("kayıt sayısı %d, sınır %d", n, maxEntries)
	}

	c.t = c.t.Add(2 * time.Minute)
	l.Fail("yeni")
	if n := l.len(); n != 1 {
		t.Fatalf("süresi dolan kayıtlar temizlenmeli, kalan: %d", n)
	}
}

func TestConcurrentUse(t *testing.T) {
	l, _ := newTest(1000, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("k%d", i%10)
				l.Fail(key)
				l.Blocked(key)
			}
		}()
	}
	wg.Wait()
}
