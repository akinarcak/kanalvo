# SRS bulguları

Tarih: 2026-10-05
SRS imajı: `ossrs/srs:5` (sürüm 5.0.225)
Ortam: `streamhub-dev` WSL dağıtımı, Docker 26.1.5, Compose 2.26.1
Sahte yayın: FFmpeg `testsrc` + `sine`, H.264 + AAC, `rtmp://srs/live/1?secret=abc`

## Sonuç

Spec'teki üç varsayımdan ikisi olduğu gibi tutmadı. Tasarım aşağıdaki iki
değişiklikle düzeltildi (spec bölüm 2, 3 ve 5 güncellendi):

1. **HLS ve kesintisiz `.ts` aynı SRS'te birlikte açılamıyor.** `.ts` çıkışı
   için ikinci bir SRS (`srs-ts`) çalışır; origin her yayını ona iletir
   (`forward`).
2. **HLS parçaları SRS'te korumasızdır.** `hls_ctx` yalnızca çalma listesini
   korur; `1-9.ts` gibi parçalar hiçbir parametre olmadan 200 döner ve adları
   tahmin edilebilir. Bu yüzden HLS, SRS'ten doğrudan sunulmaz: API içindeki
   bir geçit (`/hls/<imza>/<dosya>`) her isteği doğrular ve SRS'e aktarır.
   Origin'in HTTP portu dışarıya açılmaz, `hls_ctx` kapatılır.

## Gözlemler

| # | Varsayım | Beklenen | Gözlenen | Sonuç |
|---|---|---|---|---|
| 1 | Yayın anahtarındaki parametreler sorguya aktarılır | `"stream":"1"`, `"param":"?secret=abc"` | Aynen | Tuttu |
| 2 | Kesintisiz `.ts` çıkışı | TS verisi, `play` sorgusunda `token=t1` | HLS ile aynı sanal sunucuda SRS açılmıyor: `vhost.hls conflict with vhost.http-ts`. `hls_ts_file` veya `hls_path` değiştirmek çözmüyor. Ayrı SRS'te çalışıyor: ilk bayt ve 188. bayt `47`, `play` sorgusunda `"stream":"1"`, `"param":"?token=t1"` | Değişiklikle tuttu |
| 3 | HLS izlemesi için izleme sorgusu | `#EXTM3U`, `play` sorgusunda `token=t2` | Sorgu geliyor, ama `"stream":"1.m3u8"` (uzantılı). Yanıt yönlendirme değil, `?hls_ctx=…&token=t2` içeren bir üst çalma listesi | Tuttu, ama yetersiz (bkz. 9) |
| 4 | HLS izlemesi bitince bildirim | `stop` sorgusu | Son istekten yaklaşık 25-30 saniye sonra geliyor | Tuttu (artık kullanılmıyor) |
| 5 | Yayın listesi API'si | `streams[].name`, `app`, `publish.active` | Alanlar aynen. Adres sondaki `/` ile olmalı: `/api/v1/streams/?count=100`; `/` olmadan yönlendirme dönüyor | Tuttu (adres düzeltildi) |
| 6 | Yayın bitince bildirim | `unpublish`, aynı `client_id` | Aynen (`0454644q`) | Tuttu |
| 7 | Reddedilen yayın | FFmpeg çıkar | Kapsayıcı çıktı. Sorguyla reddedilen yayın için `unpublish` gelmedi | Tuttu |
| 8 | Reddedilen `.ts` izleme | Veri gelmez | Bağlantı veri gönderilmeden kapanıyor (HTTP durum kodu yok, 0 bayt). Yayın yokken de `play` sorgusu geliyor | Tuttu |
| 9 | (Yeni) HLS parçalarının korunması | Parça, bağlam olmadan verilmemeli | `/live/1-9.ts` parametresiz 200 dönüyor. Uydurma `hls_ctx` ile çalma listesi isteği `play` sorgusunu yeniden tetikliyor (parametrede imza yok) | Tutmadı, geçit eklendi |

## Kaydedilen sorgu gövdeleri

```text
POST /hooks/publish {"server_id":"vid-19a63yq","service_id":"3625h209","action":"on_publish","client_id":"0454644q","ip":"172.21.0.6","vhost":"__defaultVhost__","app":"live","tcUrl":"rtmp://srs:1935/live","stream":"1","param":"?secret=abc","stream_url":"/live/1","stream_id":"vid-12l8650"}
POST /hooks/play {"server_id":"vid-w5816co","service_id":"p99559o4","action":"on_play","client_id":"152822v3","ip":"172.21.0.1","vhost":"__defaultVhost__","app":"live","stream":"1","tcUrl":"rtmp://__defaultVhost__/live","param":"?token=t1","pageUrl":"","stream_url":"/live/1","stream_id":"vid-206008p"}
POST /hooks/stop {"server_id":"vid-w5816co","service_id":"p99559o4","action":"on_stop","client_id":"152822v3","ip":"172.21.0.1","vhost":"__defaultVhost__","app":"live","tcUrl":"rtmp://__defaultVhost__/live","stream":"1","param":"?token=t1","stream_url":"/live/1","stream_id":"vid-206008p"}
POST /hooks/unpublish {"server_id":"vid-19a63yq","service_id":"3625h209","action":"on_unpublish","client_id":"0454644q","ip":"172.21.0.6","vhost":"__defaultVhost__","app":"live","tcUrl":"rtmp://srs:1935/live","stream":"1","param":"?secret=abc","stream_url":"/live/1","stream_id":"vid-12l8650"}
```

Yayın listesi yanıtı (kısaltılmamış tek kayıt):

```json
{"code":0,"server":"vid-19a63yq","service":"3625h209","pid":"1","streams":[{"id":"vid-12l8650","name":"1","vhost":"vid-c2o7ju9","app":"live","tcUrl":"rtmp://srs:1935/live","url":"/live/1","live_ms":1791225637813,"clients":4,"frames":761,"send_bytes":85245,"recv_bytes":951036,"kbps":{"recv_30s":244,"send_30s":0},"publish":{"active":true,"cid":"0454644q"},"video":{"codec":"H264","profile":"High","level":"3","width":640,"height":360},"audio":{"codec":"AAC","sample_rate":44100,"channel":2,"profile":"LC"}}]}
```

HLS çalma listesi (`hls_ctx` kapalıyken beklenen biçim; parça adları görelidir):

```text
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-MEDIA-SEQUENCE:9
#EXT-X-TARGETDURATION:2
#EXTINF:2.000, no desc
1-9.ts
```

## Sonraki planlar için notlar

- **Plan 3 (oturum takibi):** `.ts` izlemeleri için SRS `play`/`stop` sorguları
  kullanılabilir. HLS izlemeleri artık geçitten geçtiği için oturum takibi
  geçitte yapılmalı (çalma listesi istekleri yaklaşık 2 saniyede bir gelir).
- **Plan 5 (edge):** `.ts` için uzak edge'de SRS edge kipi denenmeli; edge'in
  origin'den çekerken izleyicinin imzasını origin'e taşıyıp taşımadığı ve
  origin'in `play` sorgusunu nasıl etkilediği ölçülmeli. HLS için edge'de aynı
  geçit (origin'e önbellekli aktarım) çalışabilir.

## Uçtan uca doğrulama (2026-10-05)

Tüm servisler çalışırken, gerçek bir çözücüyle (FFmpeg 6.1 `ffprobe` ve `ffmpeg -f null`)
dış adresler üzerinden:

| Senaryo | Sonuç |
|---|---|
| `/live/<kullanıcı>/<şifre>/<kanal>.ts` | H.264 640x360 + AAC algılandı, 4 saniye hatasız çözüldü |
| `/live/<kullanıcı>/<şifre>/<kanal>.m3u8` | H.264 640x360 + AAC algılandı, 4 saniye hatasız çözüldü |
| Aynı kanala ikinci yayın | Reddedildi; ilk yayın sürdü |
| Yanlış gizli anahtarla yayın | Reddedildi |
| SRS öldürülüp yeniden başlatıldı | Kanal yaklaşık 30 saniye içinde çevrimdışı oldu, yeni yayın kabul edildi |

OBS ve masaüstü oynatıcıyla (VLC, TiviMate) elle doğrulama henüz yapılmadı.

## Loglar ve gizli değerler (2026-10-05, gözden geçirme sonrası)

| Durum | SRS loguna yazılan |
|---|---|
| `info` düzeyi (SRS varsayılanı), kabul edilen her sorgu | Sorgu adresi (`HOOK_SECRET` dahil) ve gövdesi (gizli yayın anahtarı, izleyici imzası) |
| `warn` düzeyi (şimdiki ayar), kabul edilen sorgu | Hiçbiri |
| `warn` düzeyi, reddedilen sorgu | Hata satırında sorgu adresi (`HOOK_SECRET` dahil) ve gövdesi (o istekte gönderilen `secret` veya `token`) |

Reddedilen sorgu satırı SRS içinden kapatılamıyor. Sonuçları:

- Yanlış anahtarla ya da sahte imzayla yapılan denemelerde loga düşen değer zaten geçersizdir.
- Doğru anahtarla gelen ama reddedilen yayın (kanal zaten yayında, yayıncı askıda) gizli yayın
  anahtarını SRS loguna yazar.
- `HOOK_SECRET` her rette SRS loguna yazılır. Sorgu ucu dışarıya açık olmadığı için (8001,
  yalnızca iç ağ) bu sır tek başına dışarıdan kullanılamaz.

Bu yüzden SRS kapsayıcılarının logları gizli veri sayılmalı: erişimi kısıtlanmalı ve üçüncü
taraf log servislerine gönderilmemelidir. API logları hiçbir gizli değer içermez.

## Bağlantı listesi ve bağlantı kesme (2026-10-05, Plan 3)

Hem origin hem `srs-ts` üzerinde ölçüldü:

| İstek | Gözlenen |
|---|---|
| `GET /api/v1/clients/?count=N` | `clients[]` dizisi; her kayıtta `id`, `ip`, `name` (kanal no), `type`, `publish` (yayıncı mı). HTTP ile `.ts` izleyen istemcinin türü `flv-play`; origin'in ilettiği yayın `srs-ts` üzerinde `flash-publish` olarak görünür |
| `DELETE /api/v1/clients/<id>`, `.ts` izleyicisi | `{"code":0}`; izleyicinin bağlantısı yaklaşık 2 saniye içinde koptu |
| `DELETE /api/v1/clients/<id>`, yayıncı (origin) | `{"code":0}`; FFmpeg çıktı, kanal çevrimdışı oldu ("yayın bitti" bildirimi geldi) |

Kimlik, "izleme başladı" sorgusundaki `client_id` ile aynıdır.
