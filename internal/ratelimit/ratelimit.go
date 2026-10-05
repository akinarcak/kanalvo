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

// New: bir anahtar window içinde max hatalı deneme yaparsa, ilk hatasından window sonrasına kadar engellenir.
func New(max int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{max: max, window: window, now: now, entries: map[string]entry{}, lastSweep: now()}
}

// Allow, bir deneme hakkı ayırır; anahtar eşiğe ulaşmışsa false döner. Ayırma ve sayma tek
// adımda yapılır, böylece eşzamanlı istekler eşiği aşamaz. Deneme başarılı çıkarsa çağıran
// Success ile hakkı geri verir; geri verilmeyen her hak bir hata sayılır.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= l.window {
		l.sweep(now)
	}
	e, ok := l.entries[key]
	if ok && l.expired(e, now) {
		delete(l.entries, key)
		ok = false
	}
	if !ok {
		if len(l.entries) >= maxEntries {
			// Dolu tabloyu her denemede taramak pahalıdır; en çok saniyede bir taranır.
			if now.Sub(l.lastSweep) >= fullSweepInterval {
				l.sweep(now)
			}
			if len(l.entries) >= maxEntries {
				// Tablo dolu: yeni anahtar izlenemez. Belleği sınırlamak için bilinçli tercih.
				return true
			}
		}
		e = entry{first: now}
	}
	if e.count >= l.max {
		return false
	}
	e.count++
	l.entries[key] = e
	return true
}

// Success, başarılı çıkan bir deneme için Allow ile ayrılan hakkı geri verir.
func (l *Limiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return
	}
	if e.count--; e.count <= 0 {
		delete(l.entries, key)
		return
	}
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
