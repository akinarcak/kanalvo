//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// player, gerçek bir Xtream oynatıcısının (TiviMate, IPTV Smarters…) yerine geçer: oynatıcıların
// kullandığı uçları aynı sırayla çağırır ve listedeki adresleri ffprobe ile açıp görüntü karesi
// çözer. Yalnızca sunucu adresini, kullanıcı adını ve şifreyi bilir.
type player struct {
	t                  *testing.T
	server             string
	username, password string
}

type liveCategory struct {
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
}

type liveStream struct {
	StreamID   int64  `json:"stream_id"`
	Name       string `json:"name"`
	CategoryID string `json:"category_id"`
}

// m3uEntry, M3U listesindeki bir kanaldır.
type m3uEntry struct {
	Name string
	URL  string
}

func newPlayer(t *testing.T, username, password string) *player {
	return &player{t: t, server: apiURL, username: username, password: password}
}

func (p *player) api(action string, v any) {
	p.t.Helper()
	q := url.Values{"username": {p.username}, "password": {p.password}}
	if action != "" {
		q.Set("action", action)
	}
	getJSON(p.t, p.server+"/player_api.php?"+q.Encode(), v)
}

// login, oynatıcının ilk isteğidir: hesabın ve sunucunun bilgisini alır.
func (p *player) login() xtreamAccount {
	p.t.Helper()
	var a xtreamAccount
	p.api("", &a)
	return a
}

func (p *player) categories() []liveCategory {
	p.t.Helper()
	var c []liveCategory
	p.api("get_live_categories", &c)
	return c
}

func (p *player) streams() []liveStream {
	p.t.Helper()
	var s []liveStream
	p.api("get_live_streams", &s)
	return s
}

// playlist, get.php'den M3U listesini alır; output "ts" ya da "m3u8" olur.
func (p *player) playlist(output string) []m3uEntry {
	p.t.Helper()
	q := url.Values{"username": {p.username}, "password": {p.password}, "type": {"m3u_plus"}, "output": {output}}
	resp, err := noRedirect.Get(p.server + "/get.php?" + q.Encode())
	if err != nil {
		p.t.Fatalf("get.php: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.t.Fatalf("get.php: durum %d", resp.StatusCode)
	}
	var entries []m3uEntry
	sc := bufio.NewScanner(resp.Body)
	first, name := true, ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case first && line != "#EXTM3U":
			p.t.Fatalf("get.php M3U başlığıyla başlamıyor: %q", line)
		case strings.HasPrefix(line, "#EXTINF:"):
			name = line[strings.LastIndex(line, ",")+1:]
		case line != "" && !strings.HasPrefix(line, "#"):
			entries = append(entries, m3uEntry{Name: name, URL: line})
		}
		first = false
	}
	return entries
}

// streamURL, oynatıcının get_live_streams yanıtından kurduğu izleme adresidir.
func (p *player) streamURL(id int64, ext string) string {
	return fmt.Sprintf("%s/live/%s/%s/%d.%s", p.server, p.username, p.password, id, ext)
}

// videoProbe, ffprobe'un bir yayından çözdüğü görüntü karelerinin özetidir.
type videoProbe struct {
	Frames        int
	Width, Height int
	KeyFrames     int
	Err           string
}

// probeVideo, adresi ffprobe ile açar (yönlendirmeleri ve HLS'in göreli parça adreslerini ffprobe
// kendisi izler) ve ilk görüntü karelerini çözer. Kare çözülemezse Frames 0 olur.
func probeVideo(u string, frames int) videoProbe {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-rw_timeout", "10000000",
		"-select_streams", "v:0", "-read_intervals", fmt.Sprintf("%%+#%d", frames),
		"-show_frames", "-show_entries", "frame=width,height,key_frame", "-of", "json", u)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var out struct {
		Frames []struct {
			Width    int `json:"width"`
			Height   int `json:"height"`
			KeyFrame int `json:"key_frame"`
		} `json:"frames"`
	}
	_ = json.Unmarshal(stdout.Bytes(), &out)
	r := videoProbe{Frames: len(out.Frames)}
	for _, f := range out.Frames {
		r.Width, r.Height = f.Width, f.Height
		r.KeyFrames += f.KeyFrame
	}
	if err != nil {
		r.Err = strings.TrimSpace(err.Error() + " " + stderr.String())
	}
	return r
}

func (v videoProbe) size() string { return fmt.Sprintf("%dx%d", v.Width, v.Height) }

// waitForVideo, adres en az frames kare ve beklenen boyutta görüntü verene kadar dener.
func waitForVideo(t *testing.T, what, u, size string, frames int) videoProbe {
	t.Helper()
	var last videoProbe
	waitFor(t, what, 60*time.Second, func() bool {
		last = probeVideo(u, frames)
		return last.Frames >= frames && last.size() == size
	})
	return last
}

// requireFFprobe, ffprobe bulunmayan makinede görüntü doğrulayan testleri atlar.
func requireFFprobe(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe bulunamadı; görüntü doğrulaması atlandı")
	}
}
