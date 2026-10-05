// Package auth, izleyiciyi kullanıcı adı ve şifresiyle doğrular ve IP başına hatalı giriş sınırı uygular.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"unicode/utf8"

	"streamhub/internal/ratelimit"
	"streamhub/internal/store"
)

var (
	ErrInvalid     = errors.New("auth: geçersiz kullanıcı adı veya şifre")
	ErrRateLimited = errors.New("auth: çok fazla hatalı deneme")
)

type Authenticator struct {
	store             *store.Store
	limiter           *ratelimit.Limiter
	trustProxyHeaders bool
}

// New: trustProxyHeaders yalnızca API, X-Forwarded-For başlığını kendisi yazan bir vekilin
// arkasındayken açılmalıdır; aksi halde istemci başlığı uydurarak sınırı atlatabilir.
func New(s *store.Store, l *ratelimit.Limiter, trustProxyHeaders bool) *Authenticator {
	return &Authenticator{store: s, limiter: l, trustProxyHeaders: trustProxyHeaders}
}

// Viewer, kullanıcı adı ve şifresi doğru olan izleyiciyi döner. Dönen izleyici askıda veya
// süresi dolmuş olabilir; buna çağıran karar verir. Hatalar: ErrInvalid, ErrRateLimited
// veya veritabanı hatası.
func (a *Authenticator) Viewer(r *http.Request, username, password string) (store.Viewer, error) {
	key := a.clientKey(r)
	// Deneme hakkı veritabanına gitmeden önce ayrılır; böylece eşzamanlı istekler eşiği aşamaz.
	if !a.limiter.Allow(key) {
		return store.Viewer{}, ErrRateLimited
	}
	v, err := a.lookup(r.Context(), username)
	if err == nil && subtle.ConstantTimeCompare([]byte(password), []byte(v.Password)) == 1 {
		a.limiter.Success(key)
		return v, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.limiter.Success(key) // veritabanı hatası istemcinin hatası değildir
		return store.Viewer{}, err
	}
	return store.Viewer{}, ErrInvalid
}

func (a *Authenticator) lookup(ctx context.Context, username string) (store.Viewer, error) {
	// PostgreSQL geçersiz UTF-8 ve NUL baytını hatayla reddeder; böyle bir ad zaten var olamaz.
	if username == "" || !utf8.ValidString(username) || strings.ContainsRune(username, 0) {
		return store.Viewer{}, store.ErrNotFound
	}
	return a.store.ViewerByUsername(ctx, username)
}

// clientKey, sınırın uygulandığı anahtarı döner: IPv4 adresi veya IPv6 /64 öneki
// (tek bir abone genellikle koca bir /64'e sahiptir).
func (a *Authenticator) clientKey(r *http.Request) string {
	var ip netip.Addr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		ip = ap.Addr()
	}
	if a.trustProxyHeaders {
		// İstemci baştaki değerleri uydurabilir; vekilin eklediği son değer gerçek adrestir.
		forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
		last := strings.TrimSpace(forwarded[strings.LastIndex(forwarded, ",")+1:])
		if parsed, err := netip.ParseAddr(last); err == nil {
			ip = parsed
		}
	}
	ip = ip.Unmap()
	switch {
	case !ip.IsValid():
		return "unknown"
	case ip.Is6():
		prefix, _ := ip.Prefix(64)
		return prefix.String()
	default:
		return ip.String()
	}
}
