# Kanalvo Plan 3 — Oturumlar, Bağlantı Limiti ve Kotalar

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Her izleyicinin eşzamanlı bağlantı sayısını sınırlamak, yayıncı kotalarını uygulamak ve askıya alınan yayıncının yayınını ve izleyicilerini kesmek.

**Architecture:** Her izleme bir oturum kaydıdır. `.ts` oturumlarını SRS'in "izleme başladı/bitti" sorguları açar ve kapatır; HLS oturumlarını geçit, imzadaki oturum anahtarı ve istemci adresiyle izler. Bir yönetici bileşen limitleri uygular ve SRS'in yönetim API'siyle bağlantı keser.

**Tech Stack:** Plan 1 ve 2 ile aynı.

**Spec:** `docs/superpowers/specs/2026-10-05-kanalvo-design.md` (bölüm 4, 5, 6, 7)

## Global Constraints

- Plan 1 ve 2'nin tüm kısıtları geçerlidir.
- SRS'in yönetim API'si (`:1985`) hiçbir SRS'te dışarıya açılmaz.
- SRS'e ulaşılamadığında oturum veya kanal durumu silinmez; yalnızca hata loglanır.
- Her commit mesajı şu satırla biter: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`

## Review Focus

1. **Kanal değiştirme:** Limiti 1 olan izleyici kanal değiştirdiğinde beklememeli; eski izleme yeni izlemeye yer açmalı.
2. **Paylaşılan HLS adresi:** Bir adres aynı anda iki ağdan kullanılamamalı; yerinden edilen adres hiçbir ağdan geri dönememeli.
3. **SRS çökmesi:** "İzleme bitti" bildirimi gelmezse `.ts` oturumları sonsuza dek limit doldurmamalı.
4. **Yarış durumu:** Aynı izleyicinin eşzamanlı iki oturum açılışı limiti aşmamalı.
5. **Askıya alma:** Askıya alınan yayıncının süren yayını ve askıya alınan ya da süresi dolan izleyicinin süren `.ts` izlemesi kesilmeli.

## Kararlar

| Konu | Karar | Neden |
|---|---|---|
| İzleyici limiti dolunca | En eski oturum sonlandırılır, yeni oturum kabul edilir | Boşta kalan HLS oturumu 30 saniye sayıldığı için "yeniyi reddet" kuralı kanal değiştirmeyi engellerdi. Hesabını paylaşanlar birbirini düşürür |
| Yayıncı bağlantı kotası dolunca | Yeni izleyici reddedilir | Bir izleyicinin başka bir izleyiciyi düşürmesi kabul edilemez |
| HLS oturum kimliği | İmzadaki rastgele anahtar; oturum aynı anda tek bir ağdan kullanılabilir | Başka ağdan açılan adres oturumu oraya taşır (Wi-Fi'den mobil veriye geçen izleyici devam eder). Taşınan oturum 1 dakika dolmadan yeniden taşınamaz; böylece adresi paylaşan iki kişi sırayla izleyemez. IPv6'da ağ /64 önekidir |
| HLS oturumunun boşta sayılması | Son istekten 30 saniye sonra | Çalma listesi yaklaşık 2 saniyede bir istenir |
| Sonlandırılan HLS oturumu | Kaydı imza ömrü (6 saat) boyunca saklanır ve adres hiçbir ağdan geri dönemez | Yerinden edilen tarafın ağ değiştirerek geri gelmesini önler |
| HLS son görülme zamanı | En sık 10 saniyede bir yazılır | HLS istekleri saniyede bir gelir; her birinde yazmak gereksiz yük olurdu |
| Uygulama döngüsü | Origin ve `.ts` dağıtıcısı adımları bağımsız çalışır, geçiş 20 saniyeyle sınırlıdır | Yanıt vermeyen bir SRS, diğerindeki kesmeleri geciktirmemeli |
| `.ts` oturumlarının eşitlenmesi | 5 saniyede bir SRS bağlantı listesiyle | SRS çökerse "izleme bitti" gelmez |
| Origin'e RTMP ile izleme | Her zaman reddedilir | İzleyici yolu değildir ve oturum takibini karmaşıklaştırırdı |
| SRT girişi | Bu plana alınmadı | Bağımsız bir iş; SRS'in SRT yetkilendirmesi ayrıca ölçülmeli |

## Dosya yapısı

| Dosya | Sorumluluk |
|---|---|
| `internal/store/migrations/0003_sessions_quotas.sql` | `sessions` tablosu, yayıncı kotaları |
| `internal/store/sessions.go` | Oturum açma (limit ve yerinden etme), kapatma, sayma, eşitleme sorguları |
| `internal/store/quotas.go` | Kotalar; kanal ve izleyici oluştururken kota denetimi |
| `internal/srsapi/srsapi.go` | SRS yönetim API'si: bağlantı listesi ve bağlantı kesme |
| `internal/session/session.go` | Oturum yöneticisi ve uygulama döngüsü |
| `internal/clientip/clientip.go` | İstemci anahtarı (hatalı giriş sınırı ve HLS oturumları ortak kullanır) |
| `internal/token/token.go` | HLS imzasında oturum anahtarı |
| `internal/hooks`, `internal/hlsgw`, `internal/play`, `internal/xtream` | Oturumların uçlara bağlanması; `active_cons` |
| `cmd/kanalvo/main.go` | Bağlama; `set-tenant-status` komutu |

## Görevler

Her görev test-önce uygulanır ve `go test -p 1 ./...` ile doğrulanır.

1. **SRS bağlantı kesme davranışının ölçülmesi:** bağlantı listesi alanları, izleyici ve yayıncı için `DELETE /api/v1/clients/<kimlik>`; sonuç `docs/srs-findings.md` dosyasına yazılır.
2. **Oturum ve kota veri katmanı:** yukarıdaki kararların her biri için test (`internal/store/sessions_test.go`).
3. **SRS API istemcisi ve oturum yöneticisi:** sahte SRS ile; kesme başarısız olursa yeni oturum yine açılır, SRS'e ulaşılamazsa oturum silinmez.
4. **Uçlara bağlama:** `.ts` izlemesi oturum açar ve kapatır; HLS geçidi her istekte oturumu işler; giriş yanıtı `active_cons` bildirir; origin'de izleme reddedilir.
5. **Uçtan uca test:** limit 1 iken ikinci `.ts` izlemesi ilkini keser; yeni HLS adresi eskisini geçersiz kılar; askıya alınan yayıncının yayını kesilir ve izleyicisi 403 alır.
