package enroll_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanalvo/internal/enroll"
	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

const (
	token       = "0123456789abcdef0123456789abcdef"
	healthyFor  = 15 * time.Second
	codeTTL     = 30 * time.Minute
	fixedKey    = "anahtar-0123456789abcdef0123456789abcdef"
	controlURL  = "http://tv.example.com:8000"
	originInEnv = "ORIGIN_RTMP=yayin.example.com:1935\n"
)

type fixture struct {
	t     *testing.T
	store *store.Store
	mux   *http.ServeMux
}

func setup(t *testing.T, trustProxy bool) *fixture {
	f := &fixture{t: t, store: testdb.New(t), mux: http.NewServeMux()}
	enroll.New(f.store, enroll.Config{
		PublicBaseURL: controlURL, IngestURL: "rtmp://yayin.example.com/live", TrustProxyHeaders: trustProxy,
	}, func() string { return fixedKey }).Register(f.mux)
	if _, err := f.store.CreateEdgeEnrollment(context.Background(), token, codeTTL); err != nil {
		t.Fatal(err)
	}
	return f
}

// do, isteği verilen adresten gelmiş gibi işler ve durum koduyla gövdeyi döner.
func (f *fixture) do(method, path, remoteAddr string, form url.Values, header ...string) (int, string) {
	f.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	r.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func (f *fixture) remoteEdges() []store.Edge {
	f.t.Helper()
	all, err := f.store.Edges(context.Background(), healthyFor)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.Edge
	for _, e := range all {
		if !e.Builtin {
			out = append(out, e)
		}
	}
	return out
}

func TestInstallScriptCarriesTheFilesAndTheCode(t *testing.T) {
	f := setup(t, false)
	code, body := f.do("GET", "/edge/install/"+token, "203.0.113.20:5555", nil)
	if code != http.StatusOK {
		t.Fatalf("kurulum betiği verilmeliydi: %d %s", code, body)
	}
	for _, want := range []string{
		"#!/bin/sh", "CONTROL_URL='" + controlURL + "'", "TOKEN='" + token + "'",
		"edge-srs:", "proxy_cache_path", "http_hooks", "/edge/enroll/$TOKEN",
		// Her ağ isteği süreyle sınırlıdır; yanıt vermeyen bir adres betiği askıda bırakmaz.
		"curl -fsS --max-time 30 \"$@\" -X POST", "curl -fsS --max-time 3 -o /dev/null",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("betikte %q yok", want)
		}
	}
	// Betiği indirmek kodu tüketmez; yarıda kalan kurulum aynı komutla yinelenebilir.
	if code, _ := f.do("GET", "/edge/install/"+token, "203.0.113.20:5555", nil); code != http.StatusOK {
		t.Fatalf("betik ikinci kez indirilemedi: %d", code)
	}
	if len(f.remoteEdges()) != 0 {
		t.Fatal("betiği indirmek sunucu kaydetmemeli")
	}
}

func TestUnknownCodeGetsNothing(t *testing.T) {
	f := setup(t, false)
	for _, bad := range []string{strings.Repeat("f", 32), "kisa", token + "0", strings.ToUpper(token)} {
		if code, body := f.do("GET", "/edge/install/"+bad, "203.0.113.20:5555", nil); code != http.StatusNotFound || strings.Contains(body, "EDGE_KEY") {
			t.Errorf("geçersiz kod %q için betik verildi: %d", bad, code)
		}
		if code, body := f.do("POST", "/edge/enroll/"+bad, "203.0.113.20:5555", url.Values{}); code != http.StatusNotFound || strings.Contains(body, "EDGE_KEY") {
			t.Errorf("geçersiz kod %q ile kayıt yapıldı: %d", bad, code)
		}
	}
	if len(f.remoteEdges()) != 0 {
		t.Fatal("geçersiz kodla sunucu kaydedildi")
	}
}

func TestEnrollRegistersTheCallerOnce(t *testing.T) {
	f := setup(t, false)
	code, body := f.do("POST", "/edge/enroll/"+token, "203.0.113.20:5555", url.Values{"port": {"8090"}})
	want := "CONTROL_URL=" + controlURL + "\nEDGE_KEY=" + fixedKey + "\n" + originInEnv + "EDGE_PORT=8090\n"
	if code != http.StatusOK || body != want {
		t.Fatalf("kayıt yanıtı: %d %q", code, body)
	}
	edges := f.remoteEdges()
	if len(edges) != 1 {
		t.Fatalf("bir sunucu kaydedilmeliydi: %+v", edges)
	}
	e := edges[0]
	if e.Enabled || e.Key != fixedKey || e.PullIP != "203.0.113.20" || e.TSBaseURL != "http://203.0.113.20:8090" ||
		e.ControlURL != e.TSBaseURL || e.Name != "Sunucu 203.0.113.20" || e.Weight != 100 {
		t.Fatalf("kaydolan sunucu: %+v", e)
	}

	// Kod tek kullanımlıktır: ikinci kayıt da, betiğin yeniden indirilmesi de reddedilir.
	if code, body := f.do("POST", "/edge/enroll/"+token, "198.51.100.7:1", url.Values{}); code != http.StatusNotFound || strings.Contains(body, "EDGE_KEY") {
		t.Fatalf("kullanılmış kodla ikinci kayıt: %d", code)
	}
	if code, _ := f.do("GET", "/edge/install/"+token, "198.51.100.7:1", nil); code != http.StatusNotFound {
		t.Fatalf("kullanılmış kodla betik verildi: %d", code)
	}
	if len(f.remoteEdges()) != 1 {
		t.Fatal("ikinci sunucu kaydedilmemeliydi")
	}
}

func TestEnrollAddressForms(t *testing.T) {
	cases := []struct {
		name, remote string
		form         url.Values
		trust        bool
		header       []string
		base, pullIP string
	}{
		{name: "varsayılan port adrese yazılmaz", remote: "203.0.113.20:5555", form: url.Values{}, base: "http://203.0.113.20", pullIP: "203.0.113.20"},
		{name: "IPv6 köşeli ayraçla", remote: "[2001:db8::7]:5555", form: url.Values{"port": {"8080"}}, base: "http://[2001:db8::7]:8080", pullIP: "2001:db8::7"},
		{name: "vekil arkasında başlıktaki adres", remote: "10.0.0.2:5555", form: url.Values{}, trust: true,
			header: []string{"X-Forwarded-For", "198.51.100.9"}, base: "http://198.51.100.9", pullIP: "198.51.100.9"},
		{name: "güvenilmeyen başlık yok sayılır", remote: "203.0.113.20:5555", form: url.Values{},
			header: []string{"X-Forwarded-For", "198.51.100.9"}, base: "http://203.0.113.20", pullIP: "203.0.113.20"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := setup(t, c.trust)
			if code, body := f.do("POST", "/edge/enroll/"+token, c.remote, c.form, c.header...); code != http.StatusOK {
				t.Fatalf("kayıt: %d %s", code, body)
			}
			e := f.remoteEdges()[0]
			if e.TSBaseURL != c.base || e.PullIP != c.pullIP {
				t.Fatalf("adres %q, çekme adresi %q", e.TSBaseURL, e.PullIP)
			}
		})
	}
}

func TestEnrollRejectsABadPortWithoutSpendingTheCode(t *testing.T) {
	f := setup(t, false)
	for _, port := range []string{"0", "70000", "abc", "-1"} {
		if code, _ := f.do("POST", "/edge/enroll/"+token, "203.0.113.20:5555", url.Values{"port": {port}}); code != http.StatusBadRequest {
			t.Errorf("port %q kabul edildi: %d", port, code)
		}
	}
	if code, _ := f.do("POST", "/edge/enroll/"+token, "203.0.113.20:5555", url.Values{"port": {"8090"}}); code != http.StatusOK {
		t.Fatalf("hatalı denemeler kodu tüketmemeliydi: %d", code)
	}
}

// Betik gerçek bir kabukta, sahte docker ve curl ile çalıştırılır: dosyaları yazmalı, sunucuyu
// kaydetmeli, ayarları yalnızca yöneticinin okuyabileceği .env dosyasına koymalı ve servisleri başlatmalıdır.
func TestInstallScriptRunsEndToEnd(t *testing.T) {
	for _, tool := range []string{"sh", "wget", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s yok; betik çalıştırılamıyor", tool)
		}
	}
	st := testdb.New(t)
	if _, err := st.CreateEdgeEnrollment(context.Background(), token, codeTTL); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	enroll.New(st, enroll.Config{PublicBaseURL: srv.URL, IngestURL: "rtmp://yayin.example.com/live"},
		func() string { return fixedKey }).Register(mux)

	res, err := http.Get(srv.URL + "/edge/install/" + token)
	if err != nil {
		t.Fatal(err)
	}
	scriptBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("betik alınamadı: %d", res.StatusCode)
	}

	work := t.TempDir()
	bin, dir := filepath.Join(work, "bin"), filepath.Join(work, "kurulum")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shims := map[string]string{
		"id":     "#!/bin/sh\necho 0\n",
		"docker": "#!/bin/sh\necho \"$*\" >> \"" + filepath.Join(work, "docker.log") + "\"\n",
		// curl yerine: kayıt isteğini wget ile gönderir, sağlık yoklamasını başarılı sayar.
		"curl": `#!/bin/sh
data=""; url=""; post=no
while [ $# -gt 0 ]; do
  case "$1" in
    -X) post=yes; shift ;;
    --data) data="$2"; shift ;;
    -H|-o|--max-time) shift ;;
    -4) echo ipv4 >> "$(dirname "$0")/../curl.log" ;;
    http*) url="$1" ;;
  esac
  shift
done
[ "$post" = yes ] || exit 0
exec wget -q -O - --post-data "$data" "$url"
`,
	}
	for name, body := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scriptPath := filepath.Join(work, "install.sh")
	if err := os.WriteFile(scriptPath, scriptBody, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		cmd := exec.Command("sh", scriptPath)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "KANALVO_EDGE_DIR="+dir, "EDGE_PORT=8090")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run()
	if err != nil {
		t.Fatalf("betik başarısız: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Kurulum tamam") {
		t.Errorf("betik kurulumun bittiğini söylemedi:\n%s", out)
	}
	for _, name := range []string{"docker-compose.yml", "nginx.conf.template", "srs-edge.conf.tmpl", "kaldir.sh", ".env"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Size() == 0 {
			t.Errorf("%s yazılmadı: %v", name, err)
		}
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if !strings.Contains(string(compose), "$$CONTROL_URL") || !strings.Contains(string(compose), "${EDGE_PORT:-80}:80") {
		t.Errorf("kurulum dosyası kabuk tarafından değiştirilmiş:\n%s", compose)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	want := "CONTROL_URL=" + srv.URL + "\nEDGE_KEY=" + fixedKey + "\n" + originInEnv + "EDGE_PORT=8090\n"
	if string(env) != want {
		t.Errorf(".env:\n%q\nbeklenen:\n%q", env, want)
	}
	if info, err := os.Stat(filepath.Join(dir, ".env")); err == nil && info.Mode().Perm()&0o077 != 0 {
		t.Errorf(".env başkalarınca okunabiliyor: %v", info.Mode().Perm())
	}
	dockerLog, _ := os.ReadFile(filepath.Join(work, "docker.log"))
	if !strings.Contains(string(dockerLog), "compose up -d") {
		t.Errorf("servisler başlatılmadı: %q", dockerLog)
	}
	if curlLog, _ := os.ReadFile(filepath.Join(work, "curl.log")); !strings.Contains(string(curlLog), "ipv4") {
		t.Error("kayıt isteği önce IPv4 ile denenmeliydi")
	}
	edges, _ := st.Edges(context.Background(), healthyFor)
	if len(edges) != 2 || edges[1].Enabled || edges[1].Key != fixedKey || edges[1].TSBaseURL != "http://127.0.0.1:8090" {
		t.Fatalf("betik sunucuyu kaydetmedi: %+v", edges)
	}

	// Kod kullanıldı: betik yeniden çalıştırılırsa açık bir hatayla durur ve servislere dokunmaz.
	out, err = run()
	if err == nil || !strings.Contains(out, "Kayıt başarısız") {
		t.Fatalf("kullanılmış kodla betik hata vermeliydi: %v\n%s", err, out)
	}
	dockerLog2, _ := os.ReadFile(filepath.Join(work, "docker.log"))
	if strings.Count(string(dockerLog2), "compose up -d") != 1 {
		t.Errorf("başarısız kayıttan sonra servisler yeniden başlatıldı: %q", dockerLog2)
	}
}
