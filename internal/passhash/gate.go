package passhash

import (
	"context"
	"errors"
	"time"
)

// ErrBusy: aynı anda çok fazla şifre işlemi sürüyor; istek sırasını bekleyemedi.
var ErrBusy = errors.New("passhash: çok fazla eşzamanlı şifre işlemi")

// Gate, aynı anda yürüyen Argon2 işlemlerinin sayısını sınırlar. Her işlem onlarca megabayt
// bellek kullanır; sınırsız eşzamanlı giriş denemesi sunucunun belleğini tüketebilir ve aynı
// süreçte çalışan yayın yetkilendirmesini de düşürür.
type Gate struct {
	slots chan struct{}
	wait  time.Duration
}

// NewGate: en çok concurrent işlem birlikte yürür; sıra wait içinde gelmezse ErrBusy döner.
func NewGate(concurrent int, wait time.Duration) *Gate {
	return &Gate{slots: make(chan struct{}, concurrent), wait: wait}
}

func (g *Gate) acquire(ctx context.Context) error {
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		return nil
	case <-timer.C:
		return ErrBusy
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Gate) release() { <-g.slots }

// Hash, sıra gelince şifreyi özetler.
func (g *Gate) Hash(ctx context.Context, password string) (string, error) {
	if err := g.acquire(ctx); err != nil {
		return "", err
	}
	defer g.release()
	return Hash(password)
}

// Verify, sıra gelince şifreyi doğrular. encoded boşsa (hesap yok) sahte bir doğrulama yapar ve
// false döner; böylece yanıt süresi hesabın var olup olmadığını ele vermez.
func (g *Gate) Verify(ctx context.Context, encoded, password string) (bool, error) {
	if err := g.acquire(ctx); err != nil {
		return false, err
	}
	defer g.release()
	if encoded == "" {
		return VerifyDummy(password), nil
	}
	return Verify(encoded, password), nil
}
