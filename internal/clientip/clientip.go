// Package clientip, bir isteğin hangi istemciden geldiğini belirleyen anahtarı üretir.
// Hatalı giriş sınırı ve HLS oturumları istemcileri bu anahtarla ayırt eder.
package clientip

import (
	"net/http"
	"net/netip"
	"strings"
)

// Key, IPv4 adresini veya IPv6 /64 önekini döner (tek bir abone genellikle koca bir /64'e
// sahiptir ve adresi bu aralıkta sık değişir).
//
// trustProxyHeaders yalnızca API, X-Forwarded-For başlığını kendisi yazan bir vekilin
// arkasındayken açılmalıdır; aksi halde istemci başlığı uydurarak kendini başkası gibi gösterebilir.
func Key(r *http.Request, trustProxyHeaders bool) string {
	var ip netip.Addr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		ip = ap.Addr()
	}
	if trustProxyHeaders {
		// İstemci baştaki değerleri uydurabilir; vekilin eklediği son değer gerçek adrestir.
		forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
		last := strings.TrimSpace(forwarded[strings.LastIndex(forwarded, ",")+1:])
		if parsed, err := netip.ParseAddr(last); err == nil {
			ip = parsed
		}
	}
	return key(ip)
}

// Normalize, metin olarak verilen bir adresi Key ile aynı biçime çevirir. Adres, güvenilen bir
// kaynaktan (ör. kimliği doğrulanmış bir edge'in bildirdiği izleyici adresi) gelmelidir.
func Normalize(raw string) string {
	ip, _ := netip.ParseAddr(strings.TrimSpace(raw))
	return key(ip)
}

func key(ip netip.Addr) string {
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
