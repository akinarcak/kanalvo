// Package balancer, izleme isteklerini edge'lere dağıtır ve edge'den gelen istekleri tanır.
package balancer

import (
	"context"
	"crypto/subtle"
	"errors"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"streamhub/internal/store"
)

// İzleme türleri.
const (
	TS  = "ts"
	HLS = "hls"
)

// KeyHeader, kontrol sunucusu ile uzak edge'in nginx'i arasındaki isteklerde edge anahtarını taşır.
const KeyHeader = "X-Edge-Key"

// ErrNoEdge: isteği karşılayabilecek sağlıklı ve etkin bir edge yok.
var ErrNoEdge = errors.New("balancer: sağlıklı edge yok")

type Balancer struct {
	store  *store.Store
	window time.Duration
	ttl    time.Duration
	intn   func(n int) int

	mu       sync.Mutex
	edges    []store.Edge
	loadedAt time.Time
}

// New: window içinde sağlık sinyali vermiş edge sağlıklı sayılır. Edge listesi ttl kadar bellekte
// tutulur; böylece her izleme isteği veritabanına gitmez. Bir edge'in eklenmesi, devre dışı
// bırakılması veya sinyalinin kesilmesi en geç ttl sonra etkili olur.
func New(s *store.Store, window, ttl time.Duration) *Balancer {
	return &Balancer{store: s, window: window, ttl: ttl, intn: rand.IntN}
}

func (b *Balancer) list(ctx context.Context) ([]store.Edge, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.edges != nil && time.Since(b.loadedAt) < b.ttl {
		return b.edges, nil
	}
	edges, err := b.store.Edges(ctx, b.window)
	if err != nil {
		return nil, err
	}
	b.edges, b.loadedAt = edges, time.Now()
	return edges, nil
}

// Pick, verilen izleme türü için bir edge seçer; uygun edge yoksa ErrNoEdge döner.
func (b *Balancer) Pick(ctx context.Context, kind string) (store.Edge, error) {
	edges, err := b.list(ctx)
	if err != nil {
		return store.Edge{}, err
	}
	e, ok := Choose(edges, kind, b.intn)
	if !ok {
		return store.Edge{}, ErrNoEdge
	}
	return e, nil
}

// Choose, uygun edge'ler arasından ağırlıklarıyla orantılı olasılıkla birini seçer.
// intn(n), [0, n) aralığında rastgele bir sayı döner.
func Choose(edges []store.Edge, kind string, intn func(n int) int) (store.Edge, bool) {
	total := 0
	for _, e := range edges {
		if eligible(e, kind) {
			total += e.Weight
		}
	}
	if total == 0 {
		return store.Edge{}, false
	}
	r := intn(total)
	for _, e := range edges {
		if !eligible(e, kind) {
			continue
		}
		if r < e.Weight {
			return e, true
		}
		r -= e.Weight
	}
	return store.Edge{}, false
}

// eligible: edge etkin, adresi belli ve sağlıklı olmalıdır. Yerel edge'in HLS'i API'nin içindeki
// geçitten verilir; isteği yanıtlayan API ayakta olduğuna göre .ts dağıtıcısının durumuna bakılmaz.
func eligible(e store.Edge, kind string) bool {
	if !e.Enabled || e.Weight <= 0 {
		return false
	}
	if kind == HLS {
		return e.HLSBaseURL != "" && (e.Healthy || e.Builtin)
	}
	return e.TSBaseURL != "" && e.Healthy
}

// ByKey, anahtarı verilen uzak edge'i bulur. Devre dışı bırakılmış edge de tanınır: süren
// izlemelerinin bildirimleri gelmeye devam eder.
func (b *Balancer) ByKey(ctx context.Context, key string) (store.Edge, bool, error) {
	if key == "" {
		return store.Edge{}, false, nil
	}
	edges, err := b.list(ctx)
	if err != nil {
		return store.Edge{}, false, err
	}
	var found store.Edge
	ok := false
	for _, e := range edges {
		if !e.Builtin && e.Key != "" && subtle.ConstantTimeCompare([]byte(e.Key), []byte(key)) == 1 {
			found, ok = e, true
		}
	}
	return found, ok, nil
}

// PullAllowed, verilen adresin origin'den yayın çekmesine izin verilen etkin bir uzak edge'e ait
// olup olmadığını söyler.
func (b *Balancer) PullAllowed(ctx context.Context, ip string) (bool, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false, nil
	}
	edges, err := b.list(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range edges {
		if e.Builtin || !e.Enabled {
			continue
		}
		if allowed, err := netip.ParseAddr(e.PullIP); err == nil && allowed.Unmap() == addr.Unmap() {
			return true, nil
		}
	}
	return false, nil
}
