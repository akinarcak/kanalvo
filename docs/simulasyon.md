# Simülasyon ortamı

Kanalvo'yu gerçek sunucu, gerçek OBS ya da gerçek oynatıcı olmadan, tek makinede uçtan uca sınar.
Her şey Docker Compose ile kalkar; dış dünyadaki her parçanın yerini sahte bir bileşen alır.

| Gerçek dünyada | Simülasyonda |
|---|---|
| Yayıncının OBS'i | FFmpeg kapsayıcısı (`ffmpeg` servisi, `e2e` profili): `testsrc` görüntü deseni + 440 Hz `sine` sesi, H.264/AAC, RTMP ile paneldeki adres ve anahtara yayın |
| İzleyicinin Xtream oynatıcısı (TiviMate, IPTV Smarters, VLC) | `e2e/oynatici_test.go` içindeki `player`: `player_api.php` (giriş, `get_live_categories`, `get_live_streams`), `get.php` (M3U, `output=ts` ve `m3u8`) ve `/live/…` adreslerini çağırır; listedeki adresleri **ffprobe** ile açar ve görüntü karelerini çözer |
| İkinci sunucu (uzak edge) | `deploy/edge` dosyalarıyla aynı makinede ayrı bir Compose projesi (`<proje>-edge`), ana sunucunun ağına bağlı |
| Ağ kopması | `docker network disconnect`: edge'in nginx'i ana sunucunun ağından çıkarılır |
| Panelin gerçek alan adı ve ACME sertifikası | `PANEL_DOMAIN=localhost`: Caddy sertifikayı kendi iç CA'sıyla üretir; testler bu CA'nın kök sertifikasını kapsayıcıdan (`/data/caddy/pki/authorities/local/root.crt`) alıp güvenir, doğrulama kapatılmaz |
| Panele başka bir makineden giren ikinci istemci | `caddy` kapsayıcısının içinden busybox `wget` ile Caddy'nin 443 portuna giriş; Caddy'ye bu makineden gelen istemciden farklı bir adresten ulaşır |
| Birden çok yayıncı | Panelden açılan iki yayıncı; her birinin sahte yayını farklı boyutta (640x360, 320x180), böylece izlenen görüntünün hangi yayıncıdan geldiği ffprobe ile ayırt edilir |

## Çalıştırma

Gerekenler: Docker (Compose v2), Go, ffprobe (FFmpeg paketiyle gelir).

```bash
e2e/sim.sh
```

Betik sırasıyla şunları yapar:

1. `.env` yoksa `.env.example`'dan oluşturur (yalnızca yerel değerler; `.env` depoya girmez).
2. Ortamı `kanalvo-sim` adlı ayrı bir Compose projesinde, ayrı portlarda kurar
   (`docker compose up -d --build --wait`).
3. `go test -tags e2e -count=1 -v ./e2e/` ile bütün senaryoları koşar.
4. Bitince (başarılı ya da değil) ortamı, kalan edge projesini ve sahte yayın kapsayıcılarını
   kaldırır (`docker compose down -v`). Makinedeki başka kapsayıcılara dokunmaz.

Koşu 7-8 dakika sürer; en uzun senaryo ağ kopmasıdır (tasarımdaki 2 dakikalık süreyi bekler).

Ek kullanım:

```bash
e2e/sim.sh -run TestTenantsAreIsolated   # go test bayrakları geçirilebilir
SIM_KEEP=1 e2e/sim.sh                    # bitince ortamı bırak (incelemek, tek test koşmak için)
e2e/sim.sh down                          # bırakılan ortamı kaldır
```

Ortam bırakıldıysa tek bir senaryo betiksiz de koşulabilir; testler adresleri aynı değişkenlerden okur:

```bash
export COMPOSE_PROJECT_NAME=kanalvo-sim KANALVO_API_PORT=18000 KANALVO_PANEL_PORT=18002 \
  KANALVO_TS_PORT=18081 KANALVO_RTMP_PORT=11935 KANALVO_PG_PORT=15432 EDGE_PORT=18090 \
  KANALVO_HTTP_PORT=18880 KANALVO_HTTPS_PORT=18443 LOGIN_MAX_FAILURES=20 \
  INGEST_BASE_URL=rtmp://localhost:11935/live
go test -tags e2e -count=1 -v -run TestEdgeDisableAndControlPartition ./e2e/
```

## Portlar ve proje adı

| Değişken | Anlamı | Simülasyon | Geliştirme (`docker compose up`) |
|---|---|---|---|
| `SIM_PROJECT` → `COMPOSE_PROJECT_NAME` | Compose proje adı; ağ `<proje>_default`, edge `<proje>-edge` olur | `kanalvo-sim` | `kanalvo` |
| `KANALVO_API_PORT` | Xtream uçları ve HLS geçidi | 18000 | 8000 |
| `KANALVO_PANEL_PORT` | Panelin düz HTTP portu (yalnızca 127.0.0.1) | 18002 | 8002 |
| `KANALVO_HTTPS_PORT` | Panel (Caddy, HTTPS) | 18443 | 443 |
| `KANALVO_HTTP_PORT` | Caddy'nin HTTP portu (HTTPS'e yönlendirir) | 18880 | 80 |
| `KANALVO_TS_PORT` | Kesintisiz `.ts` (`srs-ts`) | 18081 | 8081 |
| `KANALVO_RTMP_PORT` | RTMP yayın girişi | 11935 | 1935 |
| `KANALVO_PG_PORT` | PostgreSQL (yalnızca 127.0.0.1) | 15432 | 5432 |
| `EDGE_PORT` | İkinci sunucunun izleyici portu | 18090 | 8090 |

Port çakışırsa değişkeni vererek değiştirin, ör. `KANALVO_API_PORT=28000 SIM_PROJECT=kanalvo-sim2 e2e/sim.sh`.
Betik `PUBLIC_BASE_URL`, `EDGE_TS_BASE_URL`, `EDGE_HLS_BASE_URL` ve `INGEST_BASE_URL` değerlerini
portlardan üretir; `.env`'deki değerlerin yerine geçer. Panel için `PANEL_DOMAIN=localhost`,
`PANEL_TRUST_PROXY_HEADERS=true`, `PANEL_INSECURE_COOKIE=false` ve tasarımdaki giriş eşiği
`LOGIN_MAX_FAILURES=20` verilir (eşik `SIM_LOGIN_MAX_FAILURES` ile değişir). Simülasyon, `docker compose up` ile çalışan
bir geliştirme ortamının yanında kalkabilir.

## Senaryolar

| Test | Dosya | Ne sınar |
|---|---|---|
| `TestSingleChannelEndToEnd` | `e2e/e2e_test.go` | Tek kanal: yayın yokken 404; sahte yayınla `.ts` ve HLS; Xtream girişi, kanal listesi, M3U ve kısa adres; yanlış şifre ve sahte imzaların reddi; bağlantı limiti (en eski izleme kesilir, `.ts` ve HLS); origin'den RTMP izlemenin reddi; SRS yönetim API'sinin ve sorgu uçlarının dışarıya kapalı olması; yayın bitince çevrimdışı; yayıncı askıya alınınca yayının kesilmesi ve `Disabled`; loglarda gizli değer olmaması |
| `TestEdgeDisableAndControlPartition` | `e2e/edge_kesinti_test.go` | Edge'den ffprobe ile `.ts` ve HLS görüntüsü; **devre dışı bırakma**: yeni izleyici yönlendirilmez, süren `.ts` ve HLS izlemeleri sürer, edge origin'den yeni kanal çekemez; yeniden etkinleştirme; **ağ kopması** (yalnızca denetim bağlantısı): edge yönlendirmeden çıkar, oturumları kısa kopuklukta korunur, 2 dakikayı aşınca silinir; bağlantı dönünce edge yeniden izleyici alır, kopuklukta süren izleme sayılmaz ve kesilmez |
| `TestSecondServerSharesTheViewers` | `e2e/edge_test.go` | İkinci sunucu: izleyicilerin iki sunucuya dağılması; kayıtsız çekme adresinin reddi; edge'den `.ts` ve HLS (önbellekten parça); oturuma izleyicinin adresinin yazılması; edge'in origin'den ilk izleyicide çekip son izleyicide bırakması; edge'de yetkisiz erişimlerin reddi; askıya alınan izleyicinin edge'deki izlemesinin kesilmesi; **edge kapanması**: sağlıksız görünme, başka sunucu yoksa 503, yerel sunucuya dönüş |
| `TestTenantsAreIsolated` | `e2e/kiraci_test.go` | İki yayıncı ve iki izleyici: her oynatıcı yalnızca kendi kategorisini, kanalını ve M3U satırını görür; `.ts` ve `.m3u8`'den ffprobe ile **kendi yayıncısının** görüntüsü (boyuta göre) gelir; başka yayıncının kanalı 404; imza başka kanala taşınamaz; panel listeleri, arama ve oturumlar yalnızca kendi kayıtlarını içerir; başka yayıncının kayıtlarında değiştirme/silme 404; istekteki `tenant_id` reddedilir; yayıncı yönetici uçlarını kullanamaz |
| `TestPanelCreatedChannelCanBeStreamedAndWatched` | `e2e/panel_test.go` | Panelden (HTTPS) açılan yayıncı, kanal ve izleyiciyle yayın ve izleme; paneldeki M3U adresi; silinen izleyicinin izlemesinin kesilmesi; askıya alınan yayıncının yayınının kesilmesi ve panele girememesi |
| `TestPanelLoginOverHTTPS` | `e2e/https_test.go` | Panelin sertifikası `localhost` için Caddy'nin iç CA'sından; HTTPS'ten giriş; çerez `Secure` ve `HttpOnly`; Caddy'nin HTTP portuna gelen giriş ve çerezli istek panele ulaşmadan HTTPS'e yönlenir |
| `TestPanelPortIsOnlyOnLoopback` | `e2e/https_test.go` | `api`'nin 8002'si `127.0.0.1`'e bağlı; makinenin dış IPv4 adresinden bağlantı kurulamaz, aynı adresten Caddy'nin HTTPS portuna kurulur |
| `TestLoginLockoutFollowsTheClientBehindTheProxy` | `e2e/https_test.go` | İkinci istemci (caddy kapsayıcısından) her istekte farklı uydurulmuş `X-Forwarded-For` ile 20 hatalı girişte 401, sonra 429 alır; bu makineden gelen istemci (uydurulmuş başlıkla da) kilitlenmez. Kilit vekilin değil istemcinin adresine uygulanır |

ffprobe bulunmazsa görüntü çözen iki test (`TestTenantsAreIsolated`, `TestEdgeDisableAndControlPartition`)
atlanır (`SKIP`); betik ffprobe yoksa hiç başlamaz.

## Görüntü doğrulaması

`probeVideo` (`e2e/oynatici_test.go`) adresi ffprobe'a verir; ffprobe yönlendirmeyi ve HLS çalma
listesindeki göreli parça adreslerini bir oynatıcı gibi kendisi izler:

```text
ffprobe -v error -rw_timeout 10000000 -select_streams v:0 -read_intervals %+#10 \
        -show_frames -show_entries frame=width,height,key_frame -of json <adres>
```

Test, en az 10 karenin çözülmesini ve boyutun o yayıncının sahte yayınıyla aynı olmasını bekler.
Yalnızca MPEG-TS eşitleme baytlarına bakan eski denetim (`isMPEGTS`) hızlı ret kontrolleri için durur.

## Sınırlar

- Gerçek OBS ve gerçek oynatıcılarla deneme (tasarım §8 "Elle") bu ortamın yerine geçmez; bkz.
  `docs/ilk-surum-denetimi.md`.
- İki "sunucu" aynı Docker sunucusunda çalışır; gerçek ağ gecikmesi ve paket kaybı sınanmaz.
- Panelin HTTPS'i Caddy'nin iç CA'sıyla sınanır; gerçek alan adıyla ACME sertifikası alınması sınanmaz.
