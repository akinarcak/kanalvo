# İlk sürüm denetimi

Tarih: 2026-11-17 · Kaynak: `docs/superpowers/specs/2026-10-05-kanalvo-design.md` · Taban: `9c73ca7`

Tasarımdaki başarı ölçütleri (§1), akışlar (§5), güvenlik kuralları (§6), hata durumları (§7) ve
test bölümü (§8) koda ve testlere tek tek eşlendi. Her satırda testin gövdesi okundu: test, o
davranışı gerçekten doğruluyorsa "tamam" sayıldı.

**Durum:** **tamam** — kod var, bir test davranışı doğruluyor · **bu PR'da kapatıldı** — eksik test
bu PR'da yazıldı (kodda düzeltme gerekmedi) · **açık** — tasarım kararı ya da elle iş gerektiriyor,
bu PR'da yapılmadı.

**Kısaltmalar (uçtan uca, `go test -tags e2e`, bkz. `docs/simulasyon.md`):**
`SC` = `e2e/e2e_test.go:TestSingleChannelEndToEnd` · `EDGE` = `e2e/edge_test.go:TestSecondServerSharesTheViewers`
(adım numarası testteki yorumdur) · `KES` = `e2e/edge_kesinti_test.go:TestEdgeDisableAndControlPartition` ·
`KIR` = `e2e/kiraci_test.go:TestTenantsAreIsolated` · `PANEL` = `e2e/panel_test.go:TestPanelCreatedChannelCanBeStreamedAndWatched` ·
`HTTPS` = `e2e/https_test.go` (`TestPanelLoginOverHTTPS`, `TestPanelPortIsOnlyOnLoopback`, `TestLoginLockoutFollowsTheClientBehindTheProxy`) ·
`KOTA` = `e2e/kota_test.go:TestPublishBeyondChannelQuotaIsRejectedBySRS`.
Diğer testler birim/entegrasyon testleridir (`go test -p 1 ./...`, gerçek PostgreSQL).

## Özet

| | Madde | tamam | bu PR'da kapatıldı | açık |
|---|---|---|---|---|
| §1 | Başarı ölçütleri | 3 | 2 | 0 |
| §5 | Akışlar | 40 | 2 | 0 |
| §6 | Güvenlik | 17 | 2 | 0 |
| §7 | Hata durumları | 17 | 6 | 0 |
| §8 | Test | 3 | 2 | 1 |

Kodda tasarımla çelişen bir hata bulunmadı; yeni testlerin hiçbiri kod düzeltmesi gerektirmedi.
Açık kalan: gerçek OBS ve oynatıcılarla elle deneme. Yayın kabulünde kota denetimi
([A2](#a2-yayın-kabulünde-kota), §7 sütununda) ve panelin HTTPS'i (ters vekil,
[A1](#a1-panel-https-i-ve-ters-vekil), §6 sütununda) sonradan ayrı PR'larda kapatıldı. Ayrıntı: [Açık maddeler](#açık-maddeler).

## §1 Başarı ölçütleri

| # | Madde | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| 1.1 | Yayıncı paneldeki sunucu adresi ve anahtarla OBS'ten yayın açar | `internal/panelapi/tenant.go:toChannelJSON`, `internal/hooks/hooks.go:onPublish` | `internal/panelapi/panelapi_test.go:TestChannelLifecycle`; `PANEL` (paneldeki `ingest_url` ve `stream_key` ile FFmpeg yayını, panelde `live`) | tamam | OBS yerine FFmpeg; gerçek OBS §8.4'te açık |
| 1.2 | İzleyici Xtream oynatıcısına girer, yalnızca kendi yayıncısının kanallarını görür ve izler | `internal/xtream/xtream.go:playerAPI/liveStreams/playlist`, `internal/play/play.go:serve` | `xtream_test.go:TestLiveStreams`, `TestPlaylistAndGuideRequireValidLogin`; `SC`; `KIR` (iki yayıncı: giriş, kategoriler, kanallar, M3U, izleme) | bu PR'da kapatıldı | Önceden tek yayıncıyla sınanıyordu; `KIR` iki yayıncıyla oynatıcı akışının tamamını sınar |
| 1.3 | Yayın hem `.ts` hem `.m3u8` biçiminde izlenebilir | `play.go:target`, `internal/hlsgw/hlsgw.go:serve`, `srs-ts` | `SC`; `KIR` ve `KES` (ffprobe ile ≥10 kare, yayının boyutu) | bu PR'da kapatıldı | Önceki e2e yalnızca 3 MPEG-TS eşitleme baytına bakıyordu; artık kare çözülüyor (yerel ve edge, `.ts` ve HLS) |
| 1.4 | Bir yayıncı başka yayıncının kanalını, izleyicisini veya oturumunu hiçbir yoldan göremez | `internal/store` (her sorgu `tenant_id` ile), `panelapi/tenant.go` (kimlik oturumdan), `play.go:serve`, `hlsgw.go:authorize`, `hooks.go:onPlay` | `panelapi_test.go:TestTenantsCannotTouchEachOthersRecords`; aşağıdaki matris; `KIR` | tamam | Uç uç eşleme: [Kiracı ayrımı matrisi](#kiracı-ayrımı-matrisi). Bilinen sızıntı: kullanıcı adı platform genelinde tekil olduğundan `POST /api/tenant/viewers` başka yayıncıdaki adı 409 ile ele verir (§4 kuralının doğal sonucu) |
| 1.5 | İkinci sunucu edge olarak kaydedilince izleyiciler iki sunucuya dağılır | `internal/balancer/balancer.go:Choose/Pick` | `internal/play/edge_test.go:TestViewersAreSpreadAcrossHealthyEdges`; `EDGE` adım 1 | tamam | |

## §5 Akışlar

### Yayın açma

| # | Madde | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| Y1 | Panel OBS ayarlarını gösterir: `rtmp://<alan>/live`, `<no>?secret=<gizli>` | `panelapi/tenant.go:toChannelJSON` | `panelapi_test.go:TestChannelLifecycle`; `PANEL` | tamam | |
| Y2 | SRS sorar; API gizli anahtarı, yayıncının aktifliğini, kanalın başka bağlantıdan yayında olmadığını doğrular | `hooks.go:onPublish`, `store/queries.go:MarkLive` | `hooks_test.go:TestPublishRejectsWrongSecret`, `TestPublishRejectsSuspendedTenant`, `TestSecondPublisherRejectedAndCannotTakeChannelOffline`; `store_test.go:TestMarkLiveIsExclusive` | tamam | |
| Y3 | Kabul edilirse kanal "yayında" | `store/queries.go:MarkLive` | `hooks_test.go:TestPublishAcceptsValidSecret`; `PANEL` | tamam | |
| Y4 | Yayın bitince kanal "çevrimdışı" | `hooks.go:onUnpublish`, `store/queries.go:MarkOffline` | `hooks_test.go:TestSecondPublisherRejectedAndCannotTakeChannelOffline`; `SC` "Yayın bitince" | tamam | |
| Y5 | Gizli anahtar izleyiciye giden hiçbir adreste yer almaz | `play.go:target`, `xtream.go:playlist/liveStreams` | `play_test.go` (`redirect` yardımcısı), `xtream_test.go:TestLiveStreams`, `TestPlaylist`; `SC` (yönlendirmede gizli değer yok) | tamam | |

### İzleme

| # | Madde | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| İ1 | Oynatıcı `/live/<k>/<ş>/<no>.ts` veya `.m3u8` ister | `play.go:Register/parseFile` | `play_test.go:TestShortAndExtensionlessForms`, `TestRejectsUnknownFileNames`; `cmd/kanalvo/routes_test.go:TestPublicRoutesDoNotShadowEachOther` | tamam | |
| İ2 | İzleyici, durumu, bitiş tarihi ve kanalın izleyicinin yayıncısına ait olması doğrulanır | `play.go:serve`, `store/models.go:Usable` | `play_test.go:TestRejectsInvalidViewer`, `TestOtherTenantChannelLooksMissing`, `TestOfflineChannelIsNotFound` | tamam | Son iki test bu PR'da `.m3u8` ve kısa biçime genişletildi |
| İ3 | Sağlıklı edge'lerden biri ağırlığa göre seçilir | `balancer.go:Choose` | `balancer_test.go:TestChooseDistributesByWeight` | tamam | |
| İ4 | İmzalı adresle 302 | `play.go:serve` | `play_test.go:TestTSRedirectsToRelayWithShortToken`, `TestHLSRedirectsToGatewayWithTokenInPath` | tamam | |
| İ4a | `.ts`: `<edge>/live/<no>.ts?token=<imza>`, imza birkaç dakika (5 dk) | `play.go:target`, `internal/config/config.go` (`TOKEN_TTL`) | `play_test.go:TestTSRedirectsToRelayWithShortToken`; `config_test.go:TestLoadDefaults` | tamam | |
| İ4b | `.ts`: imza yalnızca başlarken doğrulanır; `srs-ts` sorar, API imzayı ve limiti doğrular, oturumu açar; bitince kapanır | `hooks.go:onPlay/onStop`, `session/session.go:OpenTS/CloseTS` | `hooks_test.go:TestPlayOpensASessionAndStopClosesIt`, `TestPlayBeyondConnectionLimitKicksTheOldestConnection`, `TestPlayRejections`, `TestStartedPlaybackOutlivesItsToken` | tamam | "Yalnızca başlarken" kısmı bu PR'da sınandı (bkz. G4) |
| İ4c | `.m3u8`: `<edge>/hls/<imza>/<no>.m3u8`; imza yolda, göreli parça adresleri onu devralır | `play.go:target`, `hlsgw.go:Register` | `play_test.go:TestHLSRedirectsToGatewayWithTokenInPath`; `SC`; `KIR` (ffprobe göreli parça adreslerini kendisi izler) | tamam | |
| İ4d | Geçit her çalma listesi ve parça isteğinde imzayı, izleyicinin durumunu, kanalın yayında olmasını doğrular, sonra SRS'ten aktarır | `hlsgw.go:authorize` | `hlsgw_test.go:TestRejectsBadTokens`, `TestOfflineChannelIsNotFound`, `TestServesPlaylistAndSegmentFromUpstream` | tamam | |
| İ4e | HLS imza ömrü 6 saat; askıya alma ve yayının bitmesi birkaç saniyede etkili | `config.go` (`HLS_TOKEN_TTL`), `hlsgw.go:authorize` | `config_test.go:TestLoadDefaults`; `hlsgw_test.go:TestRejectsBadTokens`; `SC` (yayın bitince eski HLS adresi); `EDGE` adım 7 | tamam | |

### Edge

| # | Madde | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| E1 | Yerel edge kurulumla gelir, silinemez, adresleri ayarlardan okunur | `store/edges.go:SyncLocalEdge/DeleteEdge/UpdateEdge`, `cmd/kanalvo/main.go` | `store/edges_test.go:TestLocalEdgeExistsAndTakesItsAddressesFromSettings`, `TestLocalEdgeCannotBeDeletedOrReaddressed`; `panelapi/edges_test.go:TestLocalEdgeCanBeWeightedOrDisabledButNotDeletedOrReaddressed` | tamam | |
| E2 | Yönetici uzak edge ekler; anahtar kayıtta üretilir | `panelapi/edges.go:adminCreateEdge` | `panelapi/edges_test.go:TestAdminManagesEdges`; `EDGE`, `KES` | tamam | Tek seferlik kurulum komutu (`internal/enroll`) tasarımda anlatılmıyor; README'de var |
| E3 | Uzak edge ilk izleyicide yayını origin'den RTMP ile çeker, son izleyici gidince bırakır | `deploy/edge/srs-edge.conf.tmpl` (`cluster`, `mode remote`) | `EDGE` (izleme sürerken origin'in bağlantı listesinde edge'in adresi var; son izleyici kesilince 30 sn içinde yok) | bu PR'da kapatıldı | Bırakma önceden sınanmıyordu |
| E4 | Origin çekmeyi yalnızca kayıtlı ve etkin edge'in çekme adresinden, o kanalın `.ts` imzasıyla (süresine bakmadan) kabul eder | `hooks.go:onPlayOrigin`, `balancer.go:PullAllowed` | `hooks/edge_test.go:TestOriginPlayIsAllowedOnlyFromARegisteredEdgeAddress` (bu PR'da silinen edge vakası eklendi); `balancer_test.go:TestPullAllowedOnlyFromEnabledEdgeAddresses`; `EDGE` adım 2; `KES` (devre dışı edge yeni kanal çekemez) | tamam | |
| E5 | `.ts`: edge'in SRS'i `/edge/<anahtar>/hooks/…` sorar; kurallar aynı, oturum o edge'e yazılır | `hooks.go:RegisterEdge` | `hooks/edge_test.go:TestEdgePlayOpensAndClosesTheSessionOnThatEdge`, `TestEdgePlayAppliesTheSameRulesAsLocal`, `TestEdgeHooksRequireAValidEdgeKey`; `EDGE` adım 4 | tamam | |
| E6 | HLS: nginx her isteği `/edge/hls/<imza>/<dosya>` ile sorar (anahtar başlıkta); kendi önbelleğinden verir, yoksa bir kez çeker; liste 1 sn, parça 60 sn | `hlsgw.go:serveEdgeAuth/serveEdgeFile`, `deploy/edge/nginx.conf.template` | `hlsgw/edge_test.go:TestEdgeAuthAnswersWithAnInternalRedirect`, `TestEdgeSegmentServesTheFileWithCacheLifetimes`; `EDGE` adım 5 (`X-Cache-Status: HIT`) | tamam | Eşzamanlı ıskalamada tek istek nginx `proxy_cache_lock` ile; ayrıca sınanmıyor |
| E7 | Sağlık: her edge 5 sn'de bir sorulur; son 15 sn'de yanıt veren sağlıklıdır | `main.go` (`enforceInterval`, `edgeHealthWindow`), `session.go:enforceEdge`, `store/edges.go` | `store/edges_test.go:TestEdgeIsHealthyOnlyWhileItsSignalIsFresh`, `session_test.go:TestUnreachableEdgeLosesHealthWithoutBlockingOthers`; `EDGE` adım 8; `KES` (kopmadan 10-15 sn sonra yönlendirmeden çıkar) | tamam | Edge listesi 2 sn önbellekli: etkisi en geç ~17 sn |
| E8 | Yerel edge'in HLS'i `.ts` dağıtıcısından bağımsız, her zaman kullanılabilir | `balancer.go:eligible` | `balancer_test.go:TestLocalEdgeServesHLSEvenWithoutAHealthSignal`; `play/edge_test.go:TestNoHealthyEdgeIsServiceUnavailable` | tamam | |
| E9 | Seçim: etkin ve sağlıklı edge'ler arasından ağırlıkla orantılı rastgele | `balancer.go:Choose` | `balancer_test.go:TestChooseDistributesByWeight`, `TestChooseSkipsUnhealthyAndDisabledEdges` | tamam | |
| E10 | SRS çöker veya yeniden başlarsa yayın listesi düzenli sorulur, listede olmayan kanal çevrimdışı olur; SRS'e ulaşılamazsa dokunulmaz | `internal/reconcile/reconcile.go:RunOnce`, `store/queries.go:ReconcileLive` | `reconcile_test.go:TestRunOnceClearsStaleLive`, `TestRunOnceLeavesChannelsAloneWhenSRSFails`, `TestRunOnceLeavesChannelsAloneWhenSRSUnreachable` | tamam | |

### Xtream uçları

| # | Uç | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| X1 | `player_api.php` (eylemsiz): hesap ve sunucu bilgisi | `xtream.go:playerAPI` | `xtream_test.go:TestAccountInfo`, `TestAccountInfoReportsActiveConnections`, `TestLoginWithPOSTBody`; `SC`; `KIR` | tamam | |
| X2 | `get_live_categories` | `xtream.go:liveCategories` | `xtream_test.go:TestLiveCategories`; `KIR` | tamam | |
| X3 | `get_live_streams` | `xtream.go:liveStreams` | `xtream_test.go:TestLiveStreams`, `TestLiveStreamsCategoryFilter`; `SC`; `KIR` | tamam | |
| X4 | `get_vod_categories`, `get_vod_streams`, `get_series_categories`, `get_series`: boş liste | `xtream.go:playerAPI` | `xtream_test.go:TestEmptyAndUnsupportedActions` | tamam | |
| X5 | `get_short_epg`, `get_simple_data_table`: boş liste | `xtream.go:playerAPI` | `xtream_test.go:TestEmptyAndUnsupportedActions` | tamam | Yanıt Xtream biçimiyle `{"epg_listings":[]}` |
| X6 | `get.php`: M3U (`output=ts` / `m3u8`) | `xtream.go:playlist` | `xtream_test.go:TestPlaylist`, `TestPlaylistOutputAndType`, `TestPlaylistEscapesNamesAndCredentials`; `KIR` (iki biçim, listedeki adresten görüntü) | tamam | |
| X7 | `xmltv.php`: boş program rehberi | `xtream.go:guide` | `xtream_test.go:TestGuideIsEmptyXMLTV` | tamam | |
| X8 | `/live/<k>/<ş>/<no>.<uzantı>` ve `/<k>/<ş>/<no>`: edge'e imzalı yönlendirme | `play.go:Register/serve` | `play_test.go:TestShortAndExtensionlessForms`; `SC` (kısa adres) | tamam | |

### Panel

| # | Yetenek | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| P1 | Yönetici: yayıncı oluşturma | `panelapi/admin.go:adminCreateTenant` | `panelapi_test.go:TestAdminManagesTenants`; `PANEL` | tamam | |
| P2 | Yönetici: askıya alma | `admin.go:adminUpdateTenant` | `panelapi_test.go:TestSuspendedTenantIsLockedOut`; `PANEL` | tamam | |
| P3 | Yönetici: kota belirleme | `admin.go:adminUpdateTenant` | `panelapi_test.go:TestAdminManagesTenants` | tamam | |
| P4 | Yönetici: edge kaydetme, ağırlık, devre dışı bırakma, silme, durum görme | `panelapi/edges.go` | `panelapi/edges_test.go:TestAdminManagesEdges`, `TestEdgeInputIsValidated`; `EDGE`, `KES` | tamam | |
| P5 | Yönetici: platform geneli canlı kanal ve oturum sayıları | `admin.go:adminStats`, `store/accounts.go:PlatformStats` | `store/panel_test.go:TestListTenantsAndStats`; `panelapi_test.go:TestAdminStatsCountLiveChannelsAndSessions` | bu PR'da kapatıldı | HTTP yanıtı (`live_channels`, `active_sessions`) önceden sınanmıyordu |
| P6 | Yayıncı: kategori ve kanal yönetimi | `panelapi/tenant.go` | `panelapi_test.go:TestCategories`, `TestChannelLifecycle` | tamam | Kategori sırasını değiştiren uç yok (veri modelinde "sıra" alanı var) |
| P7 | Yayıncı: OBS ayarlarını görme | `tenant.go:toChannelJSON` | `panelapi_test.go:TestChannelLifecycle` | tamam | |
| P8 | Yayıncı: yayın anahtarı yenileme | `tenant.go:regenerateSecret` | `panelapi_test.go:TestChannelLifecycle` | tamam | |
| P9 | Yayıncı: izleyici oluşturma, askıya alma, şifre yenileme | `tenant.go:createViewer/updateViewer/regeneratePassword` | `panelapi_test.go:TestViewerLifecycle`, `TestRegeneratingAViewerPasswordEndsRunningPlayback` | tamam | |
| P10 | Yayıncı: kendi canlı oturumlarını görme | `tenant.go:listSessions` | `panelapi_test.go:TestViewerLifecycle`, `TestSessionListIsPaged`; `PANEL`; `KIR` | tamam | |

## Kiracı ayrımı matrisi

§1.4 ve §8 "kiracı ayrımı her uç için ayrıca sınanır". Panel rotaları `internal/panelapi/panelapi.go:Register`.

| Uç | Başka yayıncıya karşı test | Durum |
|---|---|---|
| `GET /api/tenant/overview` | `panelapi_test.go:TestTenantsCannotTouchEachOthersRecords` (B'nin izleyicisi ve oturumu A'nın sayılarına girmez) | bu PR'da kapatıldı |
| `GET/POST/PATCH/DELETE /api/tenant/categories…` | `TestTenantsCannotTouchEachOthersRecords` (POST'ta `tenant_id` → 400 bu PR'da eklendi); `KIR` | tamam |
| `GET/POST/PATCH/DELETE /api/tenant/channels…`, `regenerate-secret` | `TestTenantsCannotTouchEachOthersRecords`, `TestCreateChannelWithForeignCategoryLeavesNothingBehind`; `KIR` | tamam |
| `GET/POST/PATCH/DELETE /api/tenant/viewers…`, `regenerate-password`, `?q=` | `TestTenantsCannotTouchEachOthersRecords`, `TestViewerListIsPagedAndSearchable`; `KIR` | tamam |
| `GET /api/tenant/sessions` | `TestTenantsCannotTouchEachOthersRecords`; `KIR` (iki yayıncı aynı anda izlenirken) | tamam |
| `GET /api/admin/*` (yayıncı rolüyle) | `panelapi_test.go:TestRolesAreSeparated`, `TestAdminStatsCountLiveChannelsAndSessions`; `KIR` | tamam |
| Xtream `get_live_categories`, `get_live_streams`, `get.php` | `xtream_test.go:TestLiveCategories`, `TestLiveStreams`, `TestPlaylistAndGuideRequireValidLogin`; `KIR` | tamam |
| `/live/<k>/<ş>/<no>.ts`, `.m3u8`, `/<k>/<ş>/<no>` | `play_test.go:TestOtherTenantChannelLooksMissing` (bu PR'da üç biçime genişletildi); `KIR` | bu PR'da kapatıldı |
| `/hls/<imza>/<dosya>` (yerel geçit) | `hlsgw_test.go:TestRejectsBadTokens` "başka yayıncının izleyicisi"; `KIR` (imza başka kanala taşınamaz) | tamam |
| `/edge/hls/<imza>/<dosya>` | `hlsgw/edge_test.go:TestEdgeAuthAppliesTheSameRulesAsTheLocalGateway` (bu PR'da eklendi) | bu PR'da kapatıldı |
| `/hooks/srs/<sır>/play` (yerel `.ts`) | `hooks_test.go:TestPlayRejections` "başka yayıncının izleyicisi"; `KIR` (`.ts` imzası başka kanalda yayın döndürmez) | tamam |
| `/edge/<anahtar>/hooks/play` | `hooks/edge_test.go:TestEdgePlayAppliesTheSameRulesAsLocal` (bu PR'da eklendi) | bu PR'da kapatıldı |
| `/edge/segment/<dosya>` | Yalnızca edge anahtarı denetlenir; edge güvenilir altyapıdır (tasarım gereği) | tamam |
| `GET /api/me`, `POST /api/password`, `POST /api/logout`, `player_api.php` (eylemsiz), vod/series/epg, `xmltv.php` | Kimlik çerezden ya da girişten gelir; dışarıdan numara almaz ya da sabit boş yanıt verir | uygulanmaz |

## §6 Güvenlik

| # | Kural | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| G1 | Yayıncı kimliği sunucuda oturumdan alınır; istekteki kimliğe güvenilmez | `panelapi.go:as`, `decode` (`DisallowUnknownFields`) | `TestTenantsCannotTouchEachOthersRecords` (üç oluşturma ucunda `tenant_id` → 400); `KIR` | tamam | |
| G2a | Panel izleyici uçlarından ayrı portta (8002) | `config.go`, `main.go` (ayrı mux) | `config_test.go:TestLoadDefaults`; `PANEL` (izleyici portunda `/api/me` yok) | tamam | |
| G2b | Çerez `HttpOnly`, `SameSite=Strict`, `Secure` | `panelapi.go` (çerez) | `panelapi_test.go:TestLoginSetsAHardenedCookie` | tamam | |
| G2c | Değişiklik yapan istekler özel başlık taşır | `panelapi.go:as` (`X-Kanalvo-Panel`) | `panelapi_test.go:TestChangesRequireThePanelHeader` | tamam | |
| G2d | Şifre değişince, sıfırlanınca, yayıncı askıya alınınca eski oturumlar kapanır | `panelapi.go:changePassword`, `admin.go:adminResetPassword`, `store/accounts.go:UpdateTenant` | `TestPasswordChange`, `TestAdminPasswordChangeEndsOtherAdminSessions`, `TestSuspendedTenantIsLockedOut`; `cmd/kanalvo/admin_test.go`; `PANEL` | tamam | |
| G3 | Yayıncı panel şifresini sistem üretir, bir kez gösterir; yayıncılar silinmez, askıya alınır | `admin.go:adminCreateTenant`; silme ucu yok | `TestAdminManagesTenants` | tamam | |
| G4 | İmza izleyici, kanal ve bitiş içerir, HMAC-SHA256; başlamış `.ts` süre dolunca kesilmez; HLS 6 saat sonra yeniden istenir | `internal/token/token.go`, `store/sessions.go:TSSessionsToKick` | `token_test.go`; `hlsgw_test.go:TestRejectsBadTokens` "süresi dolmuş"; `hooks_test.go:TestStartedPlaybackOutlivesItsToken` | bu PR'da kapatıldı | Süresi dolan imzayla süren `.ts` izlemesinin denetim geçişinde kesilmediği önceden sınanmıyordu |
| G5 | İmzalı adres süresi içinde paylaşılabilir; bağlantı limiti sınırlar; IP'ye bağlama sonraya | `store/sessions.go:admit/TouchHLSSession` | §7 limit satırları | tamam | Tasarımdaki "bağlantı limiti gelene kadar (Plan 3) açıktır" cümlesi eskimişti, bu PR'da düzeltildi |
| G6 | Origin SRS'in HTTP portu dışarıya açılmaz; HLS yalnızca geçitten | `docker-compose.yml` (`srs` yalnızca RTMP) | `SC` (origin'in çalma listesi ve parçası erişilemez) | tamam | |
| G7 | İmza türünü taşır; HLS imzası `.ts` ve RTMP'de geçmez | `token.go` (`Kind`), `hooks.go`, `hlsgw.go` | `hooks_test.go:TestPlayRejectsHLSToken`; `hooks/edge_test.go:TestOriginPlayIsAllowedOnlyFromARegisteredEdgeAddress`; `hlsgw_test.go:TestRejectsBadTokens` | tamam | |
| G8 | SRS sorguları ayrı portta (8001), dışarıya açılmaz | `config.go`, `main.go`, compose (8001 yayımlanmaz) | `routes_test.go`; `SC` | tamam | |
| G9 | SRS reddettiği sorguları loga yazar; SRS logları gizli veri sayılır | `deploy/srs/*.tmpl` (`warn`) | `SC` (kabul edilen yayın ve izlemeler loglara gizli değer bırakmaz) | tamam | |
| G10 | Xtream uçlarında IP başına hatalı giriş sınırı | `internal/auth/auth.go:Viewer`, `internal/ratelimit` | `auth_test.go:TestBlocksIPAfterMaxFailures`; `xtream_test.go`/`play_test.go:TestTooManyFailedLoginsReturn429` | tamam | |
| G11 | SRS sorguları yalnızca iç ağdan ve paylaşılan sırla | `hooks.go:Register` | `hooks_test.go:TestRejectsWrongHookSecretAndUnknownEvent` | tamam | "İç ağ" dağıtımla sağlanır |
| G12 | Uzak edge kendini anahtarıyla tanıtır (SRS'te yolda, nginx'te `X-Edge-Key`); anahtarsız istek reddedilir | `hooks.go:RegisterEdge`, `hlsgw.go`, `balancer.go:ByKey` | `hooks/edge_test.go:TestEdgeHooksRequireAValidEdgeKey`; `hlsgw/edge_test.go:TestEdgeEndpointsRequireAValidEdgeKey`; `EDGE` adım 6 | tamam | |
| G13 | İzleyici IP başlığına yalnızca geçerli edge anahtarıyla güvenilir | `hlsgw.go:serve/serveEdgeAuth` | `hlsgw/edge_test.go:TestViewerCannotSpoofItsAddressOnTheLocalGateway`; `EDGE` adım 4 ve 6 | tamam | |
| G14 | Edge'in SRS yönetim API'si nginx'te anahtarla korunur; SRS portları kapalı | `deploy/edge/nginx.conf.template`, `deploy/edge/docker-compose.yml` | `EDGE` adım 6 | tamam | |
| G15 | Edge anahtarı ve denetim trafiği HTTP'de açık; özel ağ ya da HTTPS; yönetim adresi ayrı tutulabilir | `store/edges.go` (`control_url`) | `panelapi/edges_test.go:TestControlAddressFollowsTheViewerAddressUnlessSetSeparately`; `EDGE` (`E2E_EDGE_CONTROL_URL` ile HTTPS) | tamam | |
| G16 | Panel yalnızca HTTPS üzerinden sunulur; Xtream HTTP'den de erişilebilir | `docker-compose.yml` (`caddy` servisi; `api`'nin 8002'si yalnızca `127.0.0.1`), `deploy/caddy/Caddyfile` (`PANEL_DOMAIN`: gerçek alan adında ACME, `localhost`'ta iç CA); Xtream (8000) ve `.ts` (8081) HTTP'de | `HTTPS` (iç CA'ya güvenerek HTTPS'ten giriş, `Secure`/`HttpOnly` çerez, düz HTTP'nin HTTPS'e yönlenmesi, 8002'nin dış arayüzden kapalı olması) | kapatıldı | Bkz. [A1](#a1-panel-https-i-ve-ters-vekil). Gerçek alan adıyla ACME sınanmadı (simülasyonda alan adı yok) |

## §7 Hata durumları

| # | Durum | Kod | Test | Durum | Not |
|---|---|---|---|---|---|
| H1 | Kanal çevrimdışıyken izleme → 404 | `play.go:serve`, `hlsgw.go:authorize` | `play_test.go:TestOfflineChannelIsNotFound` (bu PR'da `.m3u8` eklendi); `hlsgw_test.go:TestOfflineChannelIsNotFound`; `SC` | tamam | |
| H2 | Geçersiz kullanıcı adı veya şifre → `auth: 0`; yayın isteğinde 403 | `xtream.go:playerAPI`, `play.go:serve` | `xtream_test.go:TestInvalidLoginIsAuthZero`; `play_test.go:TestRejectsInvalidViewer`; `SC` | tamam | |
| H3 | Süresi dolmuş/askıdaki izleyici, askıdaki yayıncının izleyicisi → `auth: 1` + `Expired`/`Banned`/`Disabled`; listeler boş; yayın ve M3U 403 | `xtream.go`, `play.go` | `xtream_test.go:TestUnusableViewer`; `play_test.go:TestRejectsInvalidViewer`; `SC` (`Disabled`) | tamam | |
| H4 | Aynı IP'den çok hatalı giriş → pencere bitene kadar 429 | `auth.go:Viewer` | `auth_test.go:TestBlocksIPAfterMaxFailures`; `ratelimit_test.go:TestUnblocksWhenWindowEnds`; `TestTooManyFailedLoginsReturn429`; `HTTPS` (panelde Caddy arkasında: kilit vekile değil istemciye uygulanır, uydurulan `X-Forwarded-For` kilidi bölmez) | tamam | |
| H5 | İzleyici başka yayıncının kanalını ister → 404 | `play.go:serve` | `play_test.go:TestOtherTenantChannelLooksMissing` (üç biçim); `KIR` | bu PR'da kapatıldı | Önceden yalnızca `.ts` biçimi sınanıyordu |
| H6 | İzleyicinin bağlantı limiti dolu → yeni izleme kabul, en eski kesilir | `store/sessions.go:admit`, `session.go:kickEvicted` | `hooks_test.go:TestPlayBeyondConnectionLimitKicksTheOldestConnection`; `store/sessions_test.go`; `hlsgw_test.go:TestNewSessionEndsTheOlderOneWhenLimitIsReached`; `SC` | tamam | |
| H7 | Yayıncının toplam bağlantı kotası dolu → yeni izleyici 403; süren izlemeler etkilenmez | `store/sessions.go`, `hooks.go:onPlay`, `hlsgw.go` | `hooks_test.go:TestPlayRejectedWhenTenantConnectionQuotaIsFull`; `hlsgw_test.go:TestTenantConnectionQuotaFull`; `store/sessions_test.go:TestConcurrentOpensCannotExceedTenantQuota` | tamam | `.ts`'te 403 SRS'e döner; izleyici HTTP 403 değil bağlantının kapanmasını görür (SRS davranışı) |
| H8 | HLS adresi başka ağdan açıldı → oturum taşınır, eski ağ 403; 1 dk dolmadan geri taşınamaz | `store/sessions.go:TouchHLSSession` | `store/sessions_test.go:TestHLSLinkMovesToANewAddressButNotBackAndForth`; `hlsgw_test.go:TestSharedLinkFromAnotherAddressTakesTheOnlySlot`; `hlsgw/edge_test.go:TestEdgeReportedViewerAddressIsTheSessionNetwork` | tamam | `TestSharedLinkFromAnotherAddressTakesTheOnlySlot`'un eski davranışı anlatan hata iletileri bu PR'da düzeltildi |
| H9 | Yerinden edilmiş HLS adresi → her ağdan 403 | `store/sessions.go` | `store/sessions_test.go:TestEvictedHLSLinkIsDeadFromEveryAddress`; `SC` | tamam | |
| H10 | Edge sağlık sinyali kesildi → yönlendirmeden çıkar, dönünce geri eklenir | `balancer.go:eligible` | `balancer_test.go:TestPickFollowsHealthSignal`; `play/edge_test.go:TestEdgeWithoutHealthSignalGetsNoViewers`; `EDGE` adım 8; `KES` (ağ kopması ve dönüşü) | tamam | |
| H11 | Sağlıklı edge yok → 503 | `play.go:serve` | `play/edge_test.go:TestNoHealthyEdgeIsServiceUnavailable`; `EDGE` adım 8 | tamam | |
| H12 | "İzleme bitti" bildirimi kayboldu → `.ts` oturumları 5 sn'de bir SRS'le eşitlenir; SRS'e ulaşılamazsa silinmez | `session.go:enforceEdge`, `store/sessions.go:ReconcileTSSessions` | `session_test.go:TestEnforceRemovesSessionsMissingFromSRS`; `store/sessions_test.go:TestReconcileTSSessions`; `KES` (kısa kopuklukta oturum korunur) | tamam | |
| H13 | İzleyici askıya alındı veya süresi doldu → süren `.ts` birkaç saniyede kesilir; HLS sonraki istekte reddedilir | `store/sessions.go:TSSessionsToKick`, `hlsgw.go` | `session_test.go:TestEnforceKicksRevokedAndUnentitledConnections`; `EDGE` adım 7 | tamam | |
| H14 | Origin'e RTMP ile izleme denemesi → edge çekme adresi dışından geçerli imzayla bile, edge adresinden imzasız da reddedilir | `hooks.go:onPlayOrigin` | `hooks_test.go:TestOriginPlayIsRejectedForViewers`; `hooks/edge_test.go:TestOriginPlayIsAllowedOnlyFromARegisteredEdgeAddress`; `SC`; `EDGE` adım 6 | tamam | |
| H15 | Edge 2 dakikadan uzun ulaşılamıyor → `.ts` oturumları silinir; yalnızca denetim koptuysa süren izlemeler dönünce sayılmaz ve kesilemez | `session.go` (`deadEdgeAfter`), `store/edges.go:DeleteTSSessionsOfUnreachableEdges` | `session_test.go:TestSessionsOnALongUnreachableEdgeStopCounting`; `KES` (kopukluktan ~2 dk sonra silinir; dönüşte süren izleme sürer ve sayılmaz) | bu PR'da kapatıldı | İkinci cümle uçtan uca önceden sınanmıyordu |
| H16 | Edge devre dışı bırakıldı → yeni izleyici yönlendirilmez; süren izlemeler devam eder; origin'den yeni çekme başlatamaz | `balancer.go:eligible/ByKey/PullAllowed` | `play/edge_test.go:TestDisabledEdgeGetsNoViewers`; `balancer_test.go`; `KES` (`.ts` ve HLS izlemesi sürer, yeni kanal çekilemez, yeniden etkinleşince çekilir) | bu PR'da kapatıldı | "Süren izlemeler devam eder" önceden yalnızca `ByKey` düzeyinde sınanıyordu |
| H17 | Edge silindi → oturum kayıtları silinir; süren izlemeler kesilmez; yeni izleme yetkilendirilemez | `store/edges.go:DeleteEdge` | `store/edges_test.go:TestDeletingAnEdgeRemovesItsSessionsAndKicks`; `hooks/edge_test.go:TestDeletedEdgeCannotAuthorizeNewPlayback`; `hlsgw/edge_test.go:TestDeletedEdgeKeyIsRejected`; `TestOriginPlayIsAllowedOnlyFromARegisteredEdgeAddress` (silinen edge çekemez) | bu PR'da kapatıldı | Silinen edge'in anahtarının reddi önceden sınanmıyordu |
| H18 | Geçersiz edge anahtarıyla istek → izleme sorgusu 404, HLS uçları 403 | `hooks.go:RegisterEdge`, `hlsgw.go` | `hooks/edge_test.go:TestEdgeHooksRequireAValidEdgeKey`; `hlsgw/edge_test.go:TestEdgeEndpointsRequireAValidEdgeKey` | tamam | |
| H19 | Kanal veya izleyici silindi, anahtar ya da şifre yenilendi → ilgili yayın ve izlemeler kesilir; kesme başarısızsa kuyrukta yeniden denenir | `store/manage.go`, `session.go:Flush/kickAll` | `panelapi_test.go:TestChannelLifecycle`, `TestViewerLifecycle`, `TestKickFailureOnDeleteIsRetriedLater`; `store/kicks_test.go`; `session_test.go:TestQueuedKicksAreRetriedUntilTheySucceed`; `PANEL` | tamam | |
| H20 | Çok sayıda eşzamanlı panel girişi → sınırlı sayıda doğrulanır; fazlası 503 | `internal/passhash/gate.go`, `panelapi.go:hashError` | `passhash/gate_test.go`; `panelapi_test.go:TestLoginIsBusyWhenThePasswordGateIsFull` (503 + `Retry-After`) | bu PR'da kapatıldı | HTTP eşlemesi önceden sınanmıyordu; test için `export_test.go` ile kapı değiştirilebilir |
| H21 | Yayıncı askıya alındı → açık yayınları birkaç saniyede kesilir, izleyicileri izleyemez | `store/sessions.go:SuspendedLivePublishers`, `session.go:pass` | `session_test.go:TestEnforceKicksRevokedAndUnentitledConnections`; `SC`; `PANEL` | tamam | |
| H22 | Geçersiz yayın anahtarı veya **kota aşımı** → SRS yayını reddeder; kanal kotası eşzamanlı canlı kanal üst sınırıdır, kota düşürülünce süren yayınlar kesilmez | `hooks.go:onPublish`, `store/queries.go:MarkLive` | `hooks_test.go:TestPublishRejectsWrongSecret`, `TestPublishRejectedBeyondConcurrentChannelQuota`, `TestConcurrentPublishesAcceptOnlyUpToTheChannelQuota`; `store/store_test.go:TestMarkLiveEnforcesConcurrentChannelQuota`, `TestConcurrentMarkLiveCannotExceedChannelQuota`; `KOTA` | kapatıldı (A2 PR'ı) | Bkz. [A2](#a2-yayın-kabulünde-kota) |
| H23 | Aynı kanala ikinci yayın bağlantısı → ikincisi reddedilir | `store/queries.go:MarkLive` | `hooks_test.go:TestSecondPublisherRejectedAndCannotTakeChannelOffline` | tamam | |

## §8 Test

| # | Madde | Test | Durum | Not |
|---|---|---|---|---|
| T1 | Birim: imza, edge seçimi, limit ve kota hesapları, Xtream yanıt biçimleri | `internal/token`, `internal/balancer`, `internal/store/sessions_test.go`, `internal/xtream` | tamam | |
| T2 | Entegrasyon: gerçek PostgreSQL'e karşı API uçları | `panelapi`, `xtream`, `play`, `hlsgw`, `hooks` paketleri (`internal/testdb`) | tamam | |
| T2b | Kiracı ayrımı her uç için ayrıca sınanır | [Kiracı ayrımı matrisi](#kiracı-ayrımı-matrisi) | bu PR'da kapatıldı | Eksik beş uç (overview, oluşturmada `tenant_id`, `/live` `.m3u8` ve kısa biçim, `/edge/hls`, `/edge/…/hooks/play`) eklendi |
| T3 | Uçtan uca: Compose kalkar, FFmpeg sahte yayın gönderir, `.ts` ve `.m3u8`'den görüntü geldiği doğrulanır | `e2e/sim.sh`; `SC`, `EDGE`, `KES`, `KIR`, `PANEL` | bu PR'da kapatıldı | Önceden ortam elle kuruluyor, görüntü yalnızca eşitleme baytlarıyla doğrulanıyordu. Artık tek komut ve ffprobe ile kare çözme var (`docs/simulasyon.md`) |
| T4 | Elle: OBS ile yayın, en az iki gerçek oynatıcıda izleme | — | **açık** | Bkz. [A3](#a3-elle-deneme) |
| T5 | (Tasarımda adı geçen) ikinci sunucuyla deneme | `EDGE`, `KES` (aynı makinede ayrı Compose projesi; kapanma, ağ kopması, devre dışı bırakma) | tamam | |

## Sayısal süreler ve durum kodları

| Tasarım | Kod | Eşleşme |
|---|---|---|
| Sağlık sorgusu 5 sn, sağlıklı penceresi 15 sn | `cmd/kanalvo/main.go` (`enforceInterval`, `edgeHealthWindow = 3 × enforceInterval`) | evet; edge listesi 2 sn önbellekli, `KES`'te ölçülen: 10-15 sn |
| Boşta HLS oturumu 30 sn | `main.go` (`hlsSessionIdle`) | evet |
| HLS ağ taşıma beklemesi 1 dk | `store/sessions.go` (`hlsMoveCooldown`) | evet |
| Ulaşılamayan edge 2 dk | `session/session.go` (`deadEdgeAfter`) | evet; `KES`'te ölçülen: ~2 dk (son sinyalden) |
| HLS imzası 6 sa, `.ts` imzası 5 dk | `config/config.go` | evet |
| Edge önbelleği 1 sn / 60 sn | `hlsgw/hlsgw.go` | evet |
| Panel 8002, SRS sorguları 8001 | `config/config.go` | evet |
| 404 / 403 / 429 / 503 (§7) | `play.go`, `hlsgw.go`, `xtream.go`, `hooks.go`, `panelapi.go` | evet; `.ts`'te kota reddi izleyiciye HTTP 403 olarak ulaşmaz (H7 notu) |

## Bu PR'da yapılanlar

- **Simülasyon ortamı:** `e2e/sim.sh` (tek komut, ayrı Compose projesi ve portlar, sonunda
  kaldırma); `docker-compose.yml` portları ve `deploy/edge/docker-compose.e2e.yml` ağ adı ortam
  değişkeniyle değişir; e2e testleri adresleri ortamdan okur (`e2e/ortam_test.go`).
- **Oynatıcı istemcisi:** `e2e/oynatici_test.go` (Xtream uçları + ffprobe ile kare çözme).
- **Yeni uçtan uca senaryolar:** `KIR` (iki yayıncıyla kiracı ayrımı), `KES` (edge devre dışı
  bırakma ve denetim bağlantısının kopması), `EDGE`'e edge'in çekmeyi bırakması.
- **Yeni birim/entegrasyon testleri:** `TestDeletedEdgeCannotAuthorizeNewPlayback`,
  `TestDeletedEdgeKeyIsRejected`, `TestStartedPlaybackOutlivesItsToken`,
  `TestLoginIsBusyWhenThePasswordGateIsFull`, `TestAdminStatsCountLiveChannelsAndSessions`; var olan
  testlere başka yayıncı, silinen edge, `.m3u8` ve kısa biçim vakaları.
- **Belge:** tasarımdaki eskimiş "Plan 3" cümlesi; `docs/simulasyon.md`; README'nin test bölümü.

## Açık maddeler

### A1. Panel HTTPS'i ve ters vekil

**Durum: kapatıldı.** §6 "Panel yalnızca HTTPS üzerinden sunulur" ve §3 "Ters vekil (Caddy)… tüm
bileşenler tek komutta kalkar". Denetim sırasında depoda Caddy yoktu ve `docker-compose.yml` paneli
(8002) bütün arayüzlerde düz HTTP ile yayımlıyordu; `Secure` çerez HTTP'de oturumu taşımasa da giriş
isteği (şifre) düz HTTP ile gidebiliyordu.

Yapılan:

- `docker-compose.yml`'e `caddy` servisi (`caddy:2.11.1-alpine`, 80 ve 443) eklendi;
  `docker compose up` onu da kaldırır. Yapılandırma `deploy/caddy/Caddyfile`'dadır.
- Alan adı `PANEL_DOMAIN`'den gelir. Gerçek bir alan adında Caddy sertifikayı ACME ile alır;
  `localhost`'ta (verilmezse varsayılan budur) iç CA'sını kullanır ("tls internal"). Caddy bu
  seçimi alan adına göre kendisi yapar; tek bir Caddyfile hem ACME'yi hem iç CA'yı karşılar.
- `api`'nin 8002 portu varsayılan olarak yalnızca `127.0.0.1`'e bağlanır (`KANALVO_PANEL_BIND`);
  panel dışarıya yalnızca Caddy'den gider. Xtream (8000) ve `.ts` (8081) tasarım gereği HTTP'de kalır.
- Compose'da `PANEL_TRUST_PROXY_HEADERS` varsayılanı `true`, `PANEL_INSECURE_COOKIE` `false`; `.env.example`
  ve README buna göre güncellendi.
- Simülasyon Caddy'yi de kaldırır; panel testleri HTTPS'ten, Caddy'nin iç CA'sına güvenerek koşar.
  Yeni senaryolar: `HTTPS` (bkz. kısaltmalar).

Kalan: gerçek bir alan adıyla ACME sertifikası alınması bu ortamda sınanamaz; ilk kurulumda
`docker compose logs caddy` ile bakılmalı.

### A2. Yayın kabulünde kota

**Kapatıldı.** §7 "Geçersiz yayın anahtarı veya kota aşımı → SRS yayını reddeder". Önceden
`internal/hooks/hooks.go:onPublish` gizli anahtarı, yayıncının durumunu ve tekil yayını denetliyor,
hiçbir kotaya bakmıyordu; kanal kotası yalnızca kanal oluştururken uygulanıyordu
(`internal/store/quotas.go`), yönetici kotayı düşürünce fazla kanallar yayın açmaya devam ediyordu.

Karar: yayın kabulünde kanal kotası (`max_channels`) **eşzamanlı canlı kanal üst sınırıdır**.
Yayıncının o anda canlı kanal sayısı kotaya eşit ya da fazlaysa yeni yayın reddedilir; hangi kanalın
"fazla" olduğu seçilmez, ilk gelen açar. Kota 0 ise kanal oluşturmadaki kuralla (`count >= limit`)
aynı biçimde hiç yayın açılamaz. Kota düşürülünce süren yayınlara dokunulmaz, yalnız yenileri
reddedilir. Tasarım §7 tablosuna iki satır eklendi.

Uygulama: `store/queries.go:MarkLive` tekil yayın denetimiyle kota denetimini tek işlemde yapar;
yayıncı satırını `FOR UPDATE` ile kilitler (kanal oluşturmadaki `insertWithinQuota` gibi), canlı
kanalları sayar ve yer varsa kanalı işaretler. Aynı yayıncının eşzamanlı iki yayını kotayı
aşamaz. Kota reddi `ErrQuotaExceeded` döner, `onPublish` 403 verir ve loga yazar. Panelde
reddedilen yayın için ayrı bir iz (olay, durum) yoktu; yeni arayüz eklenmedi. Yayıncı panelinde
kota ve canlı kanal sayısı zaten görünür.

Sınanan: `store/store_test.go:TestMarkLiveEnforcesConcurrentChannelQuota` ve
`TestConcurrentMarkLiveCannotExceedChannelQuota`; `hooks_test.go:TestPublishRejectedBeyondConcurrentChannelQuota`
ve `TestConcurrentPublishesAcceptOnlyUpToTheChannelQuota`; uçtan uca `e2e/kota_test.go`
(`KOTA`): kotası 2 olan yayıncının FFmpeg ile açılan üçüncü yayını SRS'ten reddedilir, biri
kapanınca üçüncü açılır, kota 1'e düşürülünce süren iki yayın sürer ve yenisi reddedilir.

Bilinen sınır: SRS çökerse "canlı" işareti eşitleme döngüsü (`ReconcileLive`) temizleyene kadar
kotadan düşer; bu, aynı kanala ikinci yayın reddiyle aynı davranıştır.

### A3. Elle deneme

§8 "Elle: OBS ile yayın, en az iki gerçek oynatıcıda izleme". `docs/srs-findings.md` bunun henüz
yapılmadığını söylüyor. Simülasyon (FFmpeg + ffprobe) protokol düzeyinde aynı yolları sınar ama
gerçek OBS'in ve TiviMate/IPTV Smarters/VLC'nin davranışının yerine geçmez. Gerçek bir sunucu ve
cihaz gerektirdiği için bu ortamda yapılamaz.

### Küçük notlar (bu PR'da yapılmadı)

- Tek seferlik edge kurulum komutu (`internal/enroll`, panelde "Sunucu ekle") tasarım belgesinde
  anlatılmıyor; README'de var.
- Kategori sırasını değiştiren panel ucu yok (veri modelinde alan var).
- Süre sınırları (30 sn boşta, 1 dk taşıma) testlerde sınırın uzağındaki değerlerle sınanıyor;
  `main.go` sabitlerini tasarım değerlerine bağlayan bir test yok.
