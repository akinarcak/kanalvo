# Kanalvo Plan 2 — Xtream Uçları

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Bir IPTV oynatıcısının sunucu adresi + kullanıcı adı + şifre ile giriş yapıp kanal listesini alması ve kanalı izlemesi.

**Architecture:** Plan 1'deki `/live/...` yönlendirmesinin önüne Xtream Codes uyumlu uçlar eklenir. İzleyici doğrulaması tek bir pakette toplanır ve IP başına hatalı giriş sınırı uygular. Kategoriler veritabanına eklenir.

**Tech Stack:** Plan 1 ile aynı (Go 1.26, `net/http`, pgx v5, PostgreSQL 16).

**Spec:** `docs/superpowers/specs/2026-10-05-kanalvo-design.md` (bölüm 4, 5 "Xtream uçları", 6, 7)

## Global Constraints

- Plan 1'in tüm kısıtları geçerlidir (`go test -p 1 ./...`, gizli değerler loglanmaz, ek web çatısı yok).
- Bir izleyici yalnızca kendi yayıncısının kategorilerini ve kanallarını görür; her uç için ayrı test edilir.
- Yanıt alan adları ve türleri Xtream Codes oynatıcılarının beklediği biçimdedir (sayıların çoğu metin olarak gider).
- Dış adresler istekteki `Host` başlığından değil `PUBLIC_BASE_URL` ayarından üretilir.
- Her commit mesajı şu satırla biter: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`

## Review Focus

1. **Kategorisiz kanal:** Birçok oynatıcı kategorisi olmayan kanalı göstermez. Kategorisiz kanallar "Genel" adlı sanal kategoride (`category_id` `"0"`) listelenmeli.
2. **POST ile giriş:** Bazı oynatıcılar `player_api.php` parametrelerini gövdede gönderir; ikisi de çalışmalı.
3. **Kanal adında özel karakter:** Tırnak, virgül, satır sonu içeren ad M3U dosyasını bozmamalı.
4. **Hatalı giriş sınırı vekil arkasında:** Vekil başlıklarına güven kapalıyken `X-Forwarded-For` ile sınır atlatılamamalı; açıkken tüm izleyiciler tek IP sayılmamalı.
5. **Süresi dolmuş veya askıdaki izleyici:** Giriş yanıtı durumu bildirmeli (`Expired`, `Banned`, `Disabled`), liste uçları boş dönmeli, yayın adresi 403 vermeli.

## Uçlar

| Uç | Davranış |
|---|---|
| `GET/POST /player_api.php` (eylemsiz) | `user_info` + `server_info`. Bilinmeyen kullanıcı veya yanlış şifre: `{"user_info":{"auth":0}}` |
| `action=get_live_categories` | İzleyicinin yayıncısına ait kategoriler; kategorisiz kanal varsa sanal "Genel" |
| `action=get_live_streams` (`category_id` isteğe bağlı) | İzleyicinin yayıncısına ait kanallar |
| `action=get_vod_categories`, `get_vod_streams`, `get_series_categories`, `get_series` | `[]` |
| `action=get_short_epg`, `get_simple_data_table` | `{"epg_listings":[]}` |
| `GET /get.php` (`type=m3u` veya `m3u_plus`, `output=ts`, `m3u8` veya `hls`) | M3U çalma listesi |
| `GET /xmltv.php` | Boş program rehberi |
| `GET /{kullanıcı}/{şifre}/{kanal}` ve `/live/...` biçiminde uzantısız | `.ts` yönlendirmesi |

Spec'ten bilinçli sapma (spec buna göre güncellendi): süresi dolmuş veya askıdaki izleyici `auth: 0` yerine `auth: 1` ve durum bilgisi alır; gerçek Xtream panelleri böyle davranır ve oynatıcı izleyiciye anlamlı bir mesaj gösterir. Bu izleyici hiçbir kanalı listeleyemez ve izleyemez.

Spec'ten bilinçli sapma: EPG eylemleri spec'te "boş liste" olarak geçer; Xtream'in gerçek biçimi `{"epg_listings":[]}` nesnesidir ve oynatıcılar bunu bekler.

## Dosya yapısı

| Dosya | Sorumluluk |
|---|---|
| `internal/store/migrations/0002_categories.sql` | `categories` tablosu; `channels.category_id`, `channels.logo_url` |
| `internal/store/queries.go`, `models.go` | Kategori ve kanal listeleme sorguları, `CreatedAt` alanları |
| `internal/ratelimit/ratelimit.go` | Anahtar başına hatalı deneme sayacı |
| `internal/auth/auth.go` | İzleyici doğrulama: istemci IP'si, sınır, kullanıcı adı ve şifre denetimi |
| `internal/play/play.go` | `auth` kullanır; kısa adres biçimi ve uzantısız dosya adı |
| `internal/xtream/xtream.go` | `player_api.php`, `get.php`, `xmltv.php` |
| `internal/config/config.go` | `PUBLIC_BASE_URL`, `TRUST_PROXY_HEADERS`, `LOGIN_MAX_FAILURES`, `LOGIN_FAILURE_WINDOW` |
| `cmd/kanalvo/main.go` | Bağlama; `seed-dev` çıktısı değişmez |
| `e2e/e2e_test.go` | Giriş → kanal listesi → M3U → izleme akışı |

## Görevler

Her görev test-önce uygulanır: test yazılır, başarısız olduğu görülür, uygulama yazılır, tüm paketler `go test -p 1 ./...` ile geçer, commit edilir.

### Task 1: Kategoriler ve listeleme sorguları

- `0002_categories.sql`: `categories (id, tenant_id, name, position, created_at)`; `channels` tablosuna `category_id BIGINT NULL REFERENCES categories(id) ON DELETE SET NULL` ve `logo_url TEXT NOT NULL DEFAULT ''`.
- `store.Category{ID, TenantID int64; Name string; Position int}`.
- `Channel` ve `Viewer` türlerine `CreatedAt time.Time`; `Channel` türüne `CategoryID *int64`, `LogoURL string`.
- `CreateCategory(ctx, tenantID, name) (int64, error)`, `SetChannelCategory(ctx, channelID int64, categoryID *int64) error`, `CategoriesByTenant(ctx, tenantID) ([]Category, error)` (sıra: `position`, `id`), `ChannelsByTenant(ctx, tenantID) ([]Channel, error)` (sıra: `id`).
- `SetChannelCategory`, başka yayıncının kategorisini atamayı reddeder (`ErrNotFound`).
- Testler: listeler yalnızca istenen yayıncının kayıtlarını döner; kategori silinince kanal kategorisiz kalır; çapraz yayıncı ataması reddedilir.

### Task 2: Hatalı deneme sayacı

- `ratelimit.New(max int, window time.Duration, now func() time.Time) *Limiter`, `Blocked(key string) bool`, `Fail(key string)`.
- Bir anahtar, `window` içinde `max` hataya ulaşınca pencere bitene kadar engellenir; pencere bitince sayaç sıfırlanır.
- Süresi dolan kayıtlar temizlenir; kayıt sayısı sınırlıdır (bellek tüketimi saldırısına karşı).
- Testler: eşik, pencere sonu, anahtarların bağımsızlığı, eşzamanlı kullanım (`-race`), kayıt sayısı sınırı.

### Task 3: İzleyici doğrulama paketi

- `auth.New(s *store.Store, l *ratelimit.Limiter, trustProxyHeaders bool) *Authenticator`.
- `(*Authenticator).Viewer(r *http.Request, username, password string) (store.Viewer, error)`; hatalar `auth.ErrInvalid`, `auth.ErrRateLimited` veya veritabanı hatası. Dönen izleyici kullanılamaz durumda (askıda, süresi dolmuş) olabilir; karar çağırana aittir.
- İstemci IP'si: varsayılan olarak bağlantı adresi; `trustProxyHeaders` açıksa `X-Forwarded-For` başlığının son değeri.
- `ErrInvalid` her seferinde sayaca yazılır; engelli IP için veritabanına gidilmez.
- Geçersiz UTF-8 ve NUL içeren kullanıcı adı `ErrInvalid` sayılır (Plan 1'deki denetim buraya taşınır).
- `play` paketi bu doğrulayıcıyı kullanacak şekilde değiştirilir; engelli IP 429 alır.
- Testler: doğru giriş, yanlış şifre, olmayan kullanıcı, eşik sonrası 429 (doğru şifreyle bile), başka IP'nin etkilenmemesi, başlık güveni kapalıyken sahte `X-Forwarded-For` ile atlatılamama, açıkken başlığın kullanılması.

### Task 4: Kısa adres biçimi

- `play` şu yolları da kaydeder: `GET /{username}/{password}/{file}`.
- Uzantısız dosya adı `.ts` sayılır (her iki yol biçiminde).
- Testler: `/u/p/1`, `/u/p/1.ts`, `/u/p/1.m3u8`, `/live/u/p/1`; `/hls/...` ve `/healthz` yolları etkilenmez.

### Task 5: Xtream uçları

- `xtream.New(s *store.Store, a *auth.Authenticator, publicBaseURL *url.URL, now func() time.Time) *Handler`, `Register(mux)`.
- `user_info`: `username`, `password`, `message` (`""`), `auth` (1), `status` (`Active`, `Expired`, `Banned`, `Disabled`), `exp_date` (unix metin veya `null`), `is_trial` (`"0"`), `active_cons` (`"0"`), `created_at` (unix metin), `max_connections` (metin), `allowed_output_formats` (`["m3u8","ts"]`).
- `server_info`: `url` (alan adı), `port`, `https_port`, `server_protocol`, `rtmp_port` (`"1935"`), `timezone` (`"UTC"`), `timestamp_now` (sayı), `time_now` (`"2006-01-02 15:04:05"`).
- `get_live_streams` kaydı: `num`, `name`, `stream_type` (`"live"`), `stream_id` (sayı), `stream_icon`, `epg_channel_id` (`null`), `added` (unix metin), `category_id` (metin), `category_ids` (sayı dizisi), `custom_sid` (`""`), `tv_archive` (0), `direct_source` (`""`), `tv_archive_duration` (0).
- `get.php`: `#EXTM3U`, her kanal için `#EXTINF:-1 tvg-id="" tvg-name="…" tvg-logo="…" group-title="…",Ad` ve yayın adresi (`<PUBLIC_BASE_URL>/live/<kullanıcı>/<şifre>/<kanal>.<ts|m3u8>`). `type=m3u` ise öznitelikler yazılmaz.
- Kullanılamaz izleyici: giriş yanıtı durumu bildirir, liste eylemleri `[]` döner, `get.php` ve `xmltv.php` 403 döner.
- Testler: Review Focus'taki beş madde, kiracı ayrımı (liste ve `category_id` süzgeci), her eylemin yanıt biçimi, engelli IP için 429.

### Task 6: Bağlama ve uçtan uca test

- Yapılandırma: `PUBLIC_BASE_URL` (zorunlu), `TRUST_PROXY_HEADERS` (varsayılan kapalı), `LOGIN_MAX_FAILURES` (varsayılan 20), `LOGIN_FAILURE_WINDOW` (varsayılan 5 dakika).
- `main.go`: sınırlayıcı, doğrulayıcı ve Xtream uçları bağlanır.
- `e2e`: giriş yanıtı `auth` 1 → `get_live_streams` kanalı içerir → `get.php` içindeki adres oynar → kısa adres yönlendirir → yanlış şifre `auth` 0.
- README: oynatıcıya girilecek sunucu adresi, kullanıcı adı ve şifre.
