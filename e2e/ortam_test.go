//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Simülasyon ortamının adresleri. Varsayılanlar, depo kökünde "docker compose up" ile kalkan
// geliştirme ortamıdır. e2e/sim.sh ortamı ayrı bir Compose projesinde ve ayrı portlarda kurar ve bu
// değişkenleri testlere aktarır (bkz. docs/simulasyon.md).
var (
	project = envOr("COMPOSE_PROJECT_NAME", "kanalvo")
	apiPort = envOr("KANALVO_API_PORT", "8000")
	apiURL  = "http://localhost:" + apiPort                          // Xtream uçları ve HLS geçidi
	tsURL   = "http://localhost:" + envOr("KANALVO_TS_PORT", "8081") // kesintisiz .ts veren SRS
	// Yönetim paneli yalnızca Caddy'den, HTTPS ile sunulur (PANEL_DOMAIN=localhost, Caddy'nin iç CA'sı).
	panelURL = "https://localhost:" + envOr("KANALVO_HTTPS_PORT", "443")
	// Caddy'nin HTTP portu: ACME doğrulaması ve HTTPS'e yönlendirme.
	caddyHTTPURL = "http://localhost:" + envOr("KANALVO_HTTP_PORT", "80")
	// api'nin düz HTTP panel portu; yalnızca 127.0.0.1'e bağlı olmalı.
	panelPort = envOr("KANALVO_PANEL_PORT", "8002")
	originURL = "http://localhost:8080" // origin SRS'in HTTP portu; dışarıya açık olmamalı

	// Ana sunucunun Compose ağı; uzak edge "ikinci sunucu" olarak buna bağlanır.
	network = project + "_default"
	// Uzak edge, ana sunucudan ayrı bir Compose projesidir.
	edgeProject = project + "-edge"
	edgePort    = envOr("EDGE_PORT", "8090")
	edgeURL     = "http://localhost:" + edgePort // ikinci sunucunun izleyicilere açık adresi
	edgeSRS     = edgeProject + "-edge-srs-1"
	edgeNginx   = edgeProject + "-edge-nginx-1"

	pubName = project + "-e2e-pub"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envValue, bir ayarı önce ortamdan, yoksa ../.env dosyasından okur (Compose da bu sırayla okur).
func envValue(t *testing.T, key string) string {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
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

// publish, kanala FFmpeg kapsayıcısından OBS yerine sahte bir yayın (test deseni ve 440 Hz ses)
// başlatır ve test bitince durdurur. size, görüntünün boyutudur ("640x360"); farklı yayıncılara
// farklı boyut verilerek izlenen görüntünün hangi yayından geldiği doğrulanabilir.
func publish(t *testing.T, name, streamKey, size string) {
	t.Helper()
	compose(t, "--profile", "e2e", "run", "-d", "--rm", "--name", name, "ffmpeg",
		"-re", "-f", "lavfi", "-i", "testsrc=size="+size+":rate=25", "-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "flv", "rtmp://srs/live/"+streamKey)
	t.Cleanup(func() { stopContainer(name) })
}

func stopContainer(name string) { exec.Command("docker", "rm", "-f", name).Run() }
