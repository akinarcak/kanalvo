// Package httpserve, zaman aşımları ayarlı HTTP sunucuları kurar ve birlikte çalıştırır.
package httpserve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// New, bağlantıların ve yanıtların süresiz açık kalmasını önleyen zaman aşımlarıyla sunucu kurar.
func New(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// Run, sunucuları başlatır. ctx iptal edildiğinde veya biri açılamadığında hepsini kapatır
// ve süren isteklerin bitmesini en çok grace kadar bekler.
func Run(ctx context.Context, grace time.Duration, servers ...*http.Server) error {
	return run(ctx, grace, nil, servers...)
}

// run, Run'ın gövdesidir. listeners'ta dinleyicisi verilen sunucu Addr'ı açmak yerine o
// dinleyiciden hizmet verir; testler port seçip bırakma yarışına girmeden sunucu başlatabilsin diye.
func run(ctx context.Context, grace time.Duration, listeners map[*http.Server]net.Listener, servers ...*http.Server) error {
	failed := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			var err error
			if l, ok := listeners[s]; ok {
				err = s.Serve(l)
			} else {
				err = s.ListenAndServe()
			}
			if !errors.Is(err, http.ErrServerClosed) {
				failed <- err
			}
		}()
	}

	var first error
	select {
	case <-ctx.Done():
	case first = <-failed:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
