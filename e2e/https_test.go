//go:build e2e

package e2e

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Panel yalnızca caddy servisinden, HTTPS ile sunulur (bkz. deploy/caddy/Caddyfile). Simülasyonda
// PANEL_DOMAIN=localhost olduğu için Caddy sertifikayı kendi iç CA'sıyla üretir. Testler bu CA'nın
// kök sertifikasını kapsayıcıdan alıp güvenilen kökler arasına ekler; sertifika doğrulaması kapatılmaz.
const caddyRootPath = "/data/caddy/pki/authorities/local/root.crt"

// caddyRoots, sistem kökleri ile Caddy'nin iç CA'sıdır; yüklenemezse nil kalır.
var caddyRoots *x509.CertPool

func TestMain(m *testing.M) {
	if err := trustCaddyCA(60 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: Caddy'nin iç CA'sı yüklenemedi; panel testleri HTTPS'e bağlanamaz: %v\n", err)
	}
	os.Exit(m.Run())
}

// trustCaddyCA, Caddy'nin iç CA'sını varsayılan HTTP istemcisinin güvendiği köklere ekler ve panel
// HTTPS'ten yanıt verene kadar bekler (Caddy yaprak sertifikayı açılıştan hemen sonra üretir).
func trustCaddyCA(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := loadCaddyCA()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
}

func loadCaddyCA() error {
	cmd := exec.Command("docker", "compose", "exec", "-T", "caddy", "cat", caddyRootPath)
	cmd.Dir = ".."
	pem, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("kök sertifika okunamadı: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("kök sertifika çözülemedi")
	}
	// Ayrı istemci kurmayan bütün testler (panelClient, fetch, probe) http.DefaultTransport'u kullanır.
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(panelURL + "/")
	if err != nil {
		return err
	}
	resp.Body.Close()
	caddyRoots = pool
	return nil
}

func requireCaddyCA(t *testing.T) {
	t.Helper()
	if caddyRoots == nil {
		t.Fatal("Caddy'nin iç CA'sı yüklenemedi (caddy servisi ayakta mı?)")
	}
}

// Panel HTTPS'ten açılır ve giriş orada çalışır; sertifika panelin alan adı için Caddy'nin iç
// CA'sından gelir; giriş çerezi Secure ve HttpOnly'dir; düz HTTP istekleri panele ulaşmadan HTTPS'e
// yönlendirilir.
func TestPanelLoginOverHTTPS(t *testing.T) {
	requireCaddyCA(t)

	conn, err := tls.Dial("tcp", strings.TrimPrefix(panelURL, "https://"), &tls.Config{RootCAs: caddyRoots, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("panelin sertifikası doğrulanamadı: %v", err)
	}
	leaf := conn.ConnectionState().PeerCertificates[0]
	conn.Close()
	if !slices.Contains(leaf.DNSNames, "localhost") || !strings.Contains(leaf.Issuer.CommonName, "Caddy Local Authority") {
		t.Fatalf("beklenmeyen sertifika: adlar %v, veren %q", leaf.DNSNames, leaf.Issuer.CommonName)
	}

	stamp := time.Now().UnixNano()
	var created struct{ Email, Password string }
	if err := json.Unmarshal(compose(t, "exec", "-T", "api", "kanalvo", "create-admin", fmt.Sprintf("https-%d@example.com", stamp)), &created); err != nil {
		t.Fatalf("create-admin çıktısı çözülemedi: %v", err)
	}
	resp := panelLogin(t, panelURL, created.Email, created.Password, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil {
		t.Fatalf("HTTPS'ten giriş: durum %d, TLS %v", resp.StatusCode, resp.TLS != nil)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "kanalvo_panel" {
			session = c
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly {
		t.Fatalf("giriş çerezi Secure ve HttpOnly olmalı: %v", resp.Header.Values("Set-Cookie"))
	}

	me := func(base string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/me", nil)
		req.AddCookie(&http.Cookie{Name: session.Name, Value: session.Value})
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatalf("GET %s/api/me: %v", base, err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := me(panelURL); resp.StatusCode != http.StatusOK {
		t.Fatalf("çerezle /api/me: durum %d", resp.StatusCode)
	}

	// Caddy'nin 80 portu panele aktarmaz: çerezle ya da şifreyle gelen düz HTTP isteği HTTPS'e yönlenir.
	if resp := me(caddyHTTPURL); resp.StatusCode/100 != 3 || !strings.HasPrefix(resp.Header.Get("Location"), "https://localhost") {
		t.Fatalf("düz HTTP /api/me HTTPS'e yönlenmeli: durum %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = panelLogin(t, caddyHTTPURL, created.Email, created.Password, "")
	resp.Body.Close()
	if resp.StatusCode/100 != 3 || len(resp.Cookies()) != 0 {
		t.Fatalf("düz HTTP'den giriş kabul edilmemeli: durum %d, çerez %v", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
}

// api'nin düz HTTP panel portu (8002) yalnızca 127.0.0.1'e bağlıdır; makinenin dış arayüzünden
// ulaşılamaz. Aynı arayüzden Caddy'nin HTTPS portuna ulaşılır (dış adresin gerçekten kullanılabildiği
// böylece görülür).
func TestPanelPortIsOnlyOnLoopback(t *testing.T) {
	if got := strings.TrimSpace(string(compose(t, "port", "api", "8002"))); got != "127.0.0.1:"+panelPort {
		t.Fatalf("panel portu %q adresine bağlı, beklenen 127.0.0.1:%s", got, panelPort)
	}
	if err := dial("127.0.0.1", panelPort); err != nil {
		t.Fatalf("panel portuna bu makineden ulaşılmalı: %v", err)
	}
	ip := externalIPv4()
	if ip == "" {
		t.Skip("makinede loopback dışında IPv4 adresi yok")
	}
	if err := dial(ip, panelPort); err == nil {
		t.Fatalf("panel portu (%s:%s) dış arayüzden açık", ip, panelPort)
	}
	httpsPort := strings.TrimPrefix(panelURL, "https://localhost:")
	if err := dial(ip, httpsPort); err != nil {
		t.Fatalf("Caddy'nin HTTPS portuna dış arayüzden (%s:%s) ulaşılmalı: %v", ip, httpsPort, err)
	}
}

// Caddy istemcinin adresini X-Forwarded-For'a yazar; panel hatalı giriş kilidini vekilin değil,
// istemcinin adresine uygular. Kapsayıcının içinden gelen ikinci bir istemci eşiği doldurunca yalnızca
// o kilitlenir; bu makineden gelen istemci girmeye devam eder. İstemcinin uydurduğu X-Forwarded-For
// kilitten kurtarmaz (Caddy onu atar).
func TestLoginLockoutFollowsTheClientBehindTheProxy(t *testing.T) {
	requireCaddyCA(t)
	limit, err := strconv.Atoi(envValue(t, "LOGIN_MAX_FAILURES"))
	if err != nil || limit < 1 {
		t.Fatalf("LOGIN_MAX_FAILURES okunamadı: %v", err)
	}

	// Bu makineden gelen istemci: hatalı giriş 401 alır (kilitli değil).
	wrongLogin := func(spoof string) int {
		resp := panelLogin(t, panelURL, "kilit@example.invalid", "yanlis", spoof)
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := wrongLogin(""); code != http.StatusUnauthorized {
		t.Fatalf("ilk hatalı giriş 401 almalı, gelen %d", code)
	}

	// İkinci istemci: caddy kapsayıcısının içinden, Caddy'nin kendi 443 portuna (busybox wget). Her
	// istekte uydurulmuş bir X-Forwarded-For gönderir; Caddy bu başlığı atıp gerçek adresi yazmalı.
	script := `
login() {
	wget -S -O /dev/null --no-check-certificate \
		--header 'Content-Type: application/json' --header 'X-Kanalvo-Panel: 1' --header "X-Forwarded-For: $1" \
		--post-data '{"email":"kilit@example.invalid","password":"yanlis"}' https://localhost/api/login 2>&1 |
		sed -n 's|^ *HTTP/[0-9.]* \([0-9][0-9][0-9]\).*|\1|p' | head -n 1
}
i=0
while [ "$i" -lt "$1" ]; do login 203.0.113.$((i % 250 + 1)); i=$((i + 1)); done
login 198.51.100.9
`
	out := strings.Fields(string(compose(t, "exec", "-T", "caddy", "sh", "-c", script, "sh", strconv.Itoa(limit))))
	if len(out) != limit+1 {
		t.Fatalf("%d yanıt bekleniyordu, gelen %d: %v", limit+1, len(out), out)
	}
	for i, code := range out[:limit] {
		if code != "401" {
			t.Fatalf("ikinci istemcinin %d. hatalı girişi 401 almalı, gelen %s (X-Forwarded-For uydurularak kilit bölünmemeli de)", i+1, code)
		}
	}
	if out[limit] != "429" {
		t.Fatalf("%d hatalı girişten sonra ikinci istemci kilitlenmeli (429), gelen %s", limit, out[limit])
	}

	// Kilit vekile uygulanmış olsaydı bu makineden gelen istemci de 429 alırdı. Kilitli istemcinin
	// adresini uydurmak da onu kilitlemez.
	for _, spoof := range []string{"", "127.0.0.1", "::1"} {
		if code := wrongLogin(spoof); code != http.StatusUnauthorized {
			t.Fatalf("bu makineden gelen istemci (X-Forwarded-For %q) kilitlenmemeli: durum %d", spoof, code)
		}
	}
}

// panelLogin, panele giriş isteği gönderir; yönlendirmeyi izlemez. forwardedFor boş değilse
// istemcinin uydurduğu X-Forwarded-For başlığı olarak eklenir.
func panelLogin(t *testing.T, base, email, password, forwardedFor string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kanalvo-Panel", "1")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("POST %s/api/login: %v", base, err)
	}
	return resp
}

func dial(host, port string) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
	if err == nil {
		conn.Close()
	}
	return err
}

// externalIPv4, makinenin loopback dışındaki ilk IPv4 adresidir.
func externalIPv4() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.String()
		}
	}
	return ""
}
