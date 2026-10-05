# StreamHub

OBS ile yayın açılan, Xtream uyumlu oynatıcılardan izlenen çok kiracılı canlı yayın platformu.

- Tasarım: `docs/superpowers/specs/2026-10-05-streamhub-design.md`
- SRS'in ölçülen davranışları: `docs/srs-findings.md`

## Bileşenler

| Servis | Görevi | Dış port |
|---|---|---|
| `api` | Xtream yayın adresleri, HLS geçidi, SRS yetki sorguları | 8000 |
| `srs` | OBS'ten RTMP yayını alır, HLS üretir | 1935 |
| `srs-ts` | Kesintisiz MPEG-TS (`.ts`) dağıtır | 8081 |
| `postgres` | Veritabanı | yalnızca 127.0.0.1:5432 |

`srs` servisinin HTTP portu bilerek dışarıya açılmaz: SRS, HLS parçalarını denetimsiz sunar.
HLS izleyiciye yalnızca `api` içindeki `/hls/<imza>/…` geçidinden verilir.

## Yerelde çalıştırma

Docker ve Docker Compose v2 gerekir.

```bash
cp .env.example .env
docker compose up -d --build --wait
docker compose exec -T api streamhub seed-dev
```

`seed-dev` bir deneme yayıncısı, kanalı ve izleyicisi oluşturur ve değerlerini yazar:

- OBS → Ayarlar → Yayın → Özel: sunucu `rtmp://localhost/live`, yayın anahtarı `<channel_id>?secret=<stream_secret>`
- İzleme: `http://localhost:8000/live/<username>/<password>/<channel_id>.ts` (veya `.m3u8`)

`.env.example` içindeki değerler yalnızca yerel geliştirme içindir; sunucuda `TOKEN_KEY` ve
`HOOK_SECRET` için yeni rastgele değerler üretin ve `EDGE_*` adreslerini dış alan adınıza göre ayarlayın.

## Testler

```bash
docker compose up -d postgres
go test -p 1 ./...
```

Paketler aynı test veritabanını paylaştığı için `-p 1` gereklidir.

Uçtan uca test (tüm servisler çalışırken; sahte yayını FFmpeg kapsayıcısı gönderir):

```bash
docker compose up -d --build --wait
go test -tags e2e -count=1 ./e2e/
```
