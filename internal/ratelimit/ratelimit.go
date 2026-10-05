// Package ratelimit, anahtar başına (ör. istemci IP'si) hatalı deneme sayar ve eşiği aşanı engeller.
package ratelimit

import (
	"sync"
	"time"
)

// maxEntries, bellekte tutulan anahtar sayısının üst sınırıdır. Tablo doluyken yeni anahtarlar
// izlenmez; böylece çok sayıda farklı anahtar üreten bir saldırgan belleği tüketemez.
const maxEntries = 100_000

const fullSweepInterval = time.Second

type entry struct {
	first time.Time
	count int
}

type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	entries   map[string]entry
	lastSweep time.Time
}

// New: bir anahtar window içinde max hataya ulaşırsa, ilk hatasından window sonrasına kadar engellenir.
func New(max int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{max: max, window: window, now: now, entries: map[string]entry{}, lastSweep: now()}
}

func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return false
	}
	if l.expired(e, l.now()) {
		delete(l.entries, key)
		return false
	}
	return e.count >= l.max
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= l.window {
		l.sweep(now)
	}
	e, ok := l.entries[key]
	if !ok || l.expired(e, now) {
		if !ok && len(l.entries) >= maxEntries {
			// Dolu tabloyu her hatada taramak pahalıdır; en çok saniyede bir taranır.
			if now.Sub(l.lastSweep) >= fullSweepInterval {
				l.sweep(now)
			}
			if len(l.entries) >= maxEntries {
				return
			}
		}
		e = entry{first: now}
	}
	e.count++
	l.entries[key] = e
}

func (l *Limiter) expired(e entry, now time.Time) bool {
	return now.Sub(e.first) >= l.window
}

func (l *Limiter) sweep(now time.Time) {
	for k, e := range l.entries {
		if l.expired(e, now) {
			delete(l.entries, k)
		}
	}
	l.lastSweep = now
}

func (l *Limiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
