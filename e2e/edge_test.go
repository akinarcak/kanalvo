//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	edgeURL  = "http://localhost:8090" // ikinci sunucunun izleyicilere açık adresi
	edgePort = "8090"
	edgeSRS  = "streamhub-edge-edge-srs-1"
)

// edgeCompose, uzak edge'in Compose projesini ana sunucunun ağına bağlı olarak yönetir.
func edgeCompose(t *testing.T, env []string, args ...string) {
	t.Helper()
	full := append([]string{"compose", "-f", "deploy/edge/docker-compose.yml", "-f", "deploy/edge/docker-compose.e2e.yml"}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("edge compose %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
}

type edgeInfo struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Builtin        bool   `json:"builtin"`
	Enabled        bool   `json:"enabled"`
	Healthy        bool   `json:"healthy"`
	ActiveSessions int    `json:"active_sessions"`
	Setup          string `json:"setup"`
}

func edgeByID(c *panelClient, id int64) edgeInfo {
	var list []edgeInfo
	c.call("GET", "/api/admin/edges", nil, &list, http.StatusOK)
	for _, e := range list {
		if e.ID == id {
			return e
		}
	}
	return edgeInfo{}
}

// redirectHosts, aynı yayın adresini n kez ister ve yönlendirilen sunucuları sayar.
func redirectHosts(t *testing.T, playURL string, n int) map[string]int {
	t.Helper()
	hosts := map[string]int{}
	for i := 0; i < n; i++ {
		code, loc := probe(t, playURL)
		if code != http.StatusFound {
			t.Fatalf("yönlendirme bekleniyordu, gelen %d", code)
		}
		u, err := url.Parse(loc)
		if err != nil {
			t.Fatal(err)
		}
		hosts[u.Host]++
	}
	return hosts
}

func status(u string, header ...string) int {
	code, _ := statusAndHeader(u, header...)
	return code
}

func statusAndHeader(u string, header ...string) (int, http.Header) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return 0, nil
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// dockerValue, bir docker komutunun çıktısını boşlukları atarak döner.
func dockerValue(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// İkinci bir sunucu edge olarak kaydedildiğinde izleyiciler iki sunucuya dağılmalı; edge'den hem
// .ts hem HLS izlenebilmeli; edge durduğunda izleyiciler yerel sunucuya dönmeli.
func TestSecondServerSharesTheViewers(t *testing.T) {
	stamp := time.Now().UnixNano()
	var created struct{ Email, Password string }
	if err := json.Unmarshal(compose(t, "exec", "-T", "api", "streamhub", "create-admin", fmt.Sprintf("edge-yonetici-%d@example.com", stamp)), &created); err != nil {
		t.Fatalf("create-admin çıktısı çözülemedi: %v", err)
	}
	admin := newPanelClient(t)
	admin.login(created.Email, created.Password)

	var tenant struct {
		Tenant   struct{ ID int64 }
		Password string
	}
	tenantEmail := fmt.Sprintf("edge-yayinci-%d@example.com", stamp)
	admin.call("POST", "/api/admin/tenants", map[string]string{"name": "Edge Yayıncısı", "email": tenantEmail}, &tenant, http.StatusCreated)
	owner := newPanelClient(t)
	owner.login(tenantEmail, tenant.Password)
	var channel struct {
		ID        int64  `json:"id"`
		StreamKey string `json:"stream_key"`
	}
	owner.call("POST", "/api/tenant/channels", map[string]any{"name": "Edge Kanalı"}, &channel, http.StatusCreated)
	var viewer struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	owner.call("POST", "/api/tenant/viewers", map[string]any{"username": fmt.Sprintf("edge%d", stamp%1000000), "max_connections": 5}, &viewer, http.StatusCreated)
	playURL := func(ext string) string {
		return fmt.Sprintf("%s/live/%s/%s/%d.%s", apiURL, viewer.Username, viewer.Password, channel.ID, ext)
	}

	// Yerel edge'i bul; test sonunda yeniden etkin kalmalı.
	var all []edgeInfo
	admin.call("GET", "/api/admin/edges", nil, &all, http.StatusOK)
	var local edgeInfo
	for _, e := range all {
		if e.Builtin {
			local = e
		}
	}
	if local.ID == 0 || !local.Healthy {
		t.Fatalf("yerel edge sağlıklı olmalı: %+v", all)
	}
	localPath := fmt.Sprintf("/api/admin/edges/%d", local.ID)
	t.Cleanup(func() { admin.call("PATCH", localPath, map[string]any{"enabled": true}, nil, http.StatusOK) })

	// İkinci sunucuyu kaydet. Çekme adresi kapsayıcı başlayınca belli olur.
	var edge edgeInfo
	admin.call("POST", "/api/admin/edges", map[string]any{
		"name": fmt.Sprintf("e2e-edge-%d", stamp), "base_url": edgeURL, "control_url": "http://edge-nginx", "pull_ip": "192.0.2.1",
	}, &edge, http.StatusCreated)
	edgePath := fmt.Sprintf("/api/admin/edges/%d", edge.ID)
	var key string
	for _, line := range strings.Split(edge.Setup, "\n") {
		if v, ok := strings.CutPrefix(line, "EDGE_KEY="); ok {
			key = v
		}
	}
	if key == "" {
		t.Fatalf("kurulum bilgisinde edge anahtarı yok: %q", edge.Setup)
	}
	// E2E_EDGE_CONTROL_URL: edge'in ana sunucuya başka bir adresten (ör. HTTPS sunan bir vekil
	// üzerinden) bağlanmasını denemek içindir.
	controlURL := os.Getenv("E2E_EDGE_CONTROL_URL")
	if controlURL == "" {
		controlURL = "http://api:8000"
	}
	env := []string{"CONTROL_URL=" + controlURL, "EDGE_KEY=" + key, "ORIGIN_RTMP=srs:1935", "EDGE_PORT=" + edgePort}
	t.Cleanup(func() {
		edgeCompose(t, env, "down", "-t", "2")
		// Kayıt kalırsa sonraki testlerin izleyicileri olmayan bir sunucuya yönlendirilebilir.
		req, _ := http.NewRequest(http.MethodDelete, panelURL+edgePath, nil)
		req.Header.Set("X-StreamHub-Panel", "1")
		if resp, err := admin.http.Do(req); err == nil {
			resp.Body.Close()
		}
	})
	edgeCompose(t, env, "up", "-d")

	waitFor(t, "edge'in sağlık sinyali vermesi", 40*time.Second, func() bool { return edgeByID(admin, edge.ID).Healthy })

	// Yayını başlat.
	const publisher = "sh-e2e-edge-pub"
	compose(t, "--profile", "e2e", "run", "-d", "--rm", "--name", publisher, "ffmpeg",
		"-re", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "flv", "rtmp://srs/live/"+channel.StreamKey)
	t.Cleanup(func() { stopContainer(publisher) })
	waitFor(t, "kanalın yayında görünmesi", 30*time.Second, func() bool {
		code, _ := probe(t, playURL("ts"))
		return code == http.StatusFound
	})

	// 1. İzleyiciler iki sunucuya dağılır.
	for _, ext := range []string{"ts", "m3u8"} {
		hosts := redirectHosts(t, playURL(ext), 60)
		if len(hosts) != 2 || hosts["localhost:"+edgePort] == 0 {
			t.Fatalf(".%s izleyicileri iki sunucuya dağılmalıydı: %v", ext, hosts)
		}
	}

	// Bundan sonrası yalnızca edge'i sınar: yerel sunucu yönlendirmeden çıkarılır.
	admin.call("PATCH", localPath, map[string]any{"enabled": false}, nil, http.StatusOK)
	waitFor(t, "yönlendirmenin yalnızca edge'e gitmesi", 15*time.Second, func() bool {
		hosts := redirectHosts(t, playURL("ts"), 10)
		return len(hosts) == 1 && hosts["localhost:"+edgePort] == 10
	})

	// 2. Çekme adresi kayıtlı değilken edge origin'den yayın alamaz.
	// Tek deneme yetmez: ilk istek, edge henüz origin'e bağlanmadığı için de boş dönebilir.
	for i := 0; i < 3; i++ {
		if body, _ := fetch(playURL("ts"), 3*188); isMPEGTS(body) {
			t.Fatalf("çekme adresi kayıtlı olmayan edge origin'den yayın alabildi (%d. deneme)", i+1)
		}
	}
	pullIP := dockerValue(t, "inspect", "-f", `{{(index .NetworkSettings.Networks "streamhub_default").IPAddress}}`, edgeSRS)
	admin.call("PATCH", edgePath, map[string]any{"pull_ip": pullIP}, nil, http.StatusOK)

	// 3. Edge'den kesintisiz .ts.
	var final *url.URL
	waitFor(t, "edge'den kesintisiz TS verisi", 40*time.Second, func() bool {
		body, u := fetch(playURL("ts"), 3*188)
		final = u
		return isMPEGTS(body)
	})
	if final.Host != "localhost:"+edgePort {
		t.Fatalf(".ts edge'den gelmeliydi: %s", final.Host)
	}

	// 4. Edge'deki izleme merkezde oturum olarak görünür; izleyicinin adresi edge'in değil izleyicinindir.
	_, tsLocation := probe(t, playURL("ts"))
	watching, ended := watch(tsLocation)
	select {
	case <-watching:
	case <-time.After(20 * time.Second):
		t.Fatal("edge'de izleme başlamadı")
	}
	// Oturuma yazılan adres, edge'in nginx'inin bu testin isteklerinde gördüğü istemci adresi
	// olmalıdır. Hangi adres olduğu Docker kurulumuna göre değişir; nginx'in erişim kaydından okunur.
	// nginx isteği bitince kaydeder: süren izleme henüz kayıtta yoktur, okunan satır 3. adımdaki
	// tamamlanmış .ts isteğine aittir (aynı test sürecinden, yani aynı adresten).
	viewerIP := ""
	for _, line := range strings.Split(dockerValue(t, "logs", "--tail", "200", "streamhub-edge-edge-nginx-1"), "\n") {
		if ip, rest, found := strings.Cut(line, " "); found && strings.Contains(rest, fmt.Sprintf(`"GET /live/%d.ts?`, channel.ID)) {
			viewerIP = ip
		}
	}
	if viewerIP == "" {
		t.Fatal("edge'in nginx erişim kaydında izleme isteği bulunamadı")
	}
	waitFor(t, "edge'deki izlemenin panelde görünmesi", 15*time.Second, func() bool {
		var sessions []struct{ Viewer, Edge, Kind, IP string }
		owner.call("GET", "/api/tenant/sessions", nil, &sessions, http.StatusOK)
		for _, s := range sessions {
			if s.Viewer == viewer.Username && s.Kind == "ts" && s.Edge == edge.Name {
				if s.IP != viewerIP {
					t.Fatalf("oturuma izleyicinin adresi (%s) yazılmalıydı: %s", viewerIP, s.IP)
				}
				return true
			}
		}
		return false
	})
	if n := edgeByID(admin, edge.ID).ActiveSessions; n < 1 {
		t.Fatalf("edge'in izleme sayısı: %d", n)
	}

	// 5. Edge'den HLS: çalma listesi ve parça edge'in adresinden gelir.
	var playlist string
	var playlistURL *url.URL
	waitFor(t, "edge'den HLS çalma listesi", 60*time.Second, func() bool {
		body, u := fetch(playURL("m3u8"), 64<<10)
		playlist, playlistURL = string(body), u
		return isPlaylist(body) && firstSegment(playlist) != ""
	})
	if playlistURL.Host != "localhost:"+edgePort {
		t.Fatalf("HLS edge'den gelmeliydi: %s", playlistURL.Host)
	}
	segURL, err := playlistURL.Parse(firstSegment(playlist))
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := fetch(segURL.String(), 3*188); !isMPEGTS(body) {
		t.Fatalf("edge'den HLS parçası MPEG-TS değil: %s", segURL.Path)
	}
	// Aynı parçanın ikinci isteği ana sunucuya gitmez, edge'in önbelleğinden verilir.
	if code, h := statusAndHeader(segURL.String()); code != http.StatusOK || h.Get("X-Cache-Status") != "HIT" {
		t.Fatalf("parça edge'in önbelleğinden gelmeliydi: durum %d, önbellek %q", code, h.Get("X-Cache-Status"))
	}
	segFile := segURL.Path[strings.LastIndex(segURL.Path, "/")+1:]

	// 6. Edge'de yetkisiz erişim.
	for _, c := range []struct {
		what, url string
		want      int
	}{
		{"imzasız parça (iç konum)", edgeURL + "/_segment/" + segFile, http.StatusNotFound},
		{"sahte imzayla parça", edgeURL + "/hls/sahte/" + segFile, http.StatusForbidden},
		{"sahte imzayla çalma listesi", fmt.Sprintf("%s/hls/sahte/%d.m3u8", edgeURL, channel.ID), http.StatusForbidden},
		{"anahtarsız yönetim API'si", edgeURL + "/_srs/api/v1/clients/", http.StatusForbidden},
		{"SRS'in kendi sayfaları", edgeURL + "/", http.StatusNotFound},
		{"SRS'in HLS çıkışı", fmt.Sprintf("%s/live/%d.m3u8", edgeURL, channel.ID), http.StatusNotFound},
	} {
		if code := status(c.url); code != c.want {
			t.Errorf("edge'de %s: durum %d, beklenen %d", c.what, code, c.want)
		}
	}
	// SRS reddettiği izlemeyi sürümüne göre farklı bildirir (5.0.225 yanıt vermeden kapatır, nginx
	// 502 döner); değişmeyen kural, sahte imzanın hata almasıdır.
	if code := status(fmt.Sprintf("%s/live/%d.ts?token=sahte", edgeURL, channel.ID)); code < 400 {
		t.Errorf("edge'de sahte imzayla .ts: durum %d", code)
	}
	// Ana sunucunun edge uçları anahtarsız kullanılamaz; izleyici adresini de belirleyemez.
	for _, u := range []string{apiURL + "/edge/segment/" + segFile, apiURL + "/edge" + playlistURL.Path} {
		if code := status(u, "X-Real-IP", "203.0.113.7"); code != http.StatusForbidden {
			t.Errorf("anahtarsız %s: durum %d", u, code)
		}
	}
	// Origin'e RTMP ile izleme, edge dışındaki adreslerden hâlâ reddedilir.
	if rtmpPlay(channel.ID, tsLocation[strings.Index(tsLocation, "?"):]) == nil {
		t.Error("edge olmayan bir adres origin'den RTMP ile izleyebildi")
	}

	// 7. Askıya alınan izleyicinin edge'deki izlemesi kesilir.
	viewerPath := fmt.Sprintf("/api/tenant/viewers/%d", viewer.ID)
	owner.call("PATCH", viewerPath, map[string]any{"status": "suspended"}, nil, http.StatusOK)
	select {
	case <-ended:
	case <-time.After(25 * time.Second):
		t.Fatal("askıya alınan izleyicinin edge'deki izlemesi kesilmedi")
	}
	if code := status(playlistURL.String()); code != http.StatusForbidden {
		t.Fatalf("askıdaki izleyicinin edge'deki HLS isteği 403 almalı: %d", code)
	}
	owner.call("PATCH", viewerPath, map[string]any{"status": "active"}, nil, http.StatusOK)

	// 8. Edge durunca sağlık sinyali kesilir; başka sunucu yoksa 503, yerel sunucu dönünce izleyiciler ona gider.
	edgeCompose(t, env, "stop", "-t", "2")
	waitFor(t, "duran edge'in sağlıksız görünmesi", 40*time.Second, func() bool { return !edgeByID(admin, edge.ID).Healthy })
	waitFor(t, "sunucu kalmadığında 503", 15*time.Second, func() bool {
		code, _ := probe(t, playURL("ts"))
		return code == http.StatusServiceUnavailable
	})
	admin.call("PATCH", localPath, map[string]any{"enabled": true}, nil, http.StatusOK)
	waitFor(t, "izleyicilerin yerel sunucuya dönmesi", 15*time.Second, func() bool {
		code, _ := probe(t, playURL("ts"))
		return code == http.StatusFound
	})
	if hosts := redirectHosts(t, playURL("ts"), 30); hosts["localhost:"+edgePort] != 0 {
		t.Fatalf("duran edge'e yönlendirme yapıldı: %v", hosts)
	}
	if body, _ := fetch(playURL("ts"), 3*188); !isMPEGTS(body) {
		t.Fatal("edge durduktan sonra yerel sunucudan izlenemedi")
	}
}
