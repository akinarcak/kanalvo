#!/usr/bin/env bash
# Simülasyon ortamı: Kanalvo'yu gerçek sunucu, OBS ya da oynatıcı olmadan, bu makinede Docker
# Compose ile kurar ve uçtan uca testleri koşar (bkz. docs/simulasyon.md).
#
#   e2e/sim.sh                      # kur, bütün senaryoları koş, kaldır
#   e2e/sim.sh -run TestTenant      # go test'e ek bayrak verilebilir
#   SIM_KEEP=1 e2e/sim.sh           # bitince ortamı kaldırma (incelemek için)
#   e2e/sim.sh down                 # ortamı (ve kalan edge projesini) kaldır
#
# Ortam ayrı bir Compose projesinde ve ayrı portlarda kalkar; aynı makinede çalışan bir geliştirme
# ortamına ya da başka kapsayıcılara dokunmaz. Portlar ve proje adı ortam değişkeniyle değişir.
set -euo pipefail

cd "$(dirname "$0")/.."

export COMPOSE_PROJECT_NAME="${SIM_PROJECT:-kanalvo-sim}"
export KANALVO_API_PORT="${KANALVO_API_PORT:-18000}"
export KANALVO_PANEL_PORT="${KANALVO_PANEL_PORT:-18002}"
export KANALVO_TS_PORT="${KANALVO_TS_PORT:-18081}"
export KANALVO_RTMP_PORT="${KANALVO_RTMP_PORT:-11935}"
export KANALVO_PG_PORT="${KANALVO_PG_PORT:-15432}"
export KANALVO_HTTP_PORT="${KANALVO_HTTP_PORT:-18880}"
export KANALVO_HTTPS_PORT="${KANALVO_HTTPS_PORT:-18443}"
export EDGE_PORT="${EDGE_PORT:-18090}"
# İzleyiciye ve yayıncıya verilen adresler portlara göre üretilir (.env'deki değerlerin yerine geçer).
export PUBLIC_BASE_URL="http://localhost:${KANALVO_API_PORT}"
export EDGE_TS_BASE_URL="http://localhost:${KANALVO_TS_PORT}"
export EDGE_HLS_BASE_URL="http://localhost:${KANALVO_API_PORT}"
export INGEST_BASE_URL="rtmp://localhost:${KANALVO_RTMP_PORT}/live"
# Panel, Caddy'nin arkasında HTTPS ile (iç CA) sunulur; .env eski bir örnekten kopyalandıysa bile
# çerez Secure kalır ve istemci adresi Caddy'nin X-Forwarded-For başlığından okunur.
export PANEL_DOMAIN=localhost
export PANEL_INSECURE_COOKIE=false
export PANEL_TRUST_PROXY_HEADERS=true
# Hatalı giriş kilidi tasarımdaki eşikle (20) sınanır; .env'deki yerel değerin yerine geçer.
export LOGIN_MAX_FAILURES="${SIM_LOGIN_MAX_FAILURES:-20}"

edge_project="${COMPOSE_PROJECT_NAME}-edge"

down() {
	# Edge projesi testin içinde kaldırılır; yarıda kesilen bir koşudan kalmışsa burada kaldırılır.
	# Kapsayıcı ve ağ adları proje adıyla başlar; yalnız bu ortamın kapsayıcılarına dokunulur.
	docker ps -aq --filter "label=com.docker.compose.project=${edge_project}" | xargs -r docker rm -f >/dev/null
	docker network rm "${edge_project}_default" >/dev/null 2>&1 || true
	docker ps -aq --filter "name=^${COMPOSE_PROJECT_NAME}-e2e-" | xargs -r docker rm -f >/dev/null
	docker compose --profile e2e down -v --remove-orphans
}

if [[ "${1:-}" == "down" ]]; then
	down
	exit 0
fi

for tool in docker go ffprobe; do
	command -v "$tool" >/dev/null || { echo "sim: $tool bulunamadı" >&2; exit 1; }
done

# .env yoksa örnekten oluşturulur (yalnızca yerel geliştirme değerleri; depoya girmez).
[[ -f .env ]] || cp .env.example .env

if [[ "${SIM_KEEP:-}" != "1" ]]; then
	trap down EXIT
fi

docker compose --profile e2e pull --quiet --ignore-buildable ffmpeg
docker compose up -d --build --wait
go test -tags e2e -count=1 -v -timeout 30m ./e2e/ "$@"
