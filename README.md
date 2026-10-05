# StreamHub

OBS ile yayın açılan, Xtream uyumlu oynatıcılardan izlenen çok kiracılı canlı yayın platformu.

- Tasarım: `docs/superpowers/specs/2026-10-05-streamhub-design.md`
- SRS'in ölçülen davranışları: `docs/srs-findings.md`

## Bileşenler

| Servis | Görevi | Dış port |
|---|---|---|
| `api` | Xtream yayın adresleri ve HLS geçidi (8000); SRS yetki sorguları (8001, yalnızca iç ağ) | 8000 |
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
- IPTV oynatıcısı (TiviMate, IPTV Smarters vb.) → "Xtream Codes" girişi: sunucu `http://localhost:8000`, kullanıcı adı `<username>`, şifre `<password>`
- M3U listesi: `http://localhost:8000/get.php?username=<username>&password=<password>&type=m3u_plus&output=ts`
- Doğrudan izleme: `http://localhost:8000/live/<username>/<password>/<channel_id>.ts` (veya `.m3u8`)

Telefondaki veya televizyondaki bir oynatıcıdan denemek için `.env` içindeki `PUBLIC_BASE_URL`,
`EDGE_TS_BASE_URL` ve `EDGE_HLS_BASE_URL` adreslerinde `localhost` yerine bilgisayarın ağ adresini yazın.

## Bağlantı limiti ve askıya alma

- Her izleyicinin bir bağlantı limiti vardır (`seed-dev` ile oluşturulan izleyicide 1). Limit doluyken
  yeni bir izleme başlarsa en eski izleme kesilir; böylece kanal değiştiren izleyici beklemez,
  hesabını paylaşanlar ise birbirini düşürür.
- Paylaşılan bir HLS adresi başka bir ağdan açılırsa ayrı bir bağlantı sayılır.
- Yayıncının toplam bağlantı kotası doluysa yeni izleyici reddedilir (kimse düşürülmez).
- Bir yayıncıyı askıya almak için:

  ```bash
  docker compose exec -T api streamhub set-tenant-status <tenant_id> suspended
  ```

  Süren yayınları ve izleyicilerinin bağlantıları birkaç saniye içinde kesilir. Geri almak için
  `active` yazın.

## Ayarlar

| Değişken | Anlamı | Varsayılan |
|---|---|---|
| `PUBLIC_BASE_URL` | Oynatıcıya girilen sunucu adresi; M3U adresleri bundan üretilir | zorunlu |
| `EDGE_TS_BASE_URL`, `EDGE_HLS_BASE_URL` | İzleyicinin yönlendirildiği `.ts` ve HLS adresleri | zorunlu |
| `LOGIN_MAX_FAILURES`, `LOGIN_FAILURE_WINDOW` | Bir IP bu sürede bu kadar hatalı giriş yaparsa süre bitene kadar 429 alır | 20, 5m |
| `TRUST_PROXY_HEADERS` | İstemci IP'sini `X-Forwarded-For` başlığının son değerinden alır. Yalnızca API bu başlığı yazan bir vekilin arkasındayken açın; aksi halde giriş sınırı atlatılabilir | false |
| `TOKEN_TTL`, `HLS_TOKEN_TTL` | `.ts` ve HLS imzalarının ömrü | 5m, 6h |

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
