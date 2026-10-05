// Package sessiontest, testlerde SRS yönetim API'sinin yerine geçen sahte bir uygulama sunar.
package sessiontest

import (
	"context"
	"fmt"
	"sync"
)

// FakeSRS, session.SRS arayüzünü uygular: bağlantı listesini taklit eder ve kesme isteklerini kaydeder.
type FakeSRS struct {
	mu      sync.Mutex
	Clients []string
	// KickErr verilirse her kesme isteği bu hatayla başarısız olur (istek yine de kaydedilir).
	KickErr error
	kicked  []string
}

func (f *FakeSRS) ClientIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Clients, nil
}

func (f *FakeSRS) Kick(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kicked = append(f.kicked, id)
	return f.KickErr
}

// Kicked, kesilmesi istenen bağlantıları "[a b]" biçiminde döner.
func (f *FakeSRS) Kicked() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprint(f.kicked)
}
