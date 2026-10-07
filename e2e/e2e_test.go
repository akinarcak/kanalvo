//go:build e2e

// Bu test çalışan bir "docker compose up" ortamı ister; bkz. README.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	apiURL    = "http://localhost:8000" // Xtream uçları ve HLS geçidi
	tsURL     = "http://localhost:8081" // kesintisiz .ts veren SRS
	originURL = "http://localhost:8080" // origin SRS'in HTTP portu; dışarıya açık olmamalı
	pubName   = "kanalvo-e2e-pub"
)

type seed struct {
	TenantID     int64  `json:"tenant_id"`
	ChannelID    int64  `json:"channel_id"`
	StreamSecret string `json:"stream_secret"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

var noRedirect = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func compose(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = ".."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args[:2], " "), err, stderr.String())
	}
	return out
}

// rtmpPlay, origin'den RTMP ile 2 saniye izlemeyi dener; izleme reddedilirse hata döner.
func rtmpPlay(channelID int64, query string) error {
	cmd := exec.Command("docker", "compose", "--profile", "e2e", "run", "--rm", "ffmpeg",
		"-v", "error", "-rw_timeout", "5000000", "-t", "2",
		"-i", fmt.Sprintf("rtmp://srs/live/%d%s", channelID, query), "-f", "null", "-")
	cmd.Dir = ".."
	return cmd.Run()
}

// envValue, ../.env dosyasından bir değeri okur.
func envValue(t *testing.T, key string) string {
	t.Helper()
	data, err := os.ReadFile("../.env")
	if err != nil {
		t.Fatalf(".env okunamadı: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return v
		}
	}
	t.Fatalf(".env içinde %s yok", key)
	return ""
}

// getJSON, adresi çağırır ve 200 yanıtının JSON gövdesini v içine çözer.
func getJSON(t *testing.T, u string, v any) {
	t.Helper()
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("durum %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("JSON çözülemedi: %v", err)
	}
}

type xtreamAccount struct {
	UserInfo struct {
		Auth       int    `json:"auth"`
		Status     string `json:"status"`
		ActiveCons string `json:"active_cons"`
	} `json:"user_info"`
	ServerInfo struct {
		URL  string `json:"url"`
		Port string `json:"port"`
	} `json:"server_info"`
}

func (s seed) xtreamURL(script, extra string) string {
	return fmt.Sprintf("%s/%s?username=%s&password=%s%s", apiURL, script, s.Username, s.Password, extra)
}

// startPublisher, kanala FFmpeg kapsayıcısından sahte bir yayın başlatır.
func startPublisher(t *testing.T, s seed) {
	t.Helper()
	compose(t, "--profile", "e2e", "run", "-d", "--rm", "--name", pubName, "ffmpeg",
		"-re", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "flv",
		fmt.Sprintf("rtmp://srs/live/%d?secret=%s", s.ChannelID, s.StreamSecret))
}

func publisherRunning() bool { return containerRunning(pubName) }

func containerRunning(name string) bool {
	out, _ := exec.Command("docker", "ps", "-q", "--filter", "name="+name).Output()
	return strings.TrimSpace(string(out)) != ""
}

func stopContainer(name string) { exec.Command("docker", "rm", "-f", name).Run() }

// watch, bir .ts yayınını bağlantı kopana kadar okur. started, ilk veri gelince; ended,
// bağlantı sunucu tarafından kesilince kapanır.
func watch(u string) (started, ended chan struct{}) {
	started, ended = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		resp, err := (&http.Client{}).Get(u)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 32<<10)
		first := true
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 && first {
				first = false
				close(started)
			}
			if err != nil {
				return
			}
		}
	}()
	return started, ended
}

func (s seed) playURL(ext string) string {
	return fmt.Sprintf("%s/live/%s/%s/%d.%s", apiURL, s.Username, s.Password, s.ChannelID, ext)
}

// probe, yönlendirmeyi izlemeden durum kodunu ve Location başlığını döner.
func probe(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// fetch, yönlendirmeleri izleyerek en çok n bayt okur; son adresi de döner.
// Bağlantı kurulamazsa veya durum 200 değilse gövde boştur.
func fetch(u string, n int) ([]byte, *url.URL) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.Request.URL
	}
	buf := make([]byte, n)
	read, _ := io.ReadFull(resp.Body, buf)
	return buf[:read], resp.Request.URL
}

func isMPEGTS(b []byte) bool {
	return len(b) >= 3*188 && b[0] == 0x47 && b[188] == 0x47 && b[376] == 0x47
}

func isPlaylist(b []byte) bool { return strings.HasPrefix(string(b), "#EXTM3U") }

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s için %s beklendi, gerçekleşmedi", what, timeout)
}

func firstSegment(playlist string) string {
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func TestSingleChannelEndToEnd(t *testing.T) {
	var s seed
	if err := json.Unmarshal(compose(t, "exec", "-T", "api", "kanalvo", "seed-dev"), &s); err != nil {
		t.Fatalf("seed-dev çıktısı çözülemedi: %v", err)
	}

	for _, ext := range []string{"ts", "m3u8"} {
		if code, _ := probe(t, s.playURL(ext)); code != http.StatusNotFound {
			t.Fatalf("yayın yokken %s için 404 bekleniyordu, gelen %d", ext, code)
		}
	}

	startPublisher(t, s)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", pubName).Run() })

	// Kesintisiz .ts
	var tsLocation string
	waitFor(t, "kanalın yayında görünmesi", 30*time.Second, func() bool {
		code, loc := probe(t, s.playURL("ts"))
		tsLocation = loc
		return code == http.StatusFound
	})
	if !strings.HasPrefix(tsLocation, tsURL+"/live/") {
		t.Fatalf("beklenmeyen .ts yönlendirmesi: %s", tsLocation)
	}
	waitFor(t, "kesintisiz TS verisi", 30*time.Second, func() bool {
		body, _ := fetch(s.playURL("ts"), 3*188)
		return isMPEGTS(body)
	})

	// Bir IPTV oynatıcısının yaptığı gibi: giriş, kanal listesi, M3U ve listedeki adresten izleme.
	var account xtreamAccount
	getJSON(t, s.xtreamURL("player_api.php", ""), &account)
	if account.UserInfo.Auth != 1 || account.UserInfo.Status != "Active" || account.ServerInfo.URL != "localhost" || account.ServerInfo.Port != "8000" {
		t.Fatalf("beklenmeyen giriş yanıtı: %+v", account)
	}
	var streams []struct {
		StreamID int64  `json:"stream_id"`
		Name     string `json:"name"`
	}
	getJSON(t, s.xtreamURL("player_api.php", "&action=get_live_streams"), &streams)
	if len(streams) != 1 || streams[0].StreamID != s.ChannelID {
		t.Fatalf("kanal listesi yalnızca bu yayıncının kanalını içermeli: %+v", streams)
	}
	m3u, _ := fetch(s.xtreamURL("get.php", "&type=m3u_plus&output=ts"), 64<<10)
	listed := strings.Fields(string(m3u))
	if !isPlaylist(m3u) || listed[len(listed)-1] != s.playURL("ts") {
		t.Fatalf("M3U listesi beklenen yayın adresini içermiyor: %d satır", len(listed))
	}
	if body, _ := fetch(listed[len(listed)-1], 3*188); !isMPEGTS(body) {
		t.Fatal("M3U listesindeki adres yayın döndürmedi")
	}
	shortURL := fmt.Sprintf("%s/%s/%s/%d", apiURL, s.Username, s.Password, s.ChannelID)
	if code, loc := probe(t, shortURL); code != http.StatusFound || !strings.HasPrefix(loc, tsURL+"/live/") {
		t.Fatalf("kısa adres yönlendirmedi: durum %d", code)
	}

	// HLS
	_, hlsLocation := probe(t, s.playURL("m3u8"))
	if !strings.HasPrefix(hlsLocation, apiURL+"/hls/") {
		t.Fatalf("beklenmeyen HLS yönlendirmesi: %s", hlsLocation)
	}
	for _, loc := range []string{tsLocation, hlsLocation} {
		if strings.Contains(loc, s.StreamSecret) || strings.Contains(loc, s.Password) {
			t.Fatal("yönlendirme adresi gizli değer içeriyor")
		}
	}
	var playlist string
	var playlistURL *url.URL
	waitFor(t, "HLS çalma listesi", 60*time.Second, func() bool {
		body, final := fetch(s.playURL("m3u8"), 64<<10)
		playlist, playlistURL = string(body), final
		return isPlaylist(body) && firstSegment(playlist) != ""
	})
	segURL, err := playlistURL.Parse(firstSegment(playlist))
	if err != nil {
		t.Fatalf("parça adresi çözülemedi: %v", err)
	}
	if body, _ := fetch(segURL.String(), 3*188); !isMPEGTS(body) {
		t.Fatalf("HLS parçası MPEG-TS değil: %s", segURL.Path)
	}
	segFile := segURL.Path[strings.LastIndex(segURL.Path, "/")+1:]

	// Yetkisiz erişim
	wrong := s
	wrong.Password = "yanlis"
	for _, ext := range []string{"ts", "m3u8"} {
		if code, _ := probe(t, wrong.playURL(ext)); code != http.StatusForbidden {
			t.Fatalf("yanlış şifrede %s için 403 bekleniyordu, gelen %d", ext, code)
		}
	}
	var denied xtreamAccount
	getJSON(t, wrong.xtreamURL("player_api.php", ""), &denied)
	if denied.UserInfo.Auth != 0 {
		t.Fatalf("yanlış şifreyle giriş kabul edildi: %+v", denied)
	}
	if body, _ := fetch(wrong.xtreamURL("get.php", ""), 64<<10); isPlaylist(body) {
		t.Fatal("yanlış şifreyle M3U listesi verildi")
	}
	forged := fmt.Sprintf("1.%d.9999999999.AAAA", s.ChannelID)
	for _, direct := range []string{
		fmt.Sprintf("%s/live/%d.ts", tsURL, s.ChannelID),
		fmt.Sprintf("%s/live/%d.ts?token=%s", tsURL, s.ChannelID, forged),
		fmt.Sprintf("%s/hls/%s/%d.m3u8", apiURL, forged, s.ChannelID),
		fmt.Sprintf("%s/hls/%s/%s", apiURL, forged, segFile),
		fmt.Sprintf("%s/live/%d.m3u8", originURL, s.ChannelID),
		fmt.Sprintf("%s/live/%s", originURL, segFile),
	} {
		if body, _ := fetch(direct, 3*188); isMPEGTS(body) || isPlaylist(body) {
			t.Fatalf("imzasız veya sahte imzalı istek yayın döndürdü: %s", direct)
		}
	}

	// Bağlantı limiti (izleyicinin limiti 1): yeni bir izleme, süren en eski izlemenin yerini alır.
	firstStarted, firstEnded := watch(s.playURL("ts"))
	select {
	case <-firstStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("ilk .ts izlemesi başlamadı")
	}
	var busy xtreamAccount
	getJSON(t, s.xtreamURL("player_api.php", ""), &busy)
	if busy.UserInfo.ActiveCons != "1" {
		t.Fatalf("süren bir izleme varken active_cons %q", busy.UserInfo.ActiveCons)
	}
	secondStarted, secondEnded := watch(s.playURL("ts"))
	select {
	case <-secondStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("ikinci .ts izlemesi başlamadı (kanal değiştiren izleyici takılmamalı)")
	}
	select {
	case <-firstEnded:
	case <-time.After(15 * time.Second):
		t.Fatal("limit 1 iken ilk .ts izlemesi kesilmedi")
	}
	select {
	case <-secondEnded:
		t.Fatal("yeni izleme de kesildi")
	case <-time.After(3 * time.Second):
	}

	// Aynısı HLS için: yeni bir HLS adresi alınınca eskisi kullanılamaz olur (ve yukarıdaki .ts izlemesi kesilir).
	oldPlaylist, oldURL := fetch(s.playURL("m3u8"), 64<<10)
	if !isPlaylist(oldPlaylist) {
		t.Fatal("HLS adresi çalma listesi döndürmedi")
	}
	if newPlaylist, _ := fetch(s.playURL("m3u8"), 64<<10); !isPlaylist(newPlaylist) {
		t.Fatal("yeni HLS adresi kabul edilmeli")
	}
	if body, _ := fetch(oldURL.String(), 64<<10); isPlaylist(body) {
		t.Fatal("limit 1 iken eski HLS adresi hâlâ çalışıyor")
	}
	select {
	case <-secondEnded:
	case <-time.After(15 * time.Second):
		t.Fatal("HLS izlemesi başlayınca süren .ts izlemesi kesilmedi")
	}

	// Origin'in RTMP portu yalnızca yayıncılar içindir; izleme geçerli bir imzayla bile reddedilir.
	tsToken := tsLocation[strings.Index(tsLocation, "?token=")+len("?token="):]
	_, freshLocation := probe(t, s.playURL("ts"))
	freshToken := freshLocation[strings.Index(freshLocation, "?token=")+len("?token="):]
	for _, query := range []string{"", "?token=" + forged, "?token=" + freshToken} {
		if err := rtmpPlay(s.ChannelID, query); err == nil {
			t.Fatal("origin üzerinden RTMP izleme kabul edildi")
		}
	}

	// SRS'in yönetim API'si (bağlantı listesi, bağlantı kesme) dışarıya açık hiçbir porttan sunulmamalı.
	for _, u := range []string{tsURL + "/api/v1/clients/", tsURL + "/api/v1/streams/", originURL + "/api/v1/clients/", "http://localhost:1985/api/v1/clients/"} {
		if body, _ := fetch(u, 64<<10); strings.Contains(string(body), `"clients"`) || strings.Contains(string(body), `"streams"`) {
			t.Fatalf("SRS yönetim API'si dışarıdan erişilebilir: %s", u)
		}
	}

	// SRS yetki sorguları izleyicilere açık portta bulunmamalı (yol varsa GET 405 döner).
	if code, _ := probe(t, apiURL+"/hooks/srs/x/publish"); code != http.StatusNotFound {
		t.Fatalf("SRS sorgu ucu dış portta erişilebilir görünüyor: durum %d", code)
	}

	// Yayın bitince
	if err := exec.Command("docker", "rm", "-f", pubName).Run(); err != nil {
		t.Fatalf("yayıncı durdurulamadı: %v", err)
	}
	waitFor(t, "kanalın çevrimdışı olması", 30*time.Second, func() bool {
		code, _ := probe(t, s.playURL("ts"))
		return code == http.StatusNotFound
	})
	if body, _ := fetch(playlistURL.String(), 64<<10); isPlaylist(body) {
		t.Fatal("yayın bittikten sonra eski HLS adresi çalma listesi döndürmemeli")
	}

	// Askıya alınan yayıncının süren yayını kesilir ve izleyicileri izleyemez.
	startPublisher(t, s)
	waitFor(t, "kanalın yeniden yayında görünmesi", 30*time.Second, func() bool {
		code, _ := probe(t, s.playURL("ts"))
		return code == http.StatusFound
	})
	compose(t, "exec", "-T", "api", "kanalvo", "set-tenant-status", fmt.Sprint(s.TenantID), "suspended")
	waitFor(t, "askıdaki yayıncının yayınının kesilmesi", 30*time.Second, func() bool { return !publisherRunning() })
	for _, ext := range []string{"ts", "m3u8"} {
		if code, _ := probe(t, s.playURL(ext)); code != http.StatusForbidden {
			t.Fatalf("askıdaki yayıncının izleyicisi %s için 403 almalı, gelen %d", ext, code)
		}
	}
	var disabled xtreamAccount
	getJSON(t, s.xtreamURL("player_api.php", ""), &disabled)
	if disabled.UserInfo.Status != "Disabled" {
		t.Fatalf("askıdaki yayıncının izleyicisi için durum %q", disabled.UserInfo.Status)
	}

	// Kabul edilen yayın ve izlemeler hiçbir servisin loguna gizli değer bırakmamalı.
	// SRS, reddettiği sorguların adresini (HOOK_SECRET) hata satırına yazar; bu engellenemez
	// (bkz. docs/srs-findings.md), bu yüzden HOOK_SECRET yalnızca API logunda aranır.
	apiLogs := string(compose(t, "logs", "--no-color", "api"))
	allLogs := apiLogs + string(compose(t, "logs", "--no-color", "srs", "srs-ts"))
	for name, secret := range map[string]string{
		"gizli yayın anahtarı": s.StreamSecret,
		"izleyici şifresi":     s.Password,
		".ts imzası":           tsToken,
		"TOKEN_KEY":            envValue(t, "TOKEN_KEY"),
	} {
		if strings.Contains(allLogs, secret) {
			t.Errorf("servis loglarında %s görünüyor", name)
		}
	}
	if strings.Contains(apiLogs, envValue(t, "HOOK_SECRET")) {
		t.Error("API logunda HOOK_SECRET görünüyor")
	}
}
