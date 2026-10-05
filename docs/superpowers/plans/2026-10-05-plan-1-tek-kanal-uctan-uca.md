# StreamHub Plan 1 — Tek Kanal Uçtan Uca

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** OBS'ten (RTMP) gelen tek bir kanalın, gizli anahtar doğrulamasıyla kabul edilip `/live/<kullanıcı>/<şifre>/<kanal>.ts` ve `.m3u8` adreslerinden imzalı yönlendirmeyle izlenebilmesi.

**Architecture:** SRS yayını alır ve HLS + kesintisiz MPEG-TS üretir. Go ile yazılan kontrol API'si SRS'in yayın/izleme sorgularını yanıtlar ve izleyiciyi kısa ömürlü HMAC imzalı adresle SRS'e yönlendirir. Kalıcı durum PostgreSQL'dedir. Bu planda tek edge vardır ve adresi yapılandırmadan gelir.

**Tech Stack:** Go 1.26, `net/http` (standart kütüphane yönlendiricisi), `github.com/jackc/pgx/v5`, PostgreSQL 16, SRS 5 (`ossrs/srs:5`), Docker Compose.

**Spec:** `docs/superpowers/specs/2026-10-05-streamhub-design.md`

Bu plan, spec'in "Geliştirme sırası" bölümündeki 1. adımı kapsar. Sonraki adımlar ayrı planlardır ve bu planın Görev 1'deki bulgularına dayanır:

| Plan | Kapsam |
|---|---|
| 2 | Xtream uçları (`player_api.php`, `get.php`, `xmltv.php`, kısa adres biçimi), hatalı giriş sınırı |
| 3 | Yayıncı kotaları, oturum takibi, bağlantı limiti, askıya alınan yayıncının yayınını kesme, SRT girişi |
| 4 | Panel (yönetici ve yayıncı ekranları), panel girişi |
| 5 | Edge kaydı, sağlık sinyali, ağırlıklı yönlendirme |

## Global Constraints

- Go sürümü 1.26; `go.mod` içinde `go 1.26`. Modül adı `streamhub`.
- HTTP yönlendirmesi yalnızca standart `net/http.ServeMux` ile yapılır; ek web çatısı eklenmez.
- Tek veritabanı sürücüsü `github.com/jackc/pgx/v5`. Redis ve ORM yok.
- Testler gerçek PostgreSQL'e karşı çalışır; paketler aynı test veritabanını paylaştığı için komut her zaman `go test -p 1 ./...` biçimindedir.
- Yerelde Docker ve Docker Compose v2 gerekir (SRS yalnızca Linux kapsayıcısında çalışır).
- Gizli yayın anahtarı (`stream_secret`) izleyiciye giden hiçbir adreste veya yanıtta yer almaz.
- İzleyici şifresi, gizli yayın anahtarı, `TOKEN_KEY` ve `HOOK_SECRET` hiçbir log satırına yazılmaz. İstek yolunu loglayan ara katman eklenmez (yol, izleyici şifresini içerir).
- Yayın SRS içinde `live/<kanal no>` olarak adlandırılır; uygulama adı her zaman `live`'dır.
- SRS'e kabul yanıtı HTTP 200 + `{"code":0}`, ret yanıtı 200 dışı bir durum kodudur.
- Her commit mesajı şu satırla biter: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`

## Review Focus

1. **SRS yeniden başlar veya çöker, "yayın bitti" bildirimi gelmez:** Kanal sonsuza dek "yayında" kalmamalı ve yayıncı yeniden yayın açabilmelidir. → Görev 6 (`TestRunOnceClearsStaleLive`).
2. **Reddedilen ikinci yayıncının "yayın bitti" bildirimi:** SRS, reddettiği bağlantı için de bildirim gönderir; bu, süren yayını çevrimdışı göstermemelidir. → Görev 4 (`TestSecondPublisherRejectedAndCannotTakeChannelOffline`).
3. **Bozuk yayın adı veya parametreler:** Sayısal olmayan kanal adı, `live` dışı uygulama, eksik `secret`, bozuk JSON 500 yerine temiz ret üretmelidir. → Görev 4 (`TestPublishRejectsMalformedInput`).
4. **Başka kanal için üretilmiş imzalı adres:** A kanalının imzası B kanalında geçmemelidir. → Görev 4 (`TestPlayRejectsTokenForOtherChannel`).
5. **Beklenmeyen dosya adları ve kodlanmış karakterler:** `/live/u/p/1`, `1.mp4`, `abc.ts`, `0.ts` 404 dönmeli; `%40` içeren kullanıcı adı çalışmalıdır. → Görev 5 (`TestRejectsUnknownFileNames`, `TestEncodedUsername`).

## Dosya yapısı

| Dosya | Sorumluluk |
|---|---|
| `tools/hooklog/main.go` | SRS sorgularını ekrana yazan hata ayıklama aracı (Görev 1) |
| `deploy/srs/srs.conf.tmpl` | SRS yapılandırma şablonu |
| `deploy/postgres/init.sql` | Test veritabanını oluşturur |
| `docker-compose.yml`, `.env.example`, `Dockerfile` | Çalıştırma ortamı |
| `docs/srs-findings.md` | Görev 1'de gözlenen SRS davranışları |
| `internal/config/config.go` | Ortam değişkenlerinden yapılandırma |
| `internal/store/store.go` | Bağlantı ve şema göçleri |
| `internal/store/migrations/0001_init.sql` | İlk şema |
| `internal/store/models.go` | `Channel`, `Viewer` türleri |
| `internal/store/queries.go` | Sorgular |
| `internal/testdb/testdb.go` | Testler için temiz veritabanı |
| `internal/token/token.go` | İmzalı adres üretme ve doğrulama |
| `internal/hooks/hooks.go` | SRS yayın ve izleme sorguları |
| `internal/play/play.go` | `/live/...` yönlendirmesi |
| `internal/reconcile/reconcile.go` | SRS'teki gerçek yayınlarla kanal durumunu eşitler |
| `cmd/streamhub/main.go` | `serve` ve `seed-dev` komutları |
| `e2e/e2e_test.go` | Uçtan uca test |

---

### Task 1: Ortam ve SRS varsayımlarının doğrulanması

Bu görev bir kapıdır: spec'in 2. bölümündeki üç SRS varsayımı burada gerçek SRS'e karşı sınanır. Adım 8'deki ölçütlerden biri tutmazsa dur ve insan ortağa bildir; sonraki görevlere geçme.

**Files:**
- Create: `go.mod`, `.gitignore`, `.env.example`
- Create: `tools/hooklog/main.go`
- Create: `deploy/srs/srs.conf.tmpl`, `deploy/postgres/init.sql`, `docker-compose.yml`
- Create: `docs/srs-findings.md`

**Interfaces:**
- Consumes: yok.
- Produces: `docker compose` servisleri `postgres` (5432), `srs` (1935 RTMP, 8080 HTTP, 127.0.0.1:1985 API), `ffmpeg` (profil `e2e`). SRS sorgu adresleri `${HOOK_BASE}/publish`, `/unpublish`, `/play`, `/stop`. Test veritabanı `postgres://streamhub:streamhub@localhost:5432/streamhub_test?sslmode=disable`.

- [ ] **Step 1: Modülü ve yok sayılacak dosyaları oluştur**

```bash
cd /d/Projects/streamhub
go mod init streamhub
```

`.gitignore`:

```gitignore
.env
/bin/
*.bin
```

`.env.example` (yalnızca yerel geliştirme değerleri; sunucuda yenileri üretilir):

```dotenv
TOKEN_KEY=dev-token-key-0123456789abcdef0123456789
HOOK_SECRET=dev-hook-secret-0123456789
HOOK_BASE=http://api:8000/hooks/srs/dev-hook-secret-0123456789
EDGE_BASE_URL=http://localhost:8080
```

```bash
cp .env.example .env
```

- [ ] **Step 2: Sorgu kaydedici aracı yaz**

`tools/hooklog/main.go`:

```go
// hooklog, SRS'in gönderdiği sorguları ekrana yazar. Yalnızca hata ayıklama içindir.
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8099", "dinlenecek adres")
	reject := flag.Bool("reject", false, "tüm sorguları reddet")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.Printf("%s %s %s", r.Method, r.URL.Path, body)
		if *reject {
			http.Error(w, `{"code":403}`, http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"code":0}`))
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
```

- [ ] **Step 3: SRS yapılandırmasını yaz**

`deploy/srs/srs.conf.tmpl`:

```nginx
listen              1935;
max_connections     1000;
daemon              off;
srs_log_tank        console;

http_api {
    enabled         on;
    listen          1985;
}

http_server {
    enabled         on;
    listen          8080;
    dir             ./objs/nginx/html;
}

vhost __defaultVhost__ {
    hls {
        enabled         on;
        hls_fragment    2;
        hls_window      12;
        hls_ctx         on;
        hls_ts_ctx      on;
    }
    http_remux {
        enabled     on;
        mount       [vhost]/[app]/[stream].ts;
    }
    http_hooks {
        enabled         on;
        on_publish      __HOOK_BASE__/publish;
        on_unpublish    __HOOK_BASE__/unpublish;
        on_play         __HOOK_BASE__/play;
        on_stop         __HOOK_BASE__/stop;
    }
}
```

- [ ] **Step 4: Compose dosyasını yaz**

`deploy/postgres/init.sql`:

```sql
CREATE DATABASE streamhub_test OWNER streamhub;
```

`docker-compose.yml`:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: streamhub
      POSTGRES_PASSWORD: streamhub
      POSTGRES_DB: streamhub
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./deploy/postgres/init.sql:/docker-entrypoint-initdb.d/init.sql:ro
    ports:
      - "127.0.0.1:5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U streamhub -d streamhub"]
      interval: 2s
      timeout: 3s
      retries: 30

  srs:
    image: ossrs/srs:5
    environment:
      HOOK_BASE: ${HOOK_BASE}
    volumes:
      - ./deploy/srs/srs.conf.tmpl:/conf/srs.conf.tmpl:ro
    command:
      - sh
      - -c
      - sed "s|__HOOK_BASE__|$$HOOK_BASE|g" /conf/srs.conf.tmpl > /tmp/srs.conf && exec ./objs/srs -c /tmp/srs.conf
    extra_hosts:
      - "host.docker.internal:host-gateway"
    ports:
      - "1935:1935"
      - "8080:8080"
      - "127.0.0.1:1985:1985"

  ffmpeg:
    image: jrottenberg/ffmpeg:6.1-alpine
    profiles: ["e2e"]

volumes:
  pgdata:
```

`jrottenberg/ffmpeg:6.1-alpine` etiketi çekilemezse, giriş noktası `ffmpeg` olan ve FFmpeg 5 veya üstünü içeren başka bir imaj kullan ve bunu `docs/srs-findings.md` dosyasına yaz.

- [ ] **Step 5: Kaydediciyi ve SRS'i başlat**

Birinci terminal:

```bash
go run ./tools/hooklog
```

İkinci terminal:

```bash
HOOK_BASE=http://host.docker.internal:8099/hooks docker compose up -d postgres srs
```

Expected: `docker compose ps` çıktısında `postgres` "healthy", `srs` "running".

- [ ] **Step 6: Sahte yayın gönder ve gözlemle**

```bash
docker compose --profile e2e run -d --rm --name sh-probe ffmpeg -re -f lavfi -i testsrc=size=640x360:rate=25 -f lavfi -i sine=frequency=440 -c:v libx264 -preset veryfast -tune zerolatency -g 50 -pix_fmt yuv420p -c:a aac -f flv "rtmp://srs/live/1?secret=abc"
```

Sırayla çalıştır ve her birinin sonucunu not et:

```bash
curl -s -o ts.bin --max-time 5 "http://localhost:8080/live/1.ts?token=t1"; od -An -tx1 -N1 ts.bin
```

```bash
curl -sL "http://localhost:8080/live/1.m3u8?token=t2"
```

```bash
curl -s "http://127.0.0.1:1985/api/v1/streams?count=100"
```

Sonra yayını durdur ve en az 60 saniye bekleyip kaydedici çıktısını izle:

```bash
docker rm -f sh-probe
```

- [ ] **Step 7: Ret davranışını gözlemle**

Kaydediciyi durdur, ret kipinde yeniden başlat:

```bash
go run ./tools/hooklog -reject
```

Adım 6'daki yayın komutunu yeniden çalıştır, ardından:

```bash
docker ps --filter name=sh-probe --format '{{.Status}}'
```

```bash
curl -s -o /dev/null -w '%{http_code}\n' --max-time 5 "http://localhost:8080/live/1.ts?token=t1"
```

- [ ] **Step 8: Bulguları yaz ve kapıyı değerlendir**

`docs/srs-findings.md` dosyasını aşağıdaki başlıklarla oluştur; "Gözlenen" sütununa adım 6 ve 7'de gerçekten gördüğünü yaz (kaydedici satırlarını aynen kopyala):

```markdown
# SRS bulguları

SRS imajı: ossrs/srs:5 (sürüm: `docker compose exec srs ./objs/srs -v` çıktısı)

| # | Varsayım | Beklenen | Gözlenen | Kapı |
|---|---|---|---|---|
| 1 | Yayın anahtarındaki parametreler sorguya aktarılır | `publish` sorgusunda `"stream":"1"` ve `"param":"?secret=abc"` | | Evet |
| 2 | Kesintisiz `.ts` çıkışı | `ts.bin` ilk baytı `47`; `play` sorgusunda `token=t1` | | Evet |
| 3 | HLS izlemesi için izleme sorgusu | Çalma listesi `#EXTM3U` ile başlar; `play` sorgusunda `token=t2` | | Evet |
| 4 | HLS izlemesi bitince bildirim | İstekler kesildikten sonra `stop` sorgusu (kaç saniye sonra geldiği) | | Hayır (Plan 3 için) |
| 5 | Yayın listesi API'si | `streams[].name == "1"`, `app == "live"`, `publish.active == true` | | Evet |
| 6 | Yayın bitince bildirim | `unpublish` sorgusu, `publish` ile aynı `client_id` | | Evet |
| 7 | Reddedilen yayın | Kapsayıcı çalışmıyor (ffmpeg çıkmış) | | Evet |
| 8 | Reddedilen izleme | TS verisi gelmez; dönen durum kodu | | Evet |
```

"Kapı: Evet" satırlarından herhangi biri beklenenden farklıysa DUR, farkı insan ortağa bildir.

- [ ] **Step 9: Temizle ve commit et**

```bash
docker rm -f sh-probe; rm -f ts.bin
git add go.mod .gitignore .env.example tools deploy docker-compose.yml docs/srs-findings.md
git commit -m "chore: scaffold environment and record SRS behaviour

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Yapılandırma ve veri katmanı

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`
- Create: `internal/store/store.go`, `internal/store/models.go`, `internal/store/queries.go`
- Create: `internal/store/migrations/0001_init.sql`
- Create: `internal/testdb/testdb.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: Görev 1'deki `postgres` servisi ve test veritabanı adresi.
- Produces:
  - `config.Config{HTTPAddr, DatabaseURL string; TokenKey []byte; HookSecret, EdgeBaseURL, SRSAPIURL string; TokenTTL time.Duration}`, `config.Load(getenv func(string) string) (Config, error)`
  - `store.Open(ctx, url string) (*Store, error)`, `(*Store).Close()`, `(*Store).Migrate(ctx) error`, `store.ErrNotFound`
  - `store.Channel{ID, TenantID int64; Name, StreamSecret string; Live bool; TenantStatus string}`
  - `store.Viewer{ID, TenantID int64; Username, Password, Status string; ExpiresAt *time.Time; MaxConnections int; TenantStatus string}`, `(Viewer).Usable(now time.Time) bool`
  - `CreateTenant(ctx, name string) (int64, error)`, `SetTenantStatus(ctx, id int64, status string) error`
  - `CreateChannel(ctx, tenantID int64, name, secret string) (int64, error)`, `ChannelByID(ctx, id int64) (Channel, error)`
  - `CreateViewer(ctx, tenantID int64, username, password string, maxConnections int) (int64, error)`, `ViewerByUsername(ctx, username string) (Viewer, error)`, `ViewerByID(ctx, id int64) (Viewer, error)`, `SetViewerStatus(ctx, id int64, status string) error`, `SetViewerExpiry(ctx, id int64, at *time.Time) error`
  - `MarkLive(ctx, channelID int64, clientID string) (bool, error)`, `MarkOffline(ctx, channelID int64, clientID string) error`, `ReconcileLive(ctx, activeIDs []int64, grace time.Duration) (int64, error)`
  - `testdb.New(t *testing.T) *store.Store` — göçleri uygulanmış, tabloları boşaltılmış depo.

- [ ] **Step 1: Yapılandırma testini yaz**

`internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func valid() map[string]string {
	return map[string]string{
		"DATABASE_URL":  "postgres://x",
		"TOKEN_KEY":     strings.Repeat("k", 32),
		"HOOK_SECRET":   strings.Repeat("h", 16),
		"EDGE_BASE_URL": "http://edge:8080/",
		"SRS_API_URL":   "http://srs:1985",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8000" || c.TokenTTL != 5*time.Minute {
		t.Fatalf("varsayılanlar yanlış: %+v", c)
	}
	if c.EdgeBaseURL != "http://edge:8080" {
		t.Fatalf("sondaki / silinmeli: %q", c.EdgeBaseURL)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]func(map[string]string){
		"eksik DATABASE_URL":  func(m map[string]string) { delete(m, "DATABASE_URL") },
		"eksik EDGE_BASE_URL": func(m map[string]string) { delete(m, "EDGE_BASE_URL") },
		"eksik SRS_API_URL":   func(m map[string]string) { delete(m, "SRS_API_URL") },
		"kısa TOKEN_KEY":      func(m map[string]string) { m["TOKEN_KEY"] = "short" },
		"kısa HOOK_SECRET":    func(m map[string]string) { m["HOOK_SECRET"] = "short" },
		"bozuk TOKEN_TTL":     func(m map[string]string) { m["TOKEN_TTL"] = "abc" },
		"negatif TOKEN_TTL":   func(m map[string]string) { m["TOKEN_TTL"] = "-1m" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			mutate(m)
			if _, err := Load(env(m)); err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}
```

- [ ] **Step 2: Testin başarısız olduğunu gör**

Run: `go test ./internal/config/`
Expected: FAIL, `undefined: Load`

- [ ] **Step 3: Yapılandırmayı yaz**

`internal/config/config.go`:

```go
package config

import (
	"fmt"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	TokenKey    []byte
	HookSecret  string
	EdgeBaseURL string
	SRSAPIURL   string
	TokenTTL    time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		HTTPAddr:    getenv("HTTP_ADDR"),
		DatabaseURL: getenv("DATABASE_URL"),
		TokenKey:    []byte(getenv("TOKEN_KEY")),
		HookSecret:  getenv("HOOK_SECRET"),
		EdgeBaseURL: strings.TrimRight(getenv("EDGE_BASE_URL"), "/"),
		SRSAPIURL:   strings.TrimRight(getenv("SRS_API_URL"), "/"),
		TokenTTL:    5 * time.Minute,
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8000"
	}
	if v := getenv("TOKEN_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("TOKEN_TTL geçersiz: %q", v)
		}
		c.TokenTTL = d
	}
	for name, v := range map[string]string{
		"DATABASE_URL":  c.DatabaseURL,
		"EDGE_BASE_URL": c.EdgeBaseURL,
		"SRS_API_URL":   c.SRSAPIURL,
	} {
		if v == "" {
			return Config{}, fmt.Errorf("%s boş olamaz", name)
		}
	}
	if len(c.TokenKey) < 32 {
		return Config{}, fmt.Errorf("TOKEN_KEY en az 32 bayt olmalı")
	}
	if len(c.HookSecret) < 16 {
		return Config{}, fmt.Errorf("HOOK_SECRET en az 16 karakter olmalı")
	}
	return c, nil
}
```

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 4: Şemayı yaz**

`internal/store/migrations/0001_init.sql`:

```sql
CREATE TABLE tenants (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE channels (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id           BIGINT NOT NULL REFERENCES tenants (id),
    name                TEXT NOT NULL,
    stream_secret       TEXT NOT NULL,
    live                BOOLEAN NOT NULL DEFAULT false,
    publisher_client_id TEXT,
    last_publish_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX channels_tenant_idx ON channels (tenant_id);

CREATE TABLE viewers (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       BIGINT NOT NULL REFERENCES tenants (id),
    username        TEXT NOT NULL UNIQUE,
    password        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    expires_at      TIMESTAMPTZ,
    max_connections INT NOT NULL DEFAULT 1 CHECK (max_connections > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX viewers_tenant_idx ON viewers (tenant_id);
```

- [ ] **Step 5: Depo testlerini yaz**

`internal/store/store_test.go`:

```go
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestChannelByID(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	cid := must(s.CreateChannel(ctx, tid, "Kanal", "sek"))

	ch := must(s.ChannelByID(ctx, cid))
	if ch.TenantID != tid || ch.Name != "Kanal" || ch.StreamSecret != "sek" || ch.Live || ch.TenantStatus != "active" {
		t.Fatalf("beklenmeyen kanal: %+v", ch)
	}
	if _, err := s.ChannelByID(ctx, cid+999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
}

func TestViewerLookupAndUniqueUsername(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	vid := must(s.CreateViewer(ctx, tid, "ali", "pw", 2))

	byName := must(s.ViewerByUsername(ctx, "ali"))
	byID := must(s.ViewerByID(ctx, vid))
	if byName.ID != vid || byID.Username != "ali" || byName.Password != "pw" || byName.MaxConnections != 2 {
		t.Fatalf("beklenmeyen izleyici: %+v / %+v", byName, byID)
	}
	if _, err := s.ViewerByUsername(ctx, "yok"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
	other := must(s.CreateTenant(ctx, "t2"))
	if _, err := s.CreateViewer(ctx, other, "ali", "pw2", 1); err == nil {
		t.Fatal("aynı kullanıcı adı ikinci kez oluşturulabilmemeli")
	}
}

func TestViewerUsable(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	cases := []struct {
		name string
		v    store.Viewer
		want bool
	}{
		{"aktif, süresiz", store.Viewer{Status: "active", TenantStatus: "active"}, true},
		{"aktif, süresi ileride", store.Viewer{Status: "active", TenantStatus: "active", ExpiresAt: &future}, true},
		{"süresi dolmuş", store.Viewer{Status: "active", TenantStatus: "active", ExpiresAt: &past}, false},
		{"süresi tam şimdi", store.Viewer{Status: "active", TenantStatus: "active", ExpiresAt: &now}, false},
		{"askıda izleyici", store.Viewer{Status: "suspended", TenantStatus: "active"}, false},
		{"askıda yayıncı", store.Viewer{Status: "active", TenantStatus: "suspended"}, false},
	}
	for _, c := range cases {
		if got := c.v.Usable(now); got != c.want {
			t.Errorf("%s: %v, beklenen %v", c.name, got, c.want)
		}
	}
}

func TestMarkLiveIsExclusive(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	cid := must(s.CreateChannel(ctx, tid, "c", "sek"))

	if !must(s.MarkLive(ctx, cid, "a")) {
		t.Fatal("ilk yayıncı kabul edilmeliydi")
	}
	if must(s.MarkLive(ctx, cid, "b")) {
		t.Fatal("ikinci yayıncı reddedilmeliydi")
	}
	if err := s.MarkOffline(ctx, cid, "b"); err != nil {
		t.Fatal(err)
	}
	if !must(s.ChannelByID(ctx, cid)).Live {
		t.Fatal("başka bağlantının bildirimi kanalı çevrimdışı yapmamalı")
	}
	if err := s.MarkOffline(ctx, cid, "a"); err != nil {
		t.Fatal(err)
	}
	if must(s.ChannelByID(ctx, cid)).Live {
		t.Fatal("yayıncının kendi bildirimi kanalı çevrimdışı yapmalı")
	}
}

func TestReconcileLive(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	stale := must(s.CreateChannel(ctx, tid, "eski", "s1"))
	active := must(s.CreateChannel(ctx, tid, "süren", "s2"))
	must(s.MarkLive(ctx, stale, "a"))
	must(s.MarkLive(ctx, active, "b"))

	if n := must(s.ReconcileLive(ctx, []int64{active}, time.Hour)); n != 0 {
		t.Fatalf("bekleme süresi içindeki kanala dokunulmamalı, temizlenen: %d", n)
	}
	if n := must(s.ReconcileLive(ctx, []int64{active}, 0)); n != 1 {
		t.Fatalf("1 kanal temizlenmeliydi, temizlenen: %d", n)
	}
	if must(s.ChannelByID(ctx, stale)).Live || !must(s.ChannelByID(ctx, active)).Live {
		t.Fatal("yalnızca SRS'te olmayan kanal çevrimdışı olmalı")
	}
	if !must(s.MarkLive(ctx, stale, "c")) {
		t.Fatal("temizlenen kanala yeniden yayın açılabilmeli")
	}
	if n := must(s.ReconcileLive(ctx, nil, 0)); n != 2 {
		t.Fatalf("boş listeyle 2 kanal temizlenmeliydi, temizlenen: %d", n)
	}
}

func TestStatusSetters(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	vid := must(s.CreateViewer(ctx, tid, "ali", "pw", 1))
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := s.SetViewerStatus(ctx, vid, "suspended"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewerExpiry(ctx, vid, &at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTenantStatus(ctx, tid, "suspended"); err != nil {
		t.Fatal(err)
	}
	v := must(s.ViewerByID(ctx, vid))
	if v.Status != "suspended" || v.TenantStatus != "suspended" || v.ExpiresAt == nil || !v.ExpiresAt.Equal(at) {
		t.Fatalf("beklenmeyen izleyici: %+v", v)
	}
}
```

- [ ] **Step 6: Testin başarısız olduğunu gör**

```bash
go get github.com/jackc/pgx/v5@latest
go test -p 1 ./internal/store/
```

Expected: FAIL, `package streamhub/internal/testdb is not in std` veya `undefined: store.ErrNotFound`

- [ ] **Step 7: Depoyu yaz**

`internal/store/store.go`:

```go
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var ErrNotFound = errors.New("store: kayıt bulunamadı")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate, migrations klasöründeki dosyaları ad sırasıyla ve yalnızca bir kez uygular.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := s.applyMigration(ctx, e.Name()); err != nil {
			return fmt.Errorf("göç %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name string) error {
	var applied bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&applied)
	if err != nil || applied {
		return err
	}
	sql, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
```

`internal/store/models.go`:

```go
package store

import "time"

type Channel struct {
	ID           int64
	TenantID     int64
	Name         string
	StreamSecret string
	Live         bool
	TenantStatus string
}

type Viewer struct {
	ID             int64
	TenantID       int64
	Username       string
	Password       string
	Status         string
	ExpiresAt      *time.Time
	MaxConnections int
	TenantStatus   string
}

// Usable, izleyicinin şu an yayın izleyip izleyemeyeceğini söyler.
func (v Viewer) Usable(now time.Time) bool {
	if v.Status != "active" || v.TenantStatus != "active" {
		return false
	}
	return v.ExpiresAt == nil || now.Before(*v.ExpiresAt)
}
```

`internal/store/queries.go`:

```go
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateTenant(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	return id, err
}

func (s *Store) SetTenantStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tenants SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s *Store) CreateChannel(ctx context.Context, tenantID int64, name, secret string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO channels (tenant_id, name, stream_secret) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, name, secret).Scan(&id)
	return id, err
}

func (s *Store) ChannelByID(ctx context.Context, id int64) (Channel, error) {
	var c Channel
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.tenant_id, c.name, c.stream_secret, c.live, t.status
		FROM channels c JOIN tenants t ON t.id = c.tenant_id
		WHERE c.id = $1`, id).
		Scan(&c.ID, &c.TenantID, &c.Name, &c.StreamSecret, &c.Live, &c.TenantStatus)
	return c, notFound(err)
}

func (s *Store) CreateViewer(ctx context.Context, tenantID int64, username, password string, maxConnections int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO viewers (tenant_id, username, password, max_connections) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, username, password, maxConnections).Scan(&id)
	return id, err
}

const viewerSelect = `
	SELECT v.id, v.tenant_id, v.username, v.password, v.status, v.expires_at, v.max_connections, t.status
	FROM viewers v JOIN tenants t ON t.id = v.tenant_id `

func (s *Store) ViewerByUsername(ctx context.Context, username string) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.username = $1`, username))
}

func (s *Store) ViewerByID(ctx context.Context, id int64) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.id = $1`, id))
}

func scanViewer(row pgx.Row) (Viewer, error) {
	var v Viewer
	err := row.Scan(&v.ID, &v.TenantID, &v.Username, &v.Password, &v.Status, &v.ExpiresAt, &v.MaxConnections, &v.TenantStatus)
	return v, notFound(err)
}

func (s *Store) SetViewerStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE viewers SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s *Store) SetViewerExpiry(ctx context.Context, id int64, at *time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE viewers SET expires_at = $2 WHERE id = $1`, id, at)
	return err
}

// MarkLive, kanal çevrimdışıysa yayında olarak işaretler. Kanal zaten yayındaysa false döner.
func (s *Store) MarkLive(ctx context.Context, channelID int64, clientID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE channels SET live = true, publisher_client_id = $2, last_publish_at = now()
		WHERE id = $1 AND NOT live`, channelID, clientID)
	return tag.RowsAffected() == 1, err
}

// MarkOffline, yalnızca yayını açan bağlantının bildirimiyle kanalı çevrimdışı yapar.
func (s *Store) MarkOffline(ctx context.Context, channelID int64, clientID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE channels SET live = false, publisher_client_id = NULL
		WHERE id = $1 AND publisher_client_id = $2`, channelID, clientID)
	return err
}

// ReconcileLive, yayında görünen ama activeIDs içinde olmayan kanalları çevrimdışı yapar.
// grace süresinden daha yeni başlamış yayınlara dokunmaz.
func (s *Store) ReconcileLive(ctx context.Context, activeIDs []int64, grace time.Duration) (int64, error) {
	if activeIDs == nil {
		activeIDs = []int64{}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE channels SET live = false, publisher_client_id = NULL
		WHERE live
		  AND last_publish_at <= now() - make_interval(secs => $2)
		  AND NOT (id = ANY($1))`, activeIDs, grace.Seconds())
	return tag.RowsAffected(), err
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
```

`internal/testdb/testdb.go`:

```go
// Package testdb, testlere göçleri uygulanmış ve boşaltılmış bir veritabanı verir.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"streamhub/internal/store"
)

const defaultURL = "postgres://streamhub:streamhub@localhost:5432/streamhub_test?sslmode=disable"

func New(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = defaultURL
	}
	ctx := context.Background()

	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("test veritabanına bağlanılamadı (docker compose up -d postgres çalışıyor mu?): %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("göçler uygulanamadı: %v", err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `TRUNCATE viewers, channels, tenants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("tablolar boşaltılamadı: %v", err)
	}
	return s
}
```

- [ ] **Step 8: Testlerin geçtiğini gör**

```bash
docker compose up -d postgres
go mod tidy
go test -p 1 ./...
```

Expected: `ok streamhub/internal/config`, `ok streamhub/internal/store`

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal
git commit -m "feat: add config loading and PostgreSQL store

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: İmzalı adres

**Files:**
- Create: `internal/token/token.go`
- Test: `internal/token/token_test.go`

**Interfaces:**
- Consumes: yok.
- Produces: `token.Claims{ViewerID, ChannelID int64; ExpiresAt time.Time}`, `token.NewSigner(key []byte) *Signer`, `(*Signer).Sign(c Claims) string`, `(*Signer).Verify(tok string, now time.Time) (Claims, error)`, `token.ErrInvalid`, `token.ErrExpired`. Üretilen değer yalnızca rakam, nokta ve base64url karakterleri içerir; adres parametresine kaçışsız yazılabilir.

- [ ] **Step 1: Testi yaz**

`internal/token/token_test.go`:

```go
package token

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	now    = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	signer = NewSigner([]byte(strings.Repeat("k", 32)))
)

func TestSignVerifyRoundTrip(t *testing.T) {
	in := Claims{ViewerID: 7, ChannelID: 42, ExpiresAt: now.Add(5 * time.Minute)}
	got, err := signer.Verify(signer.Sign(in), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ViewerID != 7 || got.ChannelID != 42 || !got.ExpiresAt.Equal(in.ExpiresAt) {
		t.Fatalf("beklenmeyen içerik: %+v", got)
	}
}

func TestTokenIsURLSafe(t *testing.T) {
	tok := signer.Sign(Claims{ViewerID: 1, ChannelID: 2, ExpiresAt: now})
	if strings.ContainsAny(tok, "+/=?&# ") {
		t.Fatalf("adres için güvenli olmayan karakter: %q", tok)
	}
}

func TestVerifyExpiry(t *testing.T) {
	tok := signer.Sign(Claims{ViewerID: 1, ChannelID: 2, ExpiresAt: now})
	if _, err := signer.Verify(tok, now.Add(-time.Second)); err != nil {
		t.Fatalf("süresi dolmamış imza kabul edilmeliydi: %v", err)
	}
	if _, err := signer.Verify(tok, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("tam bitiş anında ErrExpired bekleniyordu, gelen: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	tok := signer.Sign(Claims{ViewerID: 1, ChannelID: 2, ExpiresAt: now.Add(time.Minute)})
	other := NewSigner([]byte(strings.Repeat("x", 32)))
	bad := []string{
		"",
		"abc",
		"1.2.3",
		strings.Replace(tok, "1.2.", "1.3.", 1),
		tok + "A",
		other.Sign(Claims{ViewerID: 1, ChannelID: 2, ExpiresAt: now.Add(time.Minute)}),
	}
	for _, b := range bad {
		if _, err := signer.Verify(b, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q için ErrInvalid bekleniyordu, gelen: %v", b, err)
		}
	}
}
```

- [ ] **Step 2: Testin başarısız olduğunu gör**

Run: `go test ./internal/token/`
Expected: FAIL, `undefined: NewSigner`

- [ ] **Step 3: Uygulamayı yaz**

`internal/token/token.go`:

```go
// Package token, izleyiciyi edge'e yönlendiren kısa ömürlü imzalı değeri üretir ve doğrular.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid = errors.New("token: geçersiz")
	ErrExpired = errors.New("token: süresi dolmuş")
)

type Claims struct {
	ViewerID  int64
	ChannelID int64
	ExpiresAt time.Time
}

type Signer struct {
	key []byte
}

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

// Sign, "<izleyici>.<kanal>.<bitiş unix>.<imza>" biçiminde bir değer üretir.
func (s *Signer) Sign(c Claims) string {
	payload := fmt.Sprintf("%d.%d.%d", c.ViewerID, c.ChannelID, c.ExpiresAt.Unix())
	return payload + "." + s.mac(payload)
}

func (s *Signer) Verify(tok string, now time.Time) (Claims, error) {
	i := strings.LastIndexByte(tok, '.')
	if i < 0 {
		return Claims{}, ErrInvalid
	}
	payload, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(s.mac(payload))) {
		return Claims{}, ErrInvalid
	}
	parts := strings.Split(payload, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalid
	}
	var n [3]int64
	for j, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return Claims{}, ErrInvalid
		}
		n[j] = v
	}
	c := Claims{ViewerID: n[0], ChannelID: n[1], ExpiresAt: time.Unix(n[2], 0)}
	if !now.Before(c.ExpiresAt) {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func (s *Signer) mac(payload string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
```

- [ ] **Step 4: Testlerin geçtiğini gör**

Run: `go test ./internal/token/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/token
git commit -m "feat: add signed playback token

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: SRS sorguları

SRS her yayın ve izleme olayında API'ye JSON gönderir. Görev 1'de kaydedilen gövde biçimi `docs/srs-findings.md` dosyasındadır; aşağıdaki alan adları oradaki gözlemle uyuşmuyorsa dur ve bildir.

**Files:**
- Create: `internal/hooks/hooks.go`
- Test: `internal/hooks/hooks_test.go`

**Interfaces:**
- Consumes: `store.Store` yöntemleri `ChannelByID`, `ViewerByID`, `MarkLive`, `MarkOffline`; `token.Signer.Verify`; `Viewer.Usable`; `testdb.New`.
- Produces: `hooks.New(s *store.Store, signer *token.Signer, now func() time.Time) *Handler`, `(*Handler).Register(mux *http.ServeMux, secret string)`. Kaydedilen yol: `POST /hooks/srs/{secret}/{event}`; `event` değerleri `publish`, `unpublish`, `play`, `stop`. Kabul: 200 + `{"code":0}`. Ret: 403 (yetki), 400 (bozuk gövde), 404 (yanlış sır veya bilinmeyen olay), 500 (veritabanı hatası).

- [ ] **Step 1: Testleri yaz**

`internal/hooks/hooks_test.go`:

```go
package hooks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"streamhub/internal/hooks"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
	"streamhub/internal/token"
)

const hookSecret = "hook-secret-0123456789"

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t       *testing.T
	store   *store.Store
	signer  *token.Signer
	mux     *http.ServeMux
	tenant  int64
	channel int64
	viewer  int64
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func setup(t *testing.T) *fixture {
	ctx := context.Background()
	f := &fixture{t: t, store: testdb.New(t), signer: token.NewSigner([]byte(strings.Repeat("k", 32))), mux: http.NewServeMux()}
	f.tenant = must(f.store.CreateTenant(ctx, "t"))
	f.channel = must(f.store.CreateChannel(ctx, f.tenant, "c", "sek"))
	f.viewer = must(f.store.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	hooks.New(f.store, f.signer, func() time.Time { return now }).Register(f.mux, hookSecret)
	return f
}

func (f *fixture) post(secret, event, body string) int {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/srs/"+secret+"/"+event, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK && strings.TrimSpace(rec.Body.String()) != `{"code":0}` {
		f.t.Fatalf("kabul gövdesi beklenmedik: %q", rec.Body.String())
	}
	return rec.Code
}

func event(client, app, stream, param string) string {
	return fmt.Sprintf(`{"action":"x","client_id":%q,"ip":"1.2.3.4","vhost":"__defaultVhost__","app":%q,"stream":%q,"param":%q}`,
		client, app, stream, param)
}

func (f *fixture) publish(client, param string) int {
	return f.post(hookSecret, "publish", event(client, "live", fmt.Sprint(f.channel), param))
}

func (f *fixture) live() bool {
	return must(f.store.ChannelByID(context.Background(), f.channel)).Live
}

func (f *fixture) tokenFor(viewer, channel int64, exp time.Time) string {
	return "?token=" + f.signer.Sign(token.Claims{ViewerID: viewer, ChannelID: channel, ExpiresAt: exp})
}

func (f *fixture) play(stream, param string) int {
	return f.post(hookSecret, "play", event("p1", "live", stream, param))
}

func TestPublishAcceptsValidSecret(t *testing.T) {
	f := setup(t)
	if code := f.publish("a", "?secret=sek"); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
	if !f.live() {
		t.Fatal("kanal yayında olmalı")
	}
}

func TestPublishAcceptsParamWithoutQuestionMarkAndExtraParams(t *testing.T) {
	f := setup(t)
	if code := f.publish("a", "secret=sek&foo=bar"); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}

func TestPublishRejectsWrongSecret(t *testing.T) {
	f := setup(t)
	for _, param := range []string{"?secret=yanlis", "?secret=", "", "?other=sek"} {
		if code := f.publish("a", param); code != http.StatusForbidden {
			t.Errorf("param %q: durum %d", param, code)
		}
	}
	if f.live() {
		t.Fatal("kanal çevrimdışı kalmalı")
	}
}

func TestPublishRejectsMalformedInput(t *testing.T) {
	f := setup(t)
	ch := fmt.Sprint(f.channel)
	cases := map[string]struct {
		body string
		want int
	}{
		"sayısal olmayan kanal": {event("a", "live", "abc", "?secret=sek"), http.StatusForbidden},
		"uzantılı kanal adı":    {event("a", "live", ch+".ts", "?secret=sek"), http.StatusForbidden},
		"sıfır kanal":           {event("a", "live", "0", "?secret=sek"), http.StatusForbidden},
		"negatif kanal":         {event("a", "live", "-1", "?secret=sek"), http.StatusForbidden},
		"olmayan kanal":         {event("a", "live", "999999", "?secret=sek"), http.StatusForbidden},
		"yanlış uygulama":       {event("a", "other", ch, "?secret=sek"), http.StatusForbidden},
		"bozuk parametre":       {event("a", "live", ch, "?secret=%zz"), http.StatusForbidden},
		"bozuk JSON":            {`{"app":`, http.StatusBadRequest},
		"boş gövde":             {``, http.StatusBadRequest},
	}
	for name, c := range cases {
		if code := f.post(hookSecret, "publish", c.body); code != c.want {
			t.Errorf("%s: durum %d, beklenen %d", name, code, c.want)
		}
	}
	if f.live() {
		t.Fatal("kanal çevrimdışı kalmalı")
	}
}

func TestRejectsWrongHookSecretAndUnknownEvent(t *testing.T) {
	f := setup(t)
	body := event("a", "live", fmt.Sprint(f.channel), "?secret=sek")
	if code := f.post("yanlis-sir", "publish", body); code != http.StatusNotFound {
		t.Fatalf("yanlış sır: durum %d", code)
	}
	if code := f.post(hookSecret, "bilinmeyen", body); code != http.StatusNotFound {
		t.Fatalf("bilinmeyen olay: durum %d", code)
	}
	if f.live() {
		t.Fatal("kanal çevrimdışı kalmalı")
	}
}

func TestPublishRejectsSuspendedTenant(t *testing.T) {
	f := setup(t)
	if err := f.store.SetTenantStatus(context.Background(), f.tenant, "suspended"); err != nil {
		t.Fatal(err)
	}
	if code := f.publish("a", "?secret=sek"); code != http.StatusForbidden {
		t.Fatalf("durum %d", code)
	}
}

func TestSecondPublisherRejectedAndCannotTakeChannelOffline(t *testing.T) {
	f := setup(t)
	ch := fmt.Sprint(f.channel)
	if code := f.publish("a", "?secret=sek"); code != http.StatusOK {
		t.Fatalf("ilk yayıncı: durum %d", code)
	}
	if code := f.publish("b", "?secret=sek"); code != http.StatusForbidden {
		t.Fatalf("ikinci yayıncı: durum %d", code)
	}
	if code := f.post(hookSecret, "unpublish", event("b", "live", ch, "")); code != http.StatusOK {
		t.Fatalf("unpublish her zaman kabul edilmeli: durum %d", code)
	}
	if !f.live() {
		t.Fatal("reddedilen bağlantının bildirimi süren yayını çevrimdışı yapmamalı")
	}
	f.post(hookSecret, "unpublish", event("a", "live", ch, ""))
	if f.live() {
		t.Fatal("yayıncının kendi bildirimi kanalı çevrimdışı yapmalı")
	}
}

func TestUnpublishWithMalformedStreamIsAccepted(t *testing.T) {
	f := setup(t)
	if code := f.post(hookSecret, "unpublish", event("a", "live", "abc", "")); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}

func TestPlayAcceptsValidToken(t *testing.T) {
	f := setup(t)
	f.publish("a", "?secret=sek")
	param := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute)) + "&hls_ctx=abc"
	if code := f.play(fmt.Sprint(f.channel), param); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}

func TestPlayRejectsTokenForOtherChannel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := must(f.store.CreateChannel(ctx, f.tenant, "c2", "sek2"))
	must(f.store.MarkLive(ctx, other, "z"))
	f.publish("a", "?secret=sek")
	param := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
	if code := f.play(fmt.Sprint(other), param); code != http.StatusForbidden {
		t.Fatalf("durum %d", code)
	}
}

func TestPlayRejections(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(f *fixture) string{
		"imza yok":       func(f *fixture) string { return "" },
		"bozuk imza":     func(f *fixture) string { return "?token=1.1.1.AAAA" },
		"süresi dolmuş":  func(f *fixture) string { return f.tokenFor(f.viewer, f.channel, now) },
		"olmayan izleyici": func(f *fixture) string { return f.tokenFor(f.viewer+999, f.channel, now.Add(time.Minute)) },
		"askıda izleyici": func(f *fixture) string {
			f.store.SetViewerStatus(ctx, f.viewer, "suspended")
			return f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
		},
		"süresi dolmuş izleyici": func(f *fixture) string {
			past := now.Add(-time.Hour)
			f.store.SetViewerExpiry(ctx, f.viewer, &past)
			return f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
		},
		"başka yayıncının izleyicisi": func(f *fixture) string {
			t2 := must(f.store.CreateTenant(ctx, "t2"))
			v2 := must(f.store.CreateViewer(ctx, t2, "veli", "pw", 1))
			return f.tokenFor(v2, f.channel, now.Add(time.Minute))
		},
	}
	for name, makeParam := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.publish("a", "?secret=sek")
			if code := f.play(fmt.Sprint(f.channel), makeParam(f)); code != http.StatusForbidden {
				t.Fatalf("durum %d", code)
			}
		})
	}
}

func TestPlayRejectsOfflineChannel(t *testing.T) {
	f := setup(t)
	param := f.tokenFor(f.viewer, f.channel, now.Add(time.Minute))
	if code := f.play(fmt.Sprint(f.channel), param); code != http.StatusForbidden {
		t.Fatalf("durum %d", code)
	}
}

func TestStopIsAccepted(t *testing.T) {
	f := setup(t)
	if code := f.post(hookSecret, "stop", event("p1", "live", fmt.Sprint(f.channel), "")); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
}
```

- [ ] **Step 2: Testin başarısız olduğunu gör**

Run: `go test -p 1 ./internal/hooks/`
Expected: FAIL, `undefined: hooks.New`

- [ ] **Step 3: Uygulamayı yaz**

`internal/hooks/hooks.go`:

```go
// Package hooks, SRS'in yayın ve izleme olaylarında sorduğu yetki sorgularını yanıtlar.
package hooks

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"streamhub/internal/store"
	"streamhub/internal/token"
)

const app = "live"

// Event, SRS'in gönderdiği gövdenin kullandığımız alanlarıdır.
type Event struct {
	ClientID string `json:"client_id"`
	App      string `json:"app"`
	Stream   string `json:"stream"`
	Param    string `json:"param"`
}

type Handler struct {
	store  *store.Store
	signer *token.Signer
	now    func() time.Time
}

func New(s *store.Store, signer *token.Signer, now func() time.Time) *Handler {
	return &Handler{store: s, signer: signer, now: now}
}

func (h *Handler) Register(mux *http.ServeMux, secret string) {
	events := map[string]func(context.Context, Event) int{
		"publish":   h.onPublish,
		"unpublish": h.onUnpublish,
		"play":      h.onPlay,
		"stop":      func(context.Context, Event) int { return http.StatusOK },
	}
	mux.HandleFunc("POST /hooks/srs/{secret}/{event}", func(w http.ResponseWriter, r *http.Request) {
		handle, known := events[r.PathValue("event")]
		if !known || subtle.ConstantTimeCompare([]byte(r.PathValue("secret")), []byte(secret)) != 1 {
			http.NotFound(w, r)
			return
		}
		var ev Event
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&ev); err != nil {
			respond(w, http.StatusBadRequest)
			return
		}
		respond(w, handle(r.Context(), ev))
	})
}

func (h *Handler) onPublish(ctx context.Context, ev Event) int {
	id, ok := channelID(ev)
	if !ok {
		return http.StatusForbidden
	}
	ch, err := h.store.ChannelByID(ctx, id)
	if err != nil {
		return failure(err)
	}
	given := param(ev.Param, "secret")
	if ch.TenantStatus != "active" || subtle.ConstantTimeCompare([]byte(given), []byte(ch.StreamSecret)) != 1 {
		return http.StatusForbidden
	}
	started, err := h.store.MarkLive(ctx, id, ev.ClientID)
	if err != nil {
		return failure(err)
	}
	if !started {
		return http.StatusForbidden
	}
	return http.StatusOK
}

// onUnpublish her zaman kabul eder: SRS reddettiği bağlantılar için de bu bildirimi gönderir.
func (h *Handler) onUnpublish(ctx context.Context, ev Event) int {
	id, ok := channelID(ev)
	if !ok {
		return http.StatusOK
	}
	if err := h.store.MarkOffline(ctx, id, ev.ClientID); err != nil {
		log.Printf("hooks: kanal %d çevrimdışı işaretlenemedi: %v", id, err)
	}
	return http.StatusOK
}

func (h *Handler) onPlay(ctx context.Context, ev Event) int {
	id, ok := channelID(ev)
	if !ok {
		return http.StatusForbidden
	}
	now := h.now()
	claims, err := h.signer.Verify(param(ev.Param, "token"), now)
	if err != nil || claims.ChannelID != id {
		return http.StatusForbidden
	}
	v, err := h.store.ViewerByID(ctx, claims.ViewerID)
	if err != nil {
		return failure(err)
	}
	ch, err := h.store.ChannelByID(ctx, id)
	if err != nil {
		return failure(err)
	}
	if !v.Usable(now) || v.TenantID != ch.TenantID || !ch.Live {
		return http.StatusForbidden
	}
	return http.StatusOK
}

func channelID(ev Event) (int64, bool) {
	if ev.App != app {
		return 0, false
	}
	id, err := strconv.ParseInt(ev.Stream, 10, 64)
	return id, err == nil && id > 0
}

func param(raw, key string) string {
	q, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
	if err != nil {
		return ""
	}
	return q.Get(key)
}

func failure(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusForbidden
	}
	log.Printf("hooks: veritabanı hatası: %v", err)
	return http.StatusInternalServerError
}

func respond(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusOK {
		io.WriteString(w, `{"code":0}`)
		return
	}
	io.WriteString(w, `{"code":`+strconv.Itoa(status)+`}`)
}
```

- [ ] **Step 4: Testlerin geçtiğini gör**

Run: `go test -p 1 ./internal/hooks/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/hooks
git commit -m "feat: authorize SRS publish and play events

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: İzleme yönlendirmesi

**Files:**
- Create: `internal/play/play.go`
- Test: `internal/play/play_test.go`

**Interfaces:**
- Consumes: `store.Store` yöntemleri `ViewerByUsername`, `ChannelByID`; `Viewer.Usable`; `token.Signer.Sign` ve `Verify`; `testdb.New`.
- Produces: `play.New(s *store.Store, signer *token.Signer, edgeBaseURL string, ttl time.Duration, now func() time.Time) *Handler`, `(*Handler).Register(mux *http.ServeMux)`. Kaydedilen yol: `GET /live/{username}/{password}/{file}`. Başarıda 302, `Location: <edgeBaseURL>/live/<kanal no>.<ts|m3u8>?token=<imza>`. Hatalar: 403 (izleyici geçersiz), 404 (dosya adı, kanal yok, başka yayıncının kanalı, kanal çevrimdışı), 500 (veritabanı).

- [ ] **Step 1: Testleri yaz**

`internal/play/play_test.go`:

```go
package play_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"streamhub/internal/play"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
	"streamhub/internal/token"
)

const edge = "http://edge.test:8080"

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t       *testing.T
	store   *store.Store
	signer  *token.Signer
	mux     *http.ServeMux
	tenant  int64
	channel int64
	viewer  int64
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func setup(t *testing.T) *fixture {
	ctx := context.Background()
	f := &fixture{t: t, store: testdb.New(t), signer: token.NewSigner([]byte(strings.Repeat("k", 32))), mux: http.NewServeMux()}
	f.tenant = must(f.store.CreateTenant(ctx, "t"))
	f.channel = must(f.store.CreateChannel(ctx, f.tenant, "c", "gizli-anahtar"))
	f.viewer = must(f.store.CreateViewer(ctx, f.tenant, "ali", "pw", 1))
	must(f.store.MarkLive(ctx, f.channel, "a"))
	play.New(f.store, f.signer, edge, 5*time.Minute, func() time.Time { return now }).Register(f.mux)
	return f
}

func (f *fixture) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func (f *fixture) path(user, pass, file string) string {
	return "/live/" + user + "/" + pass + "/" + file
}

func TestRedirectsToEdgeWithValidToken(t *testing.T) {
	for _, ext := range []string{"ts", "m3u8"} {
		f := setup(t)
		rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.%s", f.channel, ext)))
		if rec.Code != http.StatusFound {
			t.Fatalf("%s: durum %d", ext, rec.Code)
		}
		loc := must(url.Parse(rec.Header().Get("Location")))
		wantPrefix := fmt.Sprintf("%s/live/%d.%s", edge, f.channel, ext)
		if got := loc.Scheme + "://" + loc.Host + loc.Path; got != wantPrefix {
			t.Fatalf("%s: adres %q, beklenen %q", ext, got, wantPrefix)
		}
		claims, err := f.signer.Verify(loc.Query().Get("token"), now)
		if err != nil {
			t.Fatalf("%s: imza doğrulanamadı: %v", ext, err)
		}
		if claims.ViewerID != f.viewer || claims.ChannelID != f.channel || !claims.ExpiresAt.Equal(now.Add(5*time.Minute)) {
			t.Fatalf("%s: beklenmeyen içerik %+v", ext, claims)
		}
		if strings.Contains(rec.Header().Get("Location"), "gizli-anahtar") {
			t.Fatalf("%s: adres gizli yayın anahtarını içeriyor", ext)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: yönlendirme önbelleğe alınmamalı", ext)
		}
	}
}

func TestRejectsInvalidViewer(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(f *fixture) (user, pass string){
		"yanlış şifre":     func(f *fixture) (string, string) { return "ali", "yanlis" },
		"olmayan kullanıcı": func(f *fixture) (string, string) { return "yok", "pw" },
		"askıda izleyici": func(f *fixture) (string, string) {
			f.store.SetViewerStatus(ctx, f.viewer, "suspended")
			return "ali", "pw"
		},
		"süresi dolmuş": func(f *fixture) (string, string) {
			past := now.Add(-time.Hour)
			f.store.SetViewerExpiry(ctx, f.viewer, &past)
			return "ali", "pw"
		},
		"askıda yayıncı": func(f *fixture) (string, string) {
			f.store.SetTenantStatus(ctx, f.tenant, "suspended")
			return "ali", "pw"
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			user, pass := prepare(f)
			rec := f.get(f.path(user, pass, fmt.Sprintf("%d.ts", f.channel)))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("durum %d", rec.Code)
			}
			if rec.Header().Get("Location") != "" {
				t.Fatal("ret yanıtında yönlendirme olmamalı")
			}
		})
	}
}

func TestOtherTenantChannelLooksMissing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	t2 := must(f.store.CreateTenant(ctx, "t2"))
	other := must(f.store.CreateChannel(ctx, t2, "c2", "s2"))
	must(f.store.MarkLive(ctx, other, "z"))
	if rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.ts", other))); rec.Code != http.StatusNotFound {
		t.Fatalf("durum %d", rec.Code)
	}
}

func TestOfflineChannelIsNotFound(t *testing.T) {
	f := setup(t)
	if err := f.store.MarkOffline(context.Background(), f.channel, "a"); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(f.path("ali", "pw", fmt.Sprintf("%d.ts", f.channel))); rec.Code != http.StatusNotFound {
		t.Fatalf("durum %d", rec.Code)
	}
}

func TestRejectsUnknownFileNames(t *testing.T) {
	f := setup(t)
	id := fmt.Sprint(f.channel)
	for _, file := range []string{id, id + ".mp4", id + ".", id + ".ts.ts", "abc.ts", ".ts", "0.ts", "-1.ts", "999999.ts", id + ".TS"} {
		if rec := f.get(f.path("ali", "pw", file)); rec.Code != http.StatusNotFound {
			t.Errorf("%q: durum %d", file, rec.Code)
		}
	}
}

func TestEncodedUsername(t *testing.T) {
	f := setup(t)
	must(f.store.CreateViewer(context.Background(), f.tenant, "a@b c", "p/w", 1))
	rec := f.get(f.path("a%40b%20c", "p%2Fw", fmt.Sprintf("%d.ts", f.channel)))
	if rec.Code != http.StatusFound {
		t.Fatalf("durum %d", rec.Code)
	}
}
```

- [ ] **Step 2: Testin başarısız olduğunu gör**

Run: `go test -p 1 ./internal/play/`
Expected: FAIL, `undefined: play.New`

- [ ] **Step 3: Uygulamayı yaz**

`internal/play/play.go`:

```go
// Package play, izleyicinin Xtream biçimli yayın isteğini doğrular ve edge'e yönlendirir.
package play

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"streamhub/internal/store"
	"streamhub/internal/token"
)

type Handler struct {
	store       *store.Store
	signer      *token.Signer
	edgeBaseURL string
	ttl         time.Duration
	now         func() time.Time
}

func New(s *store.Store, signer *token.Signer, edgeBaseURL string, ttl time.Duration, now func() time.Time) *Handler {
	return &Handler{store: s, signer: signer, edgeBaseURL: edgeBaseURL, ttl: ttl, now: now}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /live/{username}/{password}/{file}", h.serve)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	channelID, ext, ok := parseFile(r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := h.now()

	v, err := h.store.ViewerByUsername(r.Context(), r.PathValue("username"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PathValue("password")), []byte(v.Password)) != 1 || !v.Usable(now) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	ch, err := h.store.ChannelByID(r.Context(), channelID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if ch.TenantID != v.TenantID || !ch.Live {
		http.NotFound(w, r)
		return
	}

	tok := h.signer.Sign(token.Claims{ViewerID: v.ID, ChannelID: ch.ID, ExpiresAt: now.Add(h.ttl)})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, fmt.Sprintf("%s/live/%d.%s?token=%s", h.edgeBaseURL, ch.ID, ext, tok), http.StatusFound)
}

func parseFile(file string) (int64, string, bool) {
	name, ext, found := strings.Cut(file, ".")
	if !found || (ext != "ts" && ext != "m3u8") {
		return 0, "", false
	}
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, ext, true
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("play: veritabanı hatası: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
```

- [ ] **Step 4: Testlerin geçtiğini gör**

Run: `go test -p 1 ./internal/play/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/play
git commit -m "feat: redirect viewers to edge with signed token

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Kanal durumu eşitleyici

SRS çökerse veya yeniden başlarsa "yayın bitti" bildirimi gelmez ve kanal "yayında" kalır; `MarkLive` bu durumda yeni yayını reddeder. Eşitleyici, SRS'in yayın listesini düzenli sorar ve listede olmayan kanalları çevrimdışı yapar. SRS'e ulaşılamazsa hiçbir kanala dokunmaz.

Yanıt biçimi Görev 1'de `docs/srs-findings.md` dosyasına kaydedildi (5. satır); aşağıdaki alan adları oradaki gözlemle uyuşmuyorsa dur ve bildir.

**Files:**
- Create: `internal/reconcile/reconcile.go`
- Test: `internal/reconcile/reconcile_test.go`

**Interfaces:**
- Consumes: `store.Store.ReconcileLive(ctx, activeIDs []int64, grace time.Duration) (int64, error)`, `MarkLive`, `ChannelByID`; `testdb.New`.
- Produces: `reconcile.New(s *store.Store, srsAPIURL string, grace time.Duration) *Reconciler`, `(*Reconciler).RunOnce(ctx) (int64, error)`, `(*Reconciler).Run(ctx, interval time.Duration)` (bağlam iptal edilene kadar döner).

- [ ] **Step 1: Testleri yaz**

`internal/reconcile/reconcile_test.go`:

```go
package reconcile_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"streamhub/internal/reconcile"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func srs(t *testing.T, status int, body string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/streams" || r.URL.Query().Get("count") == "" {
			t.Errorf("beklenmeyen istek: %s", r.URL)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func twoLiveChannels(t *testing.T) (*store.Store, int64, int64) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	a := must(s.CreateChannel(ctx, tid, "a", "s1"))
	b := must(s.CreateChannel(ctx, tid, "b", "s2"))
	must(s.MarkLive(ctx, a, "ca"))
	must(s.MarkLive(ctx, b, "cb"))
	return s, a, b
}

func TestRunOnceClearsStaleLive(t *testing.T) {
	s, a, b := twoLiveChannels(t)
	ctx := context.Background()
	body := `{"code":0,"streams":[
		{"name":"1","app":"live","publish":{"active":true}},
		{"name":"2","app":"live","publish":{"active":false}},
		{"name":"1","app":"other","publish":{"active":true}},
		{"name":"abc","app":"live","publish":{"active":true}}]}`
	if a != 1 || b != 2 {
		t.Fatalf("test, kanal numaralarının 1 ve 2 olmasına dayanıyor: %d, %d", a, b)
	}

	n, err := reconcile.New(s, srs(t, 200, body), 0).RunOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("1 kanal temizlenmeliydi: n=%d err=%v", n, err)
	}
	if !must(s.ChannelByID(ctx, a)).Live {
		t.Fatal("SRS'te süren yayın çevrimdışı yapılmamalı")
	}
	if must(s.ChannelByID(ctx, b)).Live {
		t.Fatal("SRS'te yayını bitmiş kanal çevrimdışı yapılmalı")
	}
	if !must(s.MarkLive(ctx, b, "yeni")) {
		t.Fatal("temizlenen kanala yeniden yayın açılabilmeli")
	}
}

func TestRunOnceLeavesChannelsAloneWhenSRSFails(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"HTTP hatası":   {500, `oops`},
		"SRS hata kodu": {200, `{"code":100}`},
		"bozuk JSON":    {200, `{"code":0,"streams":`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, a, b := twoLiveChannels(t)
			ctx := context.Background()
			if _, err := reconcile.New(s, srs(t, c.status, c.body), 0).RunOnce(ctx); err == nil {
				t.Fatal("hata bekleniyordu")
			}
			if !must(s.ChannelByID(ctx, a)).Live || !must(s.ChannelByID(ctx, b)).Live {
				t.Fatal("SRS'e ulaşılamadığında kanallara dokunulmamalı")
			}
		})
	}
}

func TestRunOnceLeavesChannelsAloneWhenSRSUnreachable(t *testing.T) {
	s, a, _ := twoLiveChannels(t)
	ctx := context.Background()
	if _, err := reconcile.New(s, "http://127.0.0.1:1", 0).RunOnce(ctx); err == nil {
		t.Fatal("hata bekleniyordu")
	}
	if !must(s.ChannelByID(ctx, a)).Live {
		t.Fatal("SRS'e ulaşılamadığında kanallara dokunulmamalı")
	}
}
```

- [ ] **Step 2: Testin başarısız olduğunu gör**

Run: `go test -p 1 ./internal/reconcile/`
Expected: FAIL, `undefined: reconcile.New`

- [ ] **Step 3: Uygulamayı yaz**

`internal/reconcile/reconcile.go`:

```go
// Package reconcile, veritabanındaki "yayında" durumunu SRS'teki gerçek yayınlarla eşitler.
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"streamhub/internal/store"
)

// maxStreams, SRS'ten tek istekte istenen yayın sayısıdır; SRS varsayılan olarak yalnızca 10 döner.
const maxStreams = 10000

type Reconciler struct {
	store     *store.Store
	srsAPIURL string
	grace     time.Duration
	client    *http.Client
}

func New(s *store.Store, srsAPIURL string, grace time.Duration) *Reconciler {
	return &Reconciler{store: s, srsAPIURL: srsAPIURL, grace: grace, client: &http.Client{Timeout: 5 * time.Second}}
}

func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if n, err := r.RunOnce(ctx); err != nil {
			log.Printf("reconcile: %v", err)
		} else if n > 0 {
			log.Printf("reconcile: %d kanal çevrimdışı yapıldı", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce, SRS'e ulaşılamazsa hiçbir kanala dokunmadan hata döner.
func (r *Reconciler) RunOnce(ctx context.Context) (int64, error) {
	active, err := r.activeChannelIDs(ctx)
	if err != nil {
		return 0, err
	}
	return r.store.ReconcileLive(ctx, active, r.grace)
}

func (r *Reconciler) activeChannelIDs(ctx context.Context) ([]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/streams?count=%d", r.srsAPIURL, maxStreams), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SRS yayın listesi: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Code    int `json:"code"`
		Streams []struct {
			Name    string `json:"name"`
			App     string `json:"app"`
			Publish struct {
				Active bool `json:"active"`
			} `json:"publish"`
		} `json:"streams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("SRS yayın listesi çözülemedi: %w", err)
	}
	if body.Code != 0 {
		return nil, fmt.Errorf("SRS yayın listesi: kod %d", body.Code)
	}
	ids := []int64{}
	for _, s := range body.Streams {
		if s.App != "live" || !s.Publish.Active {
			continue
		}
		if id, err := strconv.ParseInt(s.Name, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
```

- [ ] **Step 4: Testlerin geçtiğini gör**

Run: `go test -p 1 ./...`
Expected: tüm paketler `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/reconcile
git commit -m "feat: reconcile channel live state with SRS

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Program, kapsayıcı ve uçtan uca test

**Files:**
- Create: `cmd/streamhub/main.go`, `Dockerfile`, `.dockerignore`, `README.md`
- Modify: `docker-compose.yml` (`api` servisi eklenir)
- Test: `e2e/e2e_test.go`

**Interfaces:**
- Consumes: `config.Load`; `store.Open`, `Migrate`, `CreateTenant`, `CreateChannel`, `CreateViewer`; `token.NewSigner`; `hooks.New(...).Register(mux, secret)`; `play.New(...).Register(mux)`; `reconcile.New(...).Run(ctx, interval)`.
- Produces: `streamhub serve` (HTTP sunucusu, `GET /healthz` → 200 `ok`), `streamhub seed-dev` (stdout'a tek satır JSON: `{"channel_id":<sayı>,"stream_secret":"…","username":"…","password":"…"}`). Compose servisi `api` (8000).

- [ ] **Step 1: Uçtan uca testi yaz**

`e2e/e2e_test.go`:

```go
//go:build e2e

// Bu test çalışan bir "docker compose up" ortamı ister; bkz. README.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	apiURL  = "http://localhost:8000"
	edgeURL = "http://localhost:8080"
	pubName = "sh-e2e-pub"
)

type seed struct {
	ChannelID    int64  `json:"channel_id"`
	StreamSecret string `json:"stream_secret"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

var noRedirect = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func compose(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = ".."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args[:2], " "), err, stderr.String())
	}
	return out
}

func (s seed) playURL(ext string) string {
	return fmt.Sprintf("%s/live/%s/%s/%d.%s", apiURL, s.Username, s.Password, s.ChannelID, ext)
}

// probe, yönlendirmeyi izlemeden durum kodunu ve Location başlığını döner.
func probe(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// fetch, yönlendirmeleri izleyerek en çok n bayt okur; son adresi de döner.
func fetch(u string, n int) ([]byte, *url.URL) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.Request.URL
	}
	buf := make([]byte, n)
	read, _ := io.ReadFull(resp.Body, buf)
	return buf[:read], resp.Request.URL
}

func isMPEGTS(b []byte) bool {
	return len(b) >= 3*188 && b[0] == 0x47 && b[188] == 0x47 && b[376] == 0x47
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s için %s beklendi, gerçekleşmedi", what, timeout)
}

func firstSegment(playlist string) string {
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func TestSingleChannelEndToEnd(t *testing.T) {
	var s seed
	if err := json.Unmarshal(compose(t, "exec", "-T", "api", "streamhub", "seed-dev"), &s); err != nil {
		t.Fatalf("seed-dev çıktısı çözülemedi: %v", err)
	}

	if code, _ := probe(t, s.playURL("ts")); code != http.StatusNotFound {
		t.Fatalf("yayın yokken 404 bekleniyordu, gelen %d", code)
	}

	compose(t, "--profile", "e2e", "run", "-d", "--rm", "--name", pubName, "ffmpeg",
		"-re", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "flv",
		fmt.Sprintf("rtmp://srs/live/%d?secret=%s", s.ChannelID, s.StreamSecret))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", pubName).Run() })

	var location string
	waitFor(t, "kanalın yayında görünmesi", 30*time.Second, func() bool {
		code, loc := probe(t, s.playURL("ts"))
		location = loc
		return code == http.StatusFound
	})
	if !strings.HasPrefix(location, edgeURL+"/live/") {
		t.Fatalf("beklenmeyen yönlendirme adresi: %s", location)
	}
	if strings.Contains(location, s.StreamSecret) || strings.Contains(location, s.Password) {
		t.Fatal("yönlendirme adresi gizli değer içeriyor")
	}

	waitFor(t, "kesintisiz TS verisi", 30*time.Second, func() bool {
		body, _ := fetch(s.playURL("ts"), 3*188)
		return isMPEGTS(body)
	})

	var playlist string
	var playlistURL *url.URL
	waitFor(t, "HLS çalma listesi", 60*time.Second, func() bool {
		body, final := fetch(s.playURL("m3u8"), 64<<10)
		playlist, playlistURL = string(body), final
		return strings.HasPrefix(playlist, "#EXTM3U") && firstSegment(playlist) != ""
	})
	segURL, err := playlistURL.Parse(firstSegment(playlist))
	if err != nil {
		t.Fatalf("parça adresi çözülemedi: %v", err)
	}
	if body, _ := fetch(segURL.String(), 3*188); !isMPEGTS(body) {
		t.Fatalf("HLS parçası MPEG-TS değil: %s", segURL.Path)
	}

	wrong := s
	wrong.Password = "yanlis"
	if code, _ := probe(t, wrong.playURL("ts")); code != http.StatusForbidden {
		t.Fatalf("yanlış şifrede 403 bekleniyordu, gelen %d", code)
	}

	for _, direct := range []string{
		fmt.Sprintf("%s/live/%d.ts", edgeURL, s.ChannelID),
		fmt.Sprintf("%s/live/%d.ts?token=1.%d.9999999999.AAAA", edgeURL, s.ChannelID, s.ChannelID),
		fmt.Sprintf("%s/live/%d.m3u8", edgeURL, s.ChannelID),
	} {
		if body, _ := fetch(direct, 3*188); isMPEGTS(body) || strings.HasPrefix(string(body), "#EXTM3U") {
			t.Fatalf("imzasız veya sahte imzalı istek yayın döndürdü: %s", direct)
		}
	}

	if err := exec.Command("docker", "rm", "-f", pubName).Run(); err != nil {
		t.Fatalf("yayıncı durdurulamadı: %v", err)
	}
	waitFor(t, "kanalın çevrimdışı olması", 30*time.Second, func() bool {
		code, _ := probe(t, s.playURL("ts"))
		return code == http.StatusNotFound
	})
}
```

- [ ] **Step 2: Testin başarısız olduğunu gör**

Run: `go test -tags e2e -count=1 ./e2e/`
Expected: FAIL, `docker compose exec -T: ... service "api" is not running` (veya `no such service: api`)

- [ ] **Step 3: Programı yaz**

`cmd/streamhub/main.go`:

```go
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"streamhub/internal/config"
	"streamhub/internal/hooks"
	"streamhub/internal/play"
	"streamhub/internal/reconcile"
	"streamhub/internal/store"
	"streamhub/internal/token"
)

const (
	reconcileInterval = 15 * time.Second
	reconcileGrace    = 10 * time.Second
)

func main() {
	commands := map[string]func(context.Context) error{
		"serve":    serve,
		"seed-dev": seedDev,
	}
	if len(os.Args) != 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "kullanım: streamhub serve | seed-dev")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := commands[os.Args[1]](ctx); err != nil {
		log.Fatalf("streamhub %s: %v", os.Args[1], err)
	}
}

func serve(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	st, err := openStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	signer := token.NewSigner(cfg.TokenKey)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	hooks.New(st, signer, time.Now).Register(mux, cfg.HookSecret)
	play.New(st, signer, cfg.EdgeBaseURL, cfg.TokenTTL, time.Now).Register(mux)

	go reconcile.New(st, cfg.SRSAPIURL, reconcileGrace).Run(ctx, reconcileInterval)

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Printf("streamhub %s adresinde dinliyor", cfg.HTTPAddr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// seedDev, yerel deneme için bir yayıncı, kanal ve izleyici oluşturur ve bilgilerini JSON olarak yazar.
func seedDev(ctx context.Context) error {
	st, err := openStore(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer st.Close()

	tenantID, err := st.CreateTenant(ctx, "dev")
	if err != nil {
		return err
	}
	secret, username, password := randomHex(16), "dev-"+randomHex(4), randomHex(8)
	channelID, err := st.CreateChannel(ctx, tenantID, "Dev Kanal", secret)
	if err != nil {
		return err
	}
	if _, err := st.CreateViewer(ctx, tenantID, username, password, 1); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"channel_id":    channelID,
		"stream_secret": secret,
		"username":      username,
		"password":      password,
	})
}

func openStore(ctx context.Context, url string) (*store.Store, error) {
	if url == "" {
		return nil, errors.New("DATABASE_URL boş olamaz")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: Kapsayıcıyı tanımla**

`.dockerignore`:

```gitignore
.git
.env
docs
e2e
```

`Dockerfile`:

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/streamhub ./cmd/streamhub

FROM alpine:3.22
RUN adduser -D -u 10001 streamhub
USER streamhub
COPY --from=build /out/streamhub /usr/local/bin/streamhub
EXPOSE 8000
ENTRYPOINT ["streamhub"]
CMD ["serve"]
```

`docker-compose.yml` içinde `srs` servisinden önce şu servisi ekle:

```yaml
  api:
    build: .
    environment:
      DATABASE_URL: postgres://streamhub:streamhub@postgres:5432/streamhub?sslmode=disable
      SRS_API_URL: http://srs:1985
      TOKEN_KEY: ${TOKEN_KEY}
      HOOK_SECRET: ${HOOK_SECRET}
      EDGE_BASE_URL: ${EDGE_BASE_URL}
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "8000:8000"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8000/healthz"]
      interval: 2s
      timeout: 3s
      retries: 30
```

- [ ] **Step 5: Kullanım notunu yaz**

`README.md`:

````markdown
# StreamHub

OBS ile yayın açılan, Xtream uyumlu oynatıcılardan izlenen çok kiracılı canlı yayın platformu.
Tasarım: `docs/superpowers/specs/2026-10-05-streamhub-design.md`

## Yerelde çalıştırma

```bash
cp .env.example .env
docker compose up -d --build --wait
docker compose exec -T api streamhub seed-dev
```

`seed-dev` çıktısındaki değerlerle:

- OBS → Ayarlar → Yayın → Özel: sunucu `rtmp://localhost/live`, yayın anahtarı `<channel_id>?secret=<stream_secret>`
- İzleme: `http://localhost:8000/live/<username>/<password>/<channel_id>.ts` (veya `.m3u8`)

## Testler

```bash
docker compose up -d postgres
go test -p 1 ./...
```

Uçtan uca test (tüm servisler çalışırken):

```bash
docker compose up -d --build --wait
go test -tags e2e -count=1 ./e2e/
```
````

- [ ] **Step 6: Ortamı kaldır ve uçtan uca testin geçtiğini gör**

```bash
docker compose down
docker compose up -d --build --wait
go test -tags e2e -count=1 ./e2e/
```

Expected: `ok streamhub/e2e`

Test "imzasız veya sahte imzalı istek yayın döndürdü" ile başarısız olursa bu bir güvenlik açığıdır: SRS izleme sorgusunu o istek türü için göndermiyor demektir. `docs/srs-findings.md` dosyasındaki 2., 3. ve 8. satırlarla karşılaştır, DUR ve insan ortağa bildir.

- [ ] **Step 7: Birim ve entegrasyon testlerinin hâlâ geçtiğini gör**

Run: `go vet ./... && go test -p 1 ./...`
Expected: tüm paketler `ok`, `go vet` çıktısı boş

- [ ] **Step 8: OBS ve gerçek oynatıcıyla elle doğrula**

```bash
docker compose exec -T api streamhub seed-dev
```

1. Çıktıdaki değerlerle OBS'i README'deki gibi ayarla ve yayını başlat.
2. VLC → Ortam → Ağ Akışı Aç: `http://localhost:8000/live/<username>/<password>/<channel_id>.ts`. Görüntü ve ses gelmeli.
3. Aynısını `.m3u8` ile dene.
4. OBS'te yayını durdur; aynı adres 30 saniye içinde açılmaz olmalı.
5. Sonuçları `docs/srs-findings.md` dosyasının sonuna "Elle doğrulama" başlığıyla ekle (OBS sürümü, VLC sürümü, her adımın sonucu).

- [ ] **Step 9: Commit**

```bash
git add cmd Dockerfile .dockerignore docker-compose.yml README.md e2e docs/srs-findings.md
git commit -m "feat: wire server, container and end-to-end test

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
