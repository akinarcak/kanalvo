//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

const panelURL = "http://localhost:8002"

// panelClient, panele giriş yapmış bir tarayıcıyı taklit eder.
type panelClient struct {
	t    *testing.T
	http *http.Client
}

func newPanelClient(t *testing.T) *panelClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &panelClient{t: t, http: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
}

// call, isteği gönderir, beklenen durum kodunu doğrular ve yanıtı out içine çözer.
func (c *panelClient) call(method, path string, body, out any, want int) {
	c.t.Helper()
	var payload io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, panelURL+path, payload)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-StreamHub-Panel", "1")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: durum %d, beklenen %d", method, path, resp.StatusCode, want)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: JSON çözülemedi: %v", method, path, err)
		}
	}
}

func (c *panelClient) login(email, password string) {
	c.t.Helper()
	c.call("POST", "/api/login", map[string]string{"email": email, "password": password}, nil, http.StatusOK)
}

// Panelden oluşturulan yayıncı, kanal ve izleyici ile gerçek bir yayın açılıp izlenebilmeli.
func TestPanelCreatedChannelCanBeStreamedAndWatched(t *testing.T) {
	// Arayüz programın içine gömülü olarak sunulmalı.
	page, _ := fetch(panelURL+"/kanallar", 64<<10)
	if !strings.Contains(string(page), `<div id="root">`) {
		t.Fatal("panel arayüzü sunulmuyor (imaj arayüz derlenmeden mi oluşturuldu?)")
	}
	if body, _ := fetch(apiURL+"/api/me", 1024); strings.Contains(string(body), "unauthenticated") {
		t.Fatal("panel API'si izleyicilere açık portta sunulmamalı")
	}

	stamp := time.Now().UnixNano()
	var created struct{ Email, Password string }
	if err := json.Unmarshal(compose(t, "exec", "-T", "api", "streamhub", "create-admin", fmt.Sprintf("yonetici-%d@example.com", stamp)), &created); err != nil {
		t.Fatalf("create-admin çıktısı çözülemedi: %v", err)
	}

	admin := newPanelClient(t)
	admin.login(created.Email, created.Password)
	var tenant struct {
		Tenant   struct{ ID int64 }
		Password string
	}
	tenantEmail := fmt.Sprintf("yayinci-%d@example.com", stamp)
	admin.call("POST", "/api/admin/tenants", map[string]string{"name": "Uçtan Uca Yayıncı", "email": tenantEmail}, &tenant, http.StatusCreated)

	owner := newPanelClient(t)
	owner.login(tenantEmail, tenant.Password)
	var channel struct {
		ID        int64  `json:"id"`
		IngestURL string `json:"ingest_url"`
		StreamKey string `json:"stream_key"`
		Live      bool   `json:"live"`
	}
	owner.call("POST", "/api/tenant/channels", map[string]any{"name": "Panel Kanalı"}, &channel, http.StatusCreated)
	var viewer struct {
		ID          int64  `json:"id"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		PlaylistURL string `json:"playlist_url"`
	}
	owner.call("POST", "/api/tenant/viewers", map[string]any{"username": fmt.Sprintf("izleyici%d", stamp%1000000)}, &viewer, http.StatusCreated)

	// OBS'in yapacağı gibi: paneldeki sunucu adresi ve yayın anahtarıyla yayın aç.
	if channel.IngestURL != "rtmp://localhost/live" {
		t.Fatalf("beklenmeyen yayın sunucusu: %s", channel.IngestURL)
	}
	const publisher = "sh-e2e-panel-pub"
	compose(t, "--profile", "e2e", "run", "-d", "--rm", "--name", publisher, "ffmpeg",
		"-re", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "flv", "rtmp://srs/live/"+channel.StreamKey)
	t.Cleanup(func() { stopContainer(publisher) })

	waitFor(t, "kanalın panelde yayında görünmesi", 30*time.Second, func() bool {
		var list []struct {
			ID   int64 `json:"id"`
			Live bool  `json:"live"`
		}
		owner.call("GET", "/api/tenant/channels", nil, &list, http.StatusOK)
		return len(list) == 1 && list[0].ID == channel.ID && list[0].Live
	})

	// İzleyici, paneldeki M3U adresinden kanalı bulur ve izler.
	m3u, _ := fetch(viewer.PlaylistURL, 64<<10)
	lines := strings.Fields(string(m3u))
	if !isPlaylist(m3u) || !strings.Contains(string(m3u), "Panel Kanalı") {
		t.Fatalf("M3U listesi panelde oluşturulan kanalı içermiyor")
	}
	streamURL := lines[len(lines)-1]
	waitFor(t, "panelden oluşturulan kanalın izlenmesi", 30*time.Second, func() bool {
		body, _ := fetch(streamURL, 3*188)
		return isMPEGTS(body)
	})
	watching, _ := watch(streamURL)
	select {
	case <-watching:
	case <-time.After(15 * time.Second):
		t.Fatal("izleme başlamadı")
	}
	waitFor(t, "izlemenin panelde görünmesi", 15*time.Second, func() bool {
		var sessions []struct{ Viewer, Channel string }
		owner.call("GET", "/api/tenant/sessions", nil, &sessions, http.StatusOK)
		return len(sessions) == 1 && sessions[0].Viewer == viewer.Username && sessions[0].Channel == "Panel Kanalı"
	})

	// İzleyici silinince süren izlemesi kesilir ve hesabı artık çalışmaz.
	owner.call("DELETE", fmt.Sprintf("/api/tenant/viewers/%d", viewer.ID), nil, nil, http.StatusNoContent)
	if code, _ := probe(t, streamURL); code != http.StatusForbidden {
		t.Fatalf("silinen izleyici 403 almalı, gelen %d", code)
	}

	// Yönetici yayıncıyı askıya alınca yayın kesilir ve yayıncı panele giremez.
	admin.call("PATCH", fmt.Sprintf("/api/admin/tenants/%d", tenant.Tenant.ID), map[string]string{"status": "suspended"}, nil, http.StatusOK)
	waitFor(t, "askıdaki yayıncının yayınının kesilmesi", 30*time.Second, func() bool { return !containerRunning(publisher) })
	owner.call("GET", "/api/tenant/channels", nil, nil, http.StatusUnauthorized)
	newPanelClient(t).call("POST", "/api/login", map[string]string{"email": tenantEmail, "password": tenant.Password}, nil, http.StatusForbidden)
}
