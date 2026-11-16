package httpserve_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"kanalvo/internal/httpserve"
)

// listen, testin sahip olduğu bir dinleyici açar. Port hiç bırakılmadığı için başka bir süreç
// araya girip onu kapamaz.
func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// waitClosed, sunucunun Shutdown ile l'yi kapattığını doğrular: kapalı dinleyicide Accept
// net.ErrClosed döner, açık dinleyicide bekler.
func waitClosed(t *testing.T, l net.Listener, msg string) {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			c.Close()
		}
		accepted <- err
	}()
	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("%s: Accept hatası net.ErrClosed değil: %v", msg, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
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
	la, lb := listen(t), listen(t)
	a := la.Addr().String()
	sa, sb := httpserve.New(a, mux), httpserve.New(lb.Addr().String(), http.NewServeMux())
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- httpserve.RunWithListeners(ctx, 5*time.Second, map[*http.Server]net.Listener{sa: la, sb: lb}, sa, sb)
	}()

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
	waitClosed(t, lb, "ikinci sunucu da kapanmış olmalı")
}

func TestRunReturnsErrorWhenAServerCannotListen(t *testing.T) {
	busy := listen(t)
	open := listen(t)
	ok := httpserve.New(open.Addr().String(), http.NewServeMux())
	blocked := httpserve.New(busy.Addr().String(), http.NewServeMux())

	done := make(chan error, 1)
	go func() {
		done <- httpserve.RunWithListeners(context.Background(), time.Second, map[*http.Server]net.Listener{ok: open}, ok, blocked)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Fatalf("dolu port için EADDRINUSE bekleniyordu, gelen: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run dönmedi")
	}
	waitClosed(t, open, "bir sunucu açılamayınca diğeri de kapatılmalı")
}
