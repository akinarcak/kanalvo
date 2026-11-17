//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// srsRecoveryLimit: SRS'in yönetim API'si yeniden yanıt verdikten sonra yeniden bağlanan yayının
// kabul edilmesi için tanınan en uzun süre. Yeniden bağlanan yayıncı denemeler arasında 1 sn bekler;
// ölçülen kabul süreleri boş makinede 0,1-0,6 sn, bütün senaryolar art arda koşarken 2,4 sn'ye
// kadardır. Düzeltmeden önce kabul, eşitleme döngüsünün (15 sn aralık, 10 sn bekleme) temizlemesini
// bekliyordu: 13,6 ve 22,5 sn, 10 ve 17 ret (bkz. docs/simulasyon.md).
const srsRecoveryLimit = 5 * time.Second

// publishRetrying, OBS gibi bağlantı koptuğunda ya da yayın reddedildiğinde 1 sn bekleyip yeniden
// bağlanan bir sahte yayın başlatır.
func publishRetrying(t *testing.T, name, streamKey, size string) {
	t.Helper()
	ffmpeg := "ffmpeg -nostdin -nostats -v error -re -f lavfi -i testsrc=size=" + size + ":rate=25 -f lavfi -i sine=frequency=440 " +
		"-c:v libx264 -preset veryfast -tune zerolatency -g 50 -pix_fmt yuv420p -c:a aac -f flv 'rtmp://srs/live/" + streamKey + "'"
	compose(t, "--profile", "e2e", "run", "-d", "--rm", "--name", name, "--entrypoint", "sh", "ffmpeg",
		"-c", "while :; do "+ffmpeg+"; sleep 1; done")
	t.Cleanup(func() { stopContainer(name) })
}

// originAPI, origin SRS'in yönetim API'sini çağırır. API dışarıya açık değildir; istek API
// kapsayıcısının içinden yapılır.
func originAPI(path string) ([]byte, error) {
	return exec.Command("docker", "exec", project+"-api-1", "wget", "-qO-", "-T", "2", "http://srs:1985"+path).Output()
}

// originPublishers, origin'de yayını süren kanalları ve yayıncı bağlantılarının kimliklerini döner.
func originPublishers() (map[string]string, error) {
	out, err := originAPI("/api/v1/streams/?count=1000")
	if err != nil {
		return nil, err
	}
	var body struct {
		Streams []struct {
			Name    string `json:"name"`
			App     string `json:"app"`
			Publish struct {
				Active bool   `json:"active"`
				CID    string `json:"cid"`
			} `json:"publish"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return nil, err
	}
	pubs := map[string]string{}
	for _, s := range body.Streams {
		if s.App == "live" && s.Publish.Active {
			pubs[s.Name] = s.Publish.CID
		}
	}
	return pubs, nil
}

// hookLog, API'nin verilen andan sonra yayın sorgularıyla ilgili yazdığı satırları döner.
func hookLog(t *testing.T, since time.Time) string {
	t.Helper()
	return string(compose(t, "logs", "--no-color", "--no-log-prefix", "--since", since.UTC().Format(time.RFC3339Nano), "api"))
}

// publishRejections, API'nin verilen andan sonra kanal için yazdığı yayın reddi satırlarını döner.
func publishRejections(t *testing.T, since time.Time, channel int64) []string {
	t.Helper()
	out := hookLog(t, since)
	pattern := regexp.MustCompile(fmt.Sprintf(`hooks: kanal %d yayını reddedildi`, channel))
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if pattern.MatchString(line) {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

// SRS çöküp geri geldiğinde "yayın bitti" bildirimi gelmez ve kanallar veritabanında canlı kalır.
// Yeniden bağlanan yayıncı, eski yayın SRS'te gerçekten yoksa hemen kabul edilmeli; ne "kanal zaten
// yayında" ne de kota gerekçesiyle reddedilmeli. Aynı kanala gerçekten ikinci bir yayın ise yine
// reddedilir (tasarım §7).
func TestPublisherReconnectsAfterSRSRestart(t *testing.T) {
	admin := newAdmin(t, "srs-yeniden-yonetici")
	f := newTenant(t, admin, "srsyeniden", "320x180")
	var second struct {
		ID        int64  `json:"id"`
		StreamKey string `json:"stream_key"`
	}
	f.Owner.call("POST", "/api/tenant/channels", map[string]any{"name": "Kanal srsyeniden 2"}, &second, http.StatusCreated)
	// Kota iki canlı kanal: iki kanal da bayat "canlı" kalınca kota da dolu görünür.
	admin.call("PATCH", fmt.Sprintf("/api/admin/tenants/%d", f.ID),
		map[string]any{"quotas": map[string]int{"max_channels": 2, "max_viewers": 10, "max_connections": 10}}, nil, http.StatusOK)
	channels := []struct {
		ID  int64
		Key string
	}{{f.Channel.ID, f.Channel.StreamKey}, {second.ID, second.StreamKey}}

	for i, c := range channels {
		publishRetrying(t, fmt.Sprintf("%s-e2e-srsyeniden-%d", project, i), c.Key, "320x180")
	}
	var before map[string]string
	waitFor(t, "iki yayının origin'de görünmesi", 30*time.Second, func() bool {
		var err error
		before, err = originPublishers()
		return err == nil && before[fmt.Sprint(channels[0].ID)] != "" && before[fmt.Sprint(channels[1].ID)] != ""
	})
	f.waitLive(t)

	// SRS çöker (on_unpublish gönderemez) ve yeniden başlar.
	srs := project + "-srs-1"
	crashed := time.Now()
	if out, err := exec.Command("docker", "kill", srs).CombinedOutput(); err != nil {
		t.Fatalf("SRS öldürülemedi: %v %s", err, out)
	}
	if out, err := exec.Command("docker", "start", srs).CombinedOutput(); err != nil {
		t.Fatalf("SRS başlatılamadı: %v %s", err, out)
	}
	var up time.Time
	waitFor(t, "SRS'in yönetim API'sinin yanıt vermesi", 30*time.Second, func() bool {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := originAPI("/api/v1/versions"); err == nil {
				up = time.Now()
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	})

	accepted := map[int64]time.Duration{}
	var after map[string]string
	deadline := up.Add(40 * time.Second)
	for len(accepted) < len(channels) && time.Now().Before(deadline) {
		if pubs, err := originPublishers(); err == nil {
			after = pubs
			for _, c := range channels {
				if _, seen := accepted[c.ID]; !seen && pubs[fmt.Sprint(c.ID)] != "" {
					accepted[c.ID] = time.Since(up)
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Reddedilen her bağlantının sorgusu yayın kabulünden önce gelmiştir; loglar kısa bir süre
	// sonra okunur ki son satırlar da yazılmış olsun.
	time.Sleep(time.Second)
	for _, c := range channels {
		rejected := publishRejections(t, crashed, c.ID)
		latency, ok := accepted[c.ID]
		t.Logf("kanal %d: SRS'in API'si yanıt verdikten %s sonra yeniden kabul edildi (kabul=%v), %d ret", c.ID, latency.Round(100*time.Millisecond), ok, len(rejected))
		for _, line := range rejected {
			t.Logf("  %s", line)
		}
		if !ok {
			t.Errorf("kanal %d: yeniden bağlanan yayın 40 sn içinde kabul edilmedi", c.ID)
		} else if latency > srsRecoveryLimit {
			t.Errorf("kanal %d: yeniden kabul %s sürdü, sınır %s", c.ID, latency.Round(100*time.Millisecond), srsRecoveryLimit)
		}
		if len(rejected) > 0 {
			t.Errorf("kanal %d: SRS yeniden başladıktan sonra %d yayın reddi", c.ID, len(rejected))
		}
	}
	if t.Failed() {
		for _, line := range strings.Split(hookLog(t, crashed), "\n") {
			if strings.Contains(line, "hooks:") || strings.Contains(line, "reconcile:") {
				t.Logf("api: %s", strings.TrimSpace(line))
			}
		}
		return
	}
	f.waitLive(t)

	// Gerçekten süren yayına ikinci bağlantı yine reddedilir; süren yayın kesilmez.
	intruder := project + "-e2e-srsyeniden-ikinci"
	publish(t, intruder, channels[0].Key, "640x360")
	waitFor(t, "aynı kanala ikinci yayının reddedilmesi", 30*time.Second, func() bool { return !containerRunning(intruder) })
	pubs, err := originPublishers()
	if err != nil {
		t.Fatalf("origin yayın listesi: %v", err)
	}
	for _, c := range channels {
		if name := fmt.Sprint(c.ID); pubs[name] == "" || pubs[name] != after[name] {
			t.Fatalf("kanal %d: süren yayın değişmemeli (önce %q, sonra %q)", c.ID, after[name], pubs[name])
		}
	}
	if lines := publishRejections(t, up, channels[0].ID); len(lines) == 0 {
		t.Fatal("ikinci yayının reddi loga yazılmalı")
	}
}
