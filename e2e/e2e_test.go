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
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	apiURL    = "http://localhost:8000" // Xtream uçları ve HLS geçidi
	tsURL     = "http://localhost:8081" // kesintisiz .ts veren SRS
	originURL = "http://localhost:8080" // origin SRS'in HTTP portu; dışarıya açık olmamalı
	pubName   = "sh-e2e-pub"
)

type seed struct {
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
	if err := json.Unmarshal(compose(t, "exec", "-T", "api", "streamhub", "seed-dev"), &s); err != nil {
		t.Fatalf("seed-dev çıktısı çözülemedi: %v", err)
	}

	for _, ext := range []string{"ts", "m3u8"} {
		if code, _ := probe(t, s.playURL(ext)); code != http.StatusNotFound {
			t.Fatalf("yayın yokken %s için 404 bekleniyordu, gelen %d", ext, code)
		}
	}

	compose(t, "--profile", "e2e", "run", "-d", "--rm", "--name", pubName, "ffmpeg",
		"-re", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "flv",
		fmt.Sprintf("rtmp://srs/live/%d?secret=%s", s.ChannelID, s.StreamSecret))
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
}
