# StreamHub — Tasarım Dokümanı

Tarih: 2026-10-05
Durum: Uygulandı (Plan 1-5)

## 1. Amaç

Yayıncıların kendi OBS'leriyle canlı yayın açtığı, izleyicilerin Xtream Codes
uyumlu IPTV oynatıcılarından (TiviMate, IPTV Smarters, VLC vb.) izlediği çok
kiracılı bir canlı yayın platformu.

- Bağımsız bir yazılımdır; Arcak One'a kod veya altyapı bağımlılığı yoktur.
- Başlangıçta gelir hedefi yoktur; ödeme ve bayi katmanı kapsam dışıdır.
- Tek bir VPS'te çalışır, kod değişikliği olmadan edge sunucu eklenerek büyür.
- Platform yalnızca yayıncının kendi ürettiği veya lisansına sahip olduğu
  içerik için tasarlanmıştır.

### Başarı ölçütleri

1. Yayıncı, panelde gördüğü sunucu adresi ve anahtarı OBS'e yazarak yayın açar.
2. İzleyici, sunucu adresi + kullanıcı adı + şifre ile bir Xtream oynatıcısına
   giriş yapar, yalnızca kendi yayıncısının kanallarını görür ve izler.
3. Yayın hem `.ts` hem `.m3u8` biçiminde izlenebilir.
4. Bir yayıncı başka bir yayıncının kanalını, izleyicisini veya oturumunu
   hiçbir yoldan göremez.
5. İkinci bir sunucu edge olarak kaydedildiğinde izleyiciler iki sunucuya
   dağıtılır.

### Kapsam dışı (ilk sürüm)

Transcode / çoklu bitrate, kayıt ve arşiv (VOD), EPG, ödeme ve abonelik
satışı, bayi yönetimi, serbest yayıncı kaydı, davetle kayıt, web oynatıcı,
yayıncıya özel alt alan adı, Redis, OBS'i panelden uzaktan kontrol.

## 2. Yaklaşım

Hazır bir medya sunucusu (SRS) üzerine kendi kontrol katmanımızı yazıyoruz.

Değerlendirilen seçenekler:

| Seçenek | Sonuç |
|---|---|
| SRS + kendi kontrol katmanı | **Seçildi.** RTMP/SRT girişi, HLS, kesintisiz `.ts` çıkışı ve edge modu hazır. |
| MediaMTX + kendi kontrol katmanı | Reddedildi. Kesintisiz `.ts` çıkışı hazır değil; ek FFmpeg hattı gerekir. |
| Her şey tek uygulamada | Reddedildi. Medya sunucusu yazmak asıl hedefi geciktirir. |

Bu yaklaşımın dayandığı üç varsayım SRS 5.0.225'e karşı ölçüldü
(ayrıntı: `docs/srs-findings.md`) ve tasarım sonuca göre düzeltildi:

| Varsayım | Sonuç | Tasarıma etkisi |
|---|---|---|
| SRS, yayın anahtarındaki parametreleri yetki sorgusuna aktarır | Tuttu | Yok |
| SRS, kesintisiz MPEG-TS çıkışı verir | Yalnızca HLS kapalıyken; ikisi aynı SRS'te açılamıyor | `.ts` için ikinci bir SRS (`srs-ts`) çalışır; origin her yayını ona iletir |
| SRS, HLS izlemelerini yetkilendirir | Çalma listesi için evet, parçalar için hayır: parçalar denetimsiz ve adları tahmin edilebilir | HLS, SRS'ten doğrudan sunulmaz; API içindeki geçit her isteği doğrular |

## 3. Mimari

| Bileşen | Görevi | Kaynak |
|---|---|---|
| SRS (origin) | OBS'ten RTMP/SRT yayını alır, HLS üretir, yayını `srs-ts`'e iletir. HTTP portu dışarıya açılmaz | Hazır |
| SRS (`srs-ts`) | Origin'in ilettiği yayını kesintisiz `.ts` olarak dağıtır | Hazır |
| Kontrol API'si | Xtream uçları, HLS geçidi, panel API'si, SRS yetki sorguları, imzalı adres üretimi | Go ile yazılır |
| Panel | Yönetici ve yayıncı ekranları | React ile yazılır, API programına gömülür |
| PostgreSQL | Kalıcı kayıtlar ve aktif oturumlar | Hazır |
| Edge | Yayını izleyiciye dağıtır. Yerel edge, kontrol sunucusundaki `srs-ts` ve HLS geçididir; uzak edge, edge kipinde bir SRS ve önündeki nginx'tir | Hazır |
| Ters vekil | TLS ve alan adı yönlendirmesi (Caddy) | Hazır |

- Tüm bileşenler Docker Compose ile tek komutta kalkar.
- Tek VPS kurulumunda origin ve edge aynı makinededir; edge yine de ayrı bir
  servis ve ayrı bir kayıttır.
- Yük dağıtımı ayrı bir ürün değildir: API, izleme isteğini seçtiği edge'e
  yönlendirir.
- Uzak edge'de veritabanı, imza anahtarı ya da kendi kodumuz bulunmaz. Edge
  yalnızca kendi anahtarını bilir; her izlemeyi kontrol sunucusu yetkilendirir.
  Dağıtım dosyaları `deploy/edge` klasöründedir.

### Kontrol API'sinin iç birimleri

| Birim | Sorumluluk | Bağımlılık |
|---|---|---|
| `store` | PostgreSQL erişimi; her sorgu yayıncı kimliğiyle sınırlanır | PostgreSQL |
| `auth` | Panel girişi, oturum çerezi, rol kontrolü | `store` |
| `token` | İmzalı izleme adresi üretme ve doğrulama | Yok |
| `balancer` | Sağlıklı edge'ler arasından ağırlığa göre seçim; edge'i anahtarından tanıma; origin'den çekme adresi denetimi | `store` |
| `session` | Oturum açma/kapama, limit hesabı, eskiyen oturum temizliği; her edge için bağlantı kesme, eşitleme ve sağlık sinyali | `store` |
| `xtream` | Xtream uçları ve yanıt biçimleri | `store`, `token`, `balancer` |
| `hooks` | SRS'in yayın ve `.ts` izleme sorguları | `store`, `token`, `session` |
| `hlsgw` | HLS geçidi: `/hls/<imza>/<dosya>` isteklerini doğrular ve SRS'e aktarır | `store`, `token`, `session` |
| `reconcile` | Kanalların "yayında" durumunu SRS'teki gerçek yayınlarla eşitler | `store` |
| `panelapi` | Yönetici ve yayıncı REST uçları | `store`, `auth` |

## 4. Veri modeli

| Kayıt | Alanlar |
|---|---|
| Yönetici | E-posta, şifre özeti |
| Yayıncı | Ad, e-posta, şifre özeti, durum (aktif/askıda), kanal kotası, izleyici kotası, eşzamanlı bağlantı kotası |
| Kategori | Yayıncı, ad, sıra |
| Kanal | Yayıncı, kategori, ad, logo, gizli yayın anahtarı, durum (yayında/çevrimdışı), son yayın zamanı |
| İzleyici | Yayıncı, kullanıcı adı, şifre, bitiş tarihi, bağlantı limiti, durum |
| Edge | Ad, izleyici adresi, yönetim adresi, anahtar, çekme adresi, durum, ağırlık, son sağlık sinyali |
| Oturum | İzleyici, kanal, edge, IP, başlangıç, son görülme |

Kurallar:

- Kanal numarası platform genelinde tekildir (Xtream `stream_id`).
- İzleyici kullanıcı adı platform genelinde tekildir; her izleyici tek bir
  yayıncıya bağlıdır.
- İzleyici şifresi sistem tarafından rastgele üretilir, yayıncı panelden
  görebilir ve yenileyebilir. Xtream protokolü şifreyi adres içinde açık
  taşıdığı için erişim anahtarı gibi ele alınır.
- Yönetici ve yayıncı şifreleri Argon2 ile saklanır.
- Yayıncı kaydındaki kota ve durum alanları, serbest kayıt eklendiğinde veri
  yapısının değişmemesi için baştan vardır.

## 5. Akışlar

### Yayın açma

1. Yayıncı panelde kanal oluşturur. Panel OBS ayarlarını gösterir:
   sunucu `rtmp://<yayın alan adı>/live`, anahtar `<kanal no>?secret=<gizli anahtar>`.
2. OBS bağlanınca SRS API'ye sorar. API gizli anahtarı, yayıncının aktif
   olduğunu ve kanalın başka bir bağlantıdan yayında olmadığını doğrular.
3. Kabul edilirse kanal "yayında" olur.
4. Yayın bitince SRS haber verir, kanal "çevrimdışı" olur.

Yayın SRS içinde kanal numarasıyla adlandırılır; gizli anahtar izleyiciye
giden hiçbir adreste yer almaz.

### İzleme

1. Oynatıcı `/live/<kullanıcı>/<şifre>/<kanal no>.ts` veya `.m3u8` ister.
2. API izleyiciyi, durumunu, bitiş tarihini ve kanalın izleyicinin
   yayıncısına ait olduğunu doğrular.
3. API sağlıklı edge'lerden birini ağırlığa göre seçer.
4. API imzalı adresle o edge'e yönlendirir (302). İki biçim farklı işler:
   - **`.ts`:** `<edge>/live/<kanal no>.ts?token=<imza>`. İmza birkaç dakika
     geçerlidir ve yalnızca izleme başlarken doğrulanır: `srs-ts` izleme
     başlarken API'ye sorar, API imzayı ve bağlantı limitini doğrular,
     oturumu açar; izleme bitince SRS haber verir ve oturum kapanır.
   - **`.m3u8`:** `<edge>/hls/<imza>/<kanal no>.m3u8`. İmza yolun içindedir;
     böylece çalma listesindeki göreli parça adresleri onu devralır. Geçit
     her çalma listesi ve parça isteğinde imzayı, izleyicinin durumunu ve
     kanalın yayında olduğunu doğrular, sonra dosyayı SRS'ten aktarır. Her
     istekte doğrulandığı için bu imzanın ömrü uzundur (varsayılan 6 saat);
     askıya alma ve yayının bitmesi birkaç saniye içinde etkili olur.

### Edge

Kontrol sunucusundaki dağıtım "yerel" edge kaydıdır: kurulumla birlikte gelir,
silinemez, adresleri ayarlardan okunur. Yönetici panelden uzak edge ekler;
kayıt sırasında edge'in anahtarı üretilir.

- **Yayının edge'e ulaşması:** Uzak edge, ilk izleyici geldiğinde yayını
  origin'den RTMP ile çeker, son izleyici gidince bırakır. Origin bu çekmeyi
  yalnızca kayıtlı ve etkin bir edge'in çekme adresinden kabul eder.
- **`.ts`:** Edge'in SRS'i izleme başlarken ve biterken kontrol sunucusuna
  sorar (`/edge/<anahtar>/hooks/…`). Kurallar yerel edge ile aynıdır; oturum o
  edge'e yazılır.
- **HLS:** Edge'in nginx'i her isteği kontrol sunucusuna sorar
  (`/edge/hls/<imza>/<dosya>`, anahtar başlıkta). Kabul edilirse dosyayı kendi
  önbelleğinden verir; önbellekte yoksa kontrol sunucusundan bir kez çeker
  (çalma listesi 1 saniye, parça 60 saniye saklanır). Yetki her istekte
  merkezde kalır, bant genişliği edge'den harcanır.
- **Sağlık sinyali:** Kontrol sunucusu her edge'in bağlantı listesini 5
  saniyede bir sorar (oturum eşitlemesi için zaten gerekir). Son 15 saniyede
  yanıt veren edge sağlıklıdır. Yerel edge'in HLS'i API'nin içinden verildiği
  için `.ts` dağıtıcısının durumundan bağımsız, her zaman kullanılabilir.
- **Seçim:** Etkin ve sağlıklı edge'ler arasından ağırlıkla orantılı rastgele.

SRS çöker veya yeniden başlarsa "yayın bitti" bildirimi gelmez. API bu
yüzden SRS'in yayın listesini düzenli olarak sorar ve listede olmayan
kanalları çevrimdışı yapar; SRS'e ulaşılamazsa hiçbir kanala dokunmaz.

### Xtream uçları

| Uç | Davranış |
|---|---|
| `player_api.php` (eylemsiz) | Hesap ve sunucu bilgisi |
| `get_live_categories` | İzleyicinin yayıncısına ait kategoriler |
| `get_live_streams` | İzleyicinin yayıncısına ait kanallar |
| `get_vod_categories`, `get_vod_streams`, `get_series_categories`, `get_series` | Boş liste |
| `get_short_epg`, `get_simple_data_table` | Boş liste |
| `get.php` | M3U çalma listesi (`output=ts` veya `m3u8`) |
| `xmltv.php` | Boş program rehberi |
| `/live/<k>/<ş>/<no>.<uzantı>` ve `/<k>/<ş>/<no>` | Edge'e imzalı yönlendirme |

### Panel

| Rol | Yapabildikleri |
|---|---|
| Yönetici | Yayıncı oluşturma, askıya alma, kota belirleme; edge kaydetme, ağırlık verme, devre dışı bırakma, silme ve durumunu görme; platform geneli canlı kanal ve oturum sayıları |
| Yayıncı | Kategori ve kanal yönetimi, OBS ayarlarını görme, yayın anahtarı yenileme, izleyici oluşturma/askıya alma/şifre yenileme, kendi canlı oturumlarını görme |

## 6. Güvenlik

- Her panel isteğinde yayıncı kimliği sunucu tarafında oturumdan alınır;
  istekle gelen yayıncı kimliğine güvenilmez.
- Panel, izleyici uçlarından ayrı bir portta (varsayılan 8002) dinlenir.
  Çerezi `HttpOnly`, `SameSite=Strict` ve `Secure` olur; değişiklik yapan istekler özel bir
  başlık taşımak zorundadır. Şifre değişince, sıfırlanınca veya yayıncı askıya
  alınınca eski oturumlar kapanır.
- Yayıncının panel şifresini sistem üretir ve yöneticiye bir kez gösterir;
  yayıncılar silinmez, askıya alınır.
- İmzalı adres izleyici, kanal ve son geçerlilik zamanını içerir, HMAC-SHA256
  ile imzalanır. Başlamış `.ts` izlemesi süre dolunca kesilmez; HLS izlemesi
  imza süresi (varsayılan 6 saat) dolunca oynatıcının adresi yeniden
  istemesini gerektirir.
- İmzalı adres geçerlilik süresi içinde paylaşılabilir; bağlantı limiti bunu
  sınırlar. HLS adresinin ömrü uzun olduğu için bu sınır HLS'te daha önemlidir
  ve bağlantı limiti gelene kadar (Plan 3) açıktır. IP'ye bağlama seçeneği
  sonraya bırakılmıştır.
- Origin SRS'in HTTP portu dışarıya açılmaz; HLS yalnızca geçitten verilir.
- İmza hangi yol için üretildiğini taşır (`.ts` veya HLS); uzun ömürlü HLS
  imzası `.ts` ve RTMP izlemesinde geçmez.
- SRS sorguları API'nin ayrı bir portunda (varsayılan 8001) dinlenir ve bu
  port dışarıya açılmaz.
- SRS, reddettiği sorguların adresini ve gövdesini hata loguna yazar; SRS
  logları gizli veri sayılır (ayrıntı: `docs/srs-findings.md`).
- Xtream uçlarında IP başına hatalı giriş sınırı uygulanır.
- SRS'in API'ye yaptığı sorgular yalnızca iç ağdan ve paylaşılan sır ile
  kabul edilir.
- Uzak edge kendini anahtarıyla tanıtır: SRS sorgularında adres yolunda (SRS
  başlık ekleyemez), nginx isteklerinde `X-Edge-Key` başlığında. Bu uçlar
  izleyicilere açık portta dinlenir; anahtarsız istek reddedilir.
- İzleyicinin IP adresi için edge'den gelen başlığa yalnızca geçerli edge
  anahtarıyla gelen isteklerde güvenilir.
- Edge'in SRS yönetim API'si nginx'te anahtarla korunan bir yoldadır; SRS'in
  portları dışarıya açılmaz.
- Edge anahtarı ve denetim trafiği HTTP üzerinden açık taşınır. Kontrol
  sunucusu ile edge arasında özel ağ ya da HTTPS kullanılmalıdır; edge
  kaydındaki yönetim adresi bunun için izleyici adresinden ayrı tutulabilir.
- Panel yalnızca HTTPS üzerinden sunulur. Xtream uçları, HTTPS desteklemeyen
  eski oynatıcılar için HTTP üzerinden de erişilebilir.

## 7. Hata durumları

| Durum | Davranış |
|---|---|
| Kanal çevrimdışıyken izleme isteği | 404 |
| Geçersiz kullanıcı adı veya şifre | Xtream biçiminde giriş reddi (`auth: 0`); yayın isteğinde 403 |
| Süresi dolmuş veya askıdaki izleyici, askıdaki yayıncının izleyicisi | Giriş yanıtı durumu bildirir (`auth: 1`, `status`: `Expired`, `Banned` veya `Disabled`); kanal listeleri boş döner, yayın isteği ve M3U 403 alır. Gerçek Xtream panelleri böyle davranır ve oynatıcılar izleyiciye "süre doldu" gibi anlamlı bir mesaj gösterir |
| Aynı IP'den çok sayıda hatalı giriş | Pencere bitene kadar 429 (doğru şifreyle bile) |
| İzleyici başka yayıncının kanalını ister | 404 |
| İzleyicinin bağlantı limiti dolu | Yeni izleme kabul edilir, en eski izleme kesilir. Boşta kalan HLS oturumu 30 saniye sayıldığı için "yeniyi reddet" kuralı kanal değiştirmeyi engellerdi; hesabını paylaşanlar birbirini düşürür |
| Yayıncının toplam bağlantı kotası dolu | Yeni izleyici 403 alır; süren izlemeler etkilenmez |
| HLS adresi başka bir ağdan açıldı | Oturum yeni ağa taşınır, eski ağdaki istekler 403 alır; taşınan oturum 1 dakika dolmadan yeniden taşınamaz. Ağ değiştiren izleyici devam eder, adresi paylaşan iki kişi sırayla izleyemez |
| Yerinden edilmiş HLS adresi yeniden kullanıldı | Hangi ağdan gelirse gelsin 403 |
| Edge sağlık sinyali kesildi | Yönlendirmeden çıkar, sinyal dönünce geri eklenir |
| Sağlıklı edge yok | 503 |
| "İzleme bitti" bildirimi kayboldu | `.ts` oturumları 5 saniyede bir SRS'in bağlantı listesiyle eşitlenir; SRS'e ulaşılamazsa oturum silinmez |
| İzleyici askıya alındı veya süresi doldu | Süren `.ts` izlemesi birkaç saniye içinde kesilir; HLS bir sonraki istekte reddedilir |
| Origin'e RTMP ile izleme denemesi | Kayıtlı ve etkin bir edge'in çekme adresi dışındaki her adresten, geçerli imzayla bile reddedilir |
| Edge devre dışı bırakıldı | Yeni izleyici yönlendirilmez; süren izlemeler devam eder. Edge origin'den yeni çekme başlatamaz |
| Edge silindi | Oturum kayıtları silinir; edge'de süren izlemeler kesilmez, yeni izleme yetkilendirilemez |
| Geçersiz edge anahtarıyla istek | İzleme sorgusu 404, HLS uçları 403 |
| Kanal veya izleyici silindi, yayın anahtarı ya da izleyici şifresi yenilendi | İlgili yayın ve izlemeler kesilir. Kesme o an başarısız olursa kuyrukta bekler ve yeniden denenir |
| Çok sayıda eşzamanlı panel girişi | Aynı anda sınırlı sayıda şifre doğrulanır; fazlası 503 alır |
| Yayıncı askıya alındı | Açık yayınları birkaç saniye içinde kesilir, izleyicileri izleyemez |
| Geçersiz yayın anahtarı veya kota aşımı | SRS yayını reddeder |
| Aynı kanala ikinci yayın bağlantısı | İkincisi reddedilir |

## 8. Test

- **Birim:** imza üretme/doğrulama, edge seçimi, limit ve kota hesapları,
  Xtream yanıt biçimleri.
- **Entegrasyon:** gerçek PostgreSQL'e karşı API uçları; kiracı ayrımı her
  uç için ayrıca sınanır.
- **Uçtan uca:** Docker Compose ayağa kalkar, FFmpeg sahte yayın gönderir,
  Xtream adresinden `.ts` ve `.m3u8` çekilip görüntü geldiği doğrulanır.
- **Elle:** OBS ile yayın, en az iki gerçek oynatıcıda izleme.

## 9. Geliştirme sırası

1. Tek kanal uçtan uca: SRS, yayın anahtarı doğrulama, `.ts` ve `.m3u8`
   izleme. Bölüm 2'deki SRS varsayımları burada doğrulanır.
2. Xtream uçları ve izleyici hesapları.
3. Yayıncı ayrımı, kotalar, bağlantı limiti, oturum takibi.
4. Panel: yönetici ve yayıncı ekranları.
5. Edge kaydı, sağlık sinyali ve yönlendirme; ikinci sunucuyla deneme
   (aynı makinede ayrı bir Compose projesi olarak denendi).

## 10. Sonraki aşamalar (bu dokümanın kapsamı dışında)

Serbest yayıncı kaydı ve içerik denetimi, kayıt ve arşiv, transcode, EPG,
yayıncıya özel alt alan adı, ödeme.
