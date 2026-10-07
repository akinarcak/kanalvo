package httpserve_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"kanalvo/internal/httpserve"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitUp(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s dinlemeye başlamadı", addr)
}

func TestNewSetsTimeouts(t *testing.T) {
	s := httpserve.New(":0", http.NewServeMux())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 {
		t.Fatalf("tüm zaman aşımları ayarlanmalı: %+v", s)
	}
}

func TestRunWaitsForInFlightRequestsOnShutdown(t *testing.T) {
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "done")
	})
	a, b := freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- httpserve.Run(ctx, 5*time.Second, httpserve.New(a, mux), httpserve.New(b, http.NewServeMux()))
	}()
	waitUp(t, a)
	waitUp(t, b)

	body := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + a + "/slow")
		if err != nil {
			body <- "hata: " + err.Error()
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		body <- string(data)
	}()
	<-started
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run hata döndürdü: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run dönmedi")
	}
	select {
	case got := <-body:
		if got != "done" {
			t.Fatalf("süren istek tamamlanmalıydı, gelen: %q", got)
		}
	default:
		t.Fatal("Run, süren istek bitmeden döndü")
	}
	if _, err := net.DialTimeout("tcp", b, 200*time.Millisecond); err == nil {
		t.Fatal("ikinci sunucu da kapanmış olmalı")
	}
}

func TestRunReturnsErrorWhenAServerCannotListen(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	free := freeAddr(t)

	done := make(chan error, 1)
	go func() {
		done <- httpserve.Run(context.Background(), time.Second, httpserve.New(free, http.NewServeMux()), httpserve.New(l.Addr().String(), http.NewServeMux()))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dolu port için hata bekleniyordu")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run dönmedi")
	}
	if _, err := net.DialTimeout("tcp", free, 200*time.Millisecond); err == nil {
		t.Fatal("bir sunucu açılamayınca diğeri de kapatılmalı")
	}
}
