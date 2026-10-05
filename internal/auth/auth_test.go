package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"streamhub/internal/auth"
	"streamhub/internal/ratelimit"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

const maxFailures = 3

type fixture struct {
	store  *store.Store
	tenant int64
	viewer int64
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func setup(t *testing.T, trustProxy bool) (*fixture, *auth.Authenticator) {
	ctx := context.Background()
	f := &fixture{store: testdb.New(t)}
	f.tenant = must(f.store.CreateTenant(ctx, "t"))
	f.viewer = must(f.store.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	limiter := ratelimit.New(maxFailures, time.Minute, time.Now)
	return f, auth.New(f.store, limiter, trustProxy)
}

// from, verilen bağlantı adresinden ve isteğe bağlı X-Forwarded-For başlığıyla gelen bir istek üretir.
func from(remote, forwardedFor string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return r
}

func fail(t *testing.T, a *auth.Authenticator, r *http.Request, times int) {
	t.Helper()
	for i := 0; i < times; i++ {
		if _, err := a.Viewer(r, "ali", "yanlis"); !errors.Is(err, auth.ErrInvalid) {
			t.Fatalf("%d. deneme: ErrInvalid bekleniyordu, gelen: %v", i+1, err)
		}
	}
}

func TestValidCredentials(t *testing.T) {
	f, a := setup(t, false)
	v, err := a.Viewer(from("1.2.3.4:1000", ""), "ali", "pw")
	if err != nil || v.ID != f.viewer {
		t.Fatalf("izleyici %+v, hata %v", v, err)
	}
}

func TestInvalidCredentials(t *testing.T) {
	_, a := setup(t, false)
	cases := map[string][2]string{
		"yanlış şifre":      {"ali", "yanlis"},
		"boş şifre":         {"ali", ""},
		"olmayan kullanıcı": {"yok", "pw"},
		"boş kullanıcı":     {"", "pw"},
		"NUL baytı":         {"ali\x00", "pw"},
		"geçersiz UTF-8":    {"\xff", "pw"},
	}
	i := 0
	for name, c := range cases {
		i++
		// Her durum ayrı IP'den gelir ki sınır devreye girmesin.
		r := from("10.0.0."+string(rune('0'+i))+":1", "")
		if _, err := a.Viewer(r, c[0], c[1]); !errors.Is(err, auth.ErrInvalid) {
			t.Errorf("%s: ErrInvalid bekleniyordu, gelen: %v", name, err)
		}
	}
}

func TestUnusableViewerIsReturnedAndNotCountedAsFailure(t *testing.T) {
	f, a := setup(t, false)
	if err := f.store.SetViewerStatus(context.Background(), f.viewer, "suspended"); err != nil {
		t.Fatal(err)
	}
	r := from("1.2.3.4:1000", "")
	for i := 0; i < maxFailures+2; i++ {
		v, err := a.Viewer(r, "ali", "pw")
		if err != nil || v.Status != "suspended" {
			t.Fatalf("askıdaki izleyici hatasız dönmeli: %+v, %v", v, err)
		}
	}
}

func TestBlocksIPAfterMaxFailures(t *testing.T) {
	_, a := setup(t, false)
	attacker := from("1.2.3.4:1000", "")
	fail(t, a, attacker, maxFailures)

	if _, err := a.Viewer(attacker, "ali", "pw"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("eşikten sonra doğru şifre de reddedilmeli, gelen: %v", err)
	}
	if _, err := a.Viewer(from("1.2.3.4:2000", ""), "ali", "pw"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("aynı IP'nin başka portu da engelli olmalı, gelen: %v", err)
	}
	if _, err := a.Viewer(from("5.6.7.8:1000", ""), "ali", "pw"); err != nil {
		t.Fatalf("başka IP etkilenmemeli: %v", err)
	}
}

func TestForwardedForIsIgnoredUnlessTrusted(t *testing.T) {
	_, a := setup(t, false)
	for i := 0; i < maxFailures; i++ {
		spoofed := from("1.2.3.4:1000", "9.9.9."+string(rune('0'+i)))
		if _, err := a.Viewer(spoofed, "ali", "yanlis"); !errors.Is(err, auth.ErrInvalid) {
			t.Fatalf("ErrInvalid bekleniyordu, gelen: %v", err)
		}
	}
	if _, err := a.Viewer(from("1.2.3.4:1000", "8.8.8.8"), "ali", "pw"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("sahte başlıkla sınır atlatılamamalı, gelen: %v", err)
	}
}

func TestTrustedProxyUsesLastForwardedAddress(t *testing.T) {
	_, a := setup(t, true)
	proxy := "172.18.0.2:5555"
	// İstemci ilk değerleri uydurabilir; vekilin eklediği son değer gerçek adrestir.
	fail(t, a, from(proxy, "6.6.6.6, 9.9.9.9"), maxFailures)

	if _, err := a.Viewer(from(proxy, "9.9.9.9"), "ali", "pw"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("son adres engellenmeli, gelen: %v", err)
	}
	if _, err := a.Viewer(from(proxy, "9.9.9.9, 8.8.8.8"), "ali", "pw"); err != nil {
		t.Fatalf("aynı vekilin arkasındaki başka izleyici etkilenmemeli: %v", err)
	}
	if _, err := a.Viewer(from(proxy, "6.6.6.6"), "ali", "pw"); err != nil {
		t.Fatalf("uydurulan ilk adres engellenmemeli: %v", err)
	}
}

func TestTrustedProxyFallsBackToConnectionAddress(t *testing.T) {
	_, a := setup(t, true)
	fail(t, a, from("1.2.3.4:1000", ""), maxFailures-1)
	fail(t, a, from("1.2.3.4:1000", "bozuk-adres"), 1)
	if _, err := a.Viewer(from("1.2.3.4:1000", ""), "ali", "pw"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("başlık yokken veya bozukken bağlantı adresi kullanılmalı, gelen: %v", err)
	}
}

func TestIPv6IsLimitedPerSlash64(t *testing.T) {
	_, a := setup(t, false)
	fail(t, a, from("[2001:db8:0:1::1]:1000", ""), 1)
	fail(t, a, from("[2001:db8:0:1::2]:1000", ""), 1)
	fail(t, a, from("[2001:db8:0:1:ffff::3]:1000", ""), 1)

	if _, err := a.Viewer(from("[2001:db8:0:1::99]:1000", ""), "ali", "pw"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("aynı /64 içindeki adresler tek istemci sayılmalı, gelen: %v", err)
	}
	if _, err := a.Viewer(from("[2001:db8:0:2::1]:1000", ""), "ali", "pw"); err != nil {
		t.Fatalf("başka /64 etkilenmemeli: %v", err)
	}
}
