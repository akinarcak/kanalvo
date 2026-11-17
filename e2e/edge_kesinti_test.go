//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// edgeLocation, izleme adresini edge'e yönlendirilene kadar ister ve edge'deki adresi döner.
func edgeLocation(t *testing.T, playURL string) string {
	t.Helper()
	for i := 0; i < 60; i++ {
		code, loc := probe(t, playURL)
		if code != http.StatusFound {
			t.Fatalf("yönlendirme bekleniyordu, gelen %d", code)
		}
		if u, err := url.Parse(loc); err == nil && u.Host == "localhost:"+edgePort {
			return loc
		}
	}
	t.Fatal("60 istekte bir kez bile edge'e yönlendirilmedi")
	return ""
}

func sendsToEdge(t *testing.T, playURL string, n int) bool {
	return redirectHosts(t, playURL, n)["localhost:"+edgePort] > 0
}

func edgeSessions(t *testing.T, owner *panelClient, edgeName string) int {
	n := 0
	for _, s := range sessionsOf(t, owner) {
		if s.Edge == edgeName {
			n++
		}
	}
	return n
}

func stillWatching(t *testing.T, what string, ended <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-ended:
		t.Fatalf("%s: süren izleme kesildi", what)
	case <-time.After(d):
	}
}

// Devre dışı bırakılan edge yeni izleyici almaz, süren izlemeleri sürer ve origin'den yeni yayın
// çekemez. Yalnızca denetim bağlantısı koparsa (ana sunucu edge'e ulaşamaz, edge yayını almaya
// devam eder) edge yönlendirmeden çıkar; kopukluk 2 dakikayı aşınca oturumları silinir, böylece
// yayıncının kotası dolmaz; bağlantı dönünce edge yeniden izleyici alır (§7).
func TestEdgeDisableAndControlPartition(t *testing.T) {
	requireFFprobe(t)
	admin := newAdmin(t, "edge-kesinti-yonetici")
	f := newTenant(t, admin, "kesinti", "320x180")
	var second struct {
		ID        int64  `json:"id"`
		StreamKey string `json:"stream_key"`
	}
	f.Owner.call("POST", "/api/tenant/channels", map[string]any{"name": "Kanal kesinti 2"}, &second, http.StatusCreated)

	edge, edgePath, _ := startRemoteEdge(t, admin, "")
	waitFor(t, "edge'in sağlık sinyali vermesi", 40*time.Second, func() bool { return edgeByID(admin, edge.ID).Healthy })
	publish(t, project+"-e2e-kesinti-pub", f.Channel.StreamKey, f.Size)
	publish(t, project+"-e2e-kesinti-pub2", second.StreamKey, "640x360")
	f.waitLive(t)
	p := f.player(t)
	waitFor(t, "ikinci kanalın yayında görünmesi", 30*time.Second, func() bool {
		code, _ := probe(t, p.streamURL(second.ID, "ts"))
		return code == http.StatusFound
	})

	// Edge'de süren bir .ts izlemesi ve bir HLS adresi.
	tsLoc := edgeLocation(t, p.streamURL(f.Channel.ID, "ts"))
	waitForVideo(t, "edge'den .ts görüntüsü", tsLoc, f.Size, 10)
	started, ended := watch(tsLoc)
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("edge'de izleme başlamadı")
	}
	hlsLoc := edgeLocation(t, p.streamURL(f.Channel.ID, "m3u8"))
	waitForVideo(t, "edge'den HLS görüntüsü", hlsLoc, f.Size, 10)
	waitFor(t, "edge'deki izlemenin panelde görünmesi", 15*time.Second, func() bool { return edgeSessions(t, f.Owner, edge.Name) > 0 })

	// 1. Devre dışı bırakma.
	admin.call("PATCH", edgePath, map[string]any{"enabled": false}, nil, http.StatusOK)
	waitFor(t, "devre dışı edge'e yönlendirmenin durması", 15*time.Second, func() bool {
		return !sendsToEdge(t, p.streamURL(f.Channel.ID, "ts"), 20) && !sendsToEdge(t, p.streamURL(f.Channel.ID, "m3u8"), 20)
	})
	stillWatching(t, "devre dışı edge", ended, 8*time.Second)
	if v := probeVideo(hlsLoc, 5); v.Frames < 5 {
		t.Fatalf("devre dışı edge'deki HLS izlemesi sürmeli: %+v", v)
	}
	// Edge'in henüz çekmediği ikinci kanal: edge'de geçerli bir imzayla bile görüntü gelmez, çünkü
	// origin devre dışı edge'in çekmesini reddeder.
	_, localLoc := probe(t, p.streamURL(second.ID, "ts"))
	token := localLoc[strings.Index(localLoc, "?token="):]
	direct := fmt.Sprintf("%s/live/%d.ts%s", edgeURL, second.ID, token)
	for i := 0; i < 2; i++ {
		if body, _ := fetch(direct, 3*188); isMPEGTS(body) {
			t.Fatal("devre dışı edge origin'den yeni yayın çekebildi")
		}
	}

	// Yeniden etkinleştirilen edge izleyici alır ve yeni kanalı çeker.
	admin.call("PATCH", edgePath, map[string]any{"enabled": true}, nil, http.StatusOK)
	waitFor(t, "edge'in yeniden izleyici alması", 15*time.Second, func() bool { return sendsToEdge(t, p.streamURL(f.Channel.ID, "ts"), 20) })
	waitForVideo(t, "yeniden etkin edge'den ikinci kanal", edgeLocation(t, p.streamURL(second.ID, "ts")), "640x360", 10)

	// 2. Denetim bağlantısının kopması: ana sunucu edge'in nginx'ine ulaşamaz; edge'in SRS'i
	// origin'e bağlı kalır, izleyiciler edge'e kendi portundan ulaşmaya devam eder.
	disconnect := exec.Command("docker", "network", "disconnect", network, edgeNginx)
	if out, err := disconnect.CombinedOutput(); err != nil {
		t.Fatalf("ağ kesilemedi: %v %s", err, out)
	}
	reconnect := func() error {
		return exec.Command("docker", "network", "connect", "--alias", "edge-nginx", network, edgeNginx).Run()
	}
	connected := false
	t.Cleanup(func() {
		if !connected {
			reconnect()
		}
	})
	cut := time.Now()
	waitFor(t, "kopuk edge'in sağlıksız görünmesi", 40*time.Second, func() bool { return !edgeByID(admin, edge.ID).Healthy })
	waitFor(t, "kopuk edge'e yönlendirmenin durması", 15*time.Second, func() bool { return !sendsToEdge(t, p.streamURL(f.Channel.ID, "ts"), 20) })
	t.Logf("edge %s sonra yönlendirmeden çıktı", time.Since(cut).Round(time.Second))
	if edgeSessions(t, f.Owner, edge.Name) == 0 {
		t.Fatal("kısa kopuklukta edge'in oturumları korunmalı")
	}
	waitFor(t, "2 dakikadır ulaşılamayan edge'in oturumlarının silinmesi", 3*time.Minute, func() bool { return edgeSessions(t, f.Owner, edge.Name) == 0 })
	// Süre son sağlık sinyalinden sayılır; o sinyal kopmadan en çok bir denetim aralığı (5 sn)
	// önce alınmıştır.
	gone := time.Since(cut)
	if gone < 2*time.Minute-10*time.Second {
		t.Fatalf("oturumlar 2 dakika dolmadan silindi: %s", gone)
	}
	t.Logf("oturumlar kopukluktan %s sonra silindi", gone.Round(time.Second))
	stillWatching(t, "kopuk edge", ended, time.Second)

	// Bağlantı dönünce edge yeniden izleyici alır. Kopukluk sırasında süren izleme sayılmaz ve
	// kesilmez (tasarımın bilinen sınırı).
	if err := reconnect(); err != nil {
		t.Fatalf("ağ yeniden bağlanamadı: %v", err)
	}
	connected = true
	waitFor(t, "edge'in yeniden sağlıklı görünmesi", 40*time.Second, func() bool { return edgeByID(admin, edge.ID).Healthy })
	waitFor(t, "edge'in yeniden izleyici alması", 15*time.Second, func() bool { return sendsToEdge(t, p.streamURL(f.Channel.ID, "ts"), 20) })
	stillWatching(t, "bağlantı dönen edge", ended, 8*time.Second)
	if n := edgeSessions(t, f.Owner, edge.Name); n != 0 {
		t.Fatalf("kopuklukta süren izleme bağlantı dönünce sayılmamalı (tasarım §7): %d oturum", n)
	}
	waitForVideo(t, "bağlantısı dönen edge'den yeni izleme", edgeLocation(t, p.streamURL(f.Channel.ID, "ts")), f.Size, 10)
}
