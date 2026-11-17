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

	"kanalvo/internal/session"
	"kanalvo/internal/store"
	"kanalvo/internal/token"
)

const app = "live"

// Event, SRS'in gönderdiği gövdenin kullandığımız alanlarıdır.
type Event struct {
	ClientID string `json:"client_id"`
	IP       string `json:"ip"`
	App      string `json:"app"`
	Stream   string `json:"stream"`
	Param    string `json:"param"`
}

// Edges, uzak edge'leri tanır (bkz. balancer.Balancer).
type Edges interface {
	ByKey(ctx context.Context, key string) (store.Edge, bool, error)
	PullAllowed(ctx context.Context, ip string) (bool, error)
}

// Origin, yayıncıların bağlandığı origin SRS'in yönetim API'sidir (bkz. srsapi.Client).
type Origin interface {
	// ClientIDs, SRS'e bağlı tüm bağlantıların (yayıncı, izleyici, edge çekmesi) kimlikleridir.
	ClientIDs(ctx context.Context) ([]string, error)
}

// originTimeout, yayın sorgusu sırasında origin'in bağlantı listesini beklemek için en uzun süredir.
// SRS, yayıncıyı bu sorgunun yanıtını alana kadar bekletir.
const originTimeout = 2 * time.Second

type Handler struct {
	store    *store.Store
	signer   *token.Signer
	sessions *session.Manager
	edges    Edges
	origin   Origin
	now      func() time.Time
}

func New(s *store.Store, signer *token.Signer, sessions *session.Manager, edges Edges, origin Origin, now func() time.Time) *Handler {
	return &Handler{store: s, signer: signer, sessions: sessions, edges: edges, origin: origin, now: now}
}

// Register, kontrol sunucusundaki SRS'lerin çağırdığı olayları kaydeder. Bu uçlar dışarıya
// açılmayan adreste dinlenir; localEdgeID, yerel .ts dağıtıcısının edge numarasıdır.
//
//	publish, unpublish : origin'e gelen yayın
//	play, stop         : yerel .ts dağıtıcısındaki izleme
//	play-origin        : origin'den RTMP ile izleme; yalnızca kayıtlı bir edge'in çekmesi kabul edilir
func (h *Handler) Register(mux *http.ServeMux, secret string, localEdgeID int64) {
	events := map[string]func(context.Context, Event) int{
		"publish":     h.onPublish,
		"unpublish":   h.onUnpublish,
		"play":        func(ctx context.Context, ev Event) int { return h.onPlay(ctx, localEdgeID, ev) },
		"stop":        func(ctx context.Context, ev Event) int { return h.onStop(ctx, localEdgeID, ev) },
		"play-origin": h.onPlayOrigin,
	}
	mux.HandleFunc("POST /hooks/srs/{secret}/{event}", func(w http.ResponseWriter, r *http.Request) {
		handle, known := events[r.PathValue("event")]
		if !known || subtle.ConstantTimeCompare([]byte(r.PathValue("secret")), []byte(secret)) != 1 {
			http.NotFound(w, r)
			return
		}
		serve(w, r, handle)
	})
}

// RegisterEdge, uzak edge'lerin SRS'lerinin çağırdığı izleme olaylarını kaydeder (play, stop).
// Bu uçlar izleyicilere açık adreste dinlenir; edge, adres yolundaki anahtarıyla tanınır
// (SRS sorgulara başlık ekleyemez).
func (h *Handler) RegisterEdge(mux *http.ServeMux) {
	mux.HandleFunc("POST /edge/{key}/hooks/{event}", func(w http.ResponseWriter, r *http.Request) {
		event := r.PathValue("event")
		if event != "play" && event != "stop" {
			http.NotFound(w, r)
			return
		}
		edge, known, err := h.edges.ByKey(r.Context(), r.PathValue("key"))
		if err != nil {
			respond(w, failure(err))
			return
		}
		if !known {
			http.NotFound(w, r)
			return
		}
		serve(w, r, func(ctx context.Context, ev Event) int {
			if event == "play" {
				return h.onPlay(ctx, edge.ID, ev)
			}
			return h.onStop(ctx, edge.ID, ev)
		})
	})
}

func serve(w http.ResponseWriter, r *http.Request, handle func(context.Context, Event) int) {
	var ev Event
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&ev); err != nil {
		respond(w, http.StatusBadRequest)
		return
	}
	respond(w, handle(r.Context(), ev))
}

// onPlayOrigin: origin'e RTMP ile bağlanıp izlemek bir izleyici yolu değildir (oturum sayılmaz,
// limit uygulanmaz). Yalnızca kayıtlı ve etkin bir edge'in çekme adresinden gelen ve o kanal için
// bizim ürettiğimiz bir .ts imzası taşıyan istek kabul edilir. Edge, çekmeyi tetikleyen ilk
// izleyicinin imzasını gönderir (bkz. docs/srs-findings.md); yeniden bağlanırken bu imzanın süresi
// dolmuş olabileceği için süreye bakılmaz. İmza koşulu, edge ile aynı adresten görünen birinin
// (aynı NAT, aynı makine) kanal numarasını bilerek izlemesini engeller.
func (h *Handler) onPlayOrigin(ctx context.Context, ev Event) int {
	id, ok := channelID(ev)
	if !ok {
		return http.StatusForbidden
	}
	claims, err := h.signer.VerifyIgnoringExpiry(param(ev.Param, "token"))
	if err != nil || claims.Kind != token.KindTS || claims.ChannelID != id {
		return http.StatusForbidden
	}
	allowed, err := h.edges.PullAllowed(ctx, ev.IP)
	if err != nil {
		return failure(err)
	}
	if !allowed {
		return http.StatusForbidden
	}
	return http.StatusOK
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
	// Tekil yayın ve kanal kotası (eşzamanlı canlı kanal sayısı) aynı işlemde denetlenir.
	started, err := h.store.MarkLive(ctx, id, ev.ClientID)
	if (err == nil && !started) || errors.Is(err, store.ErrQuotaExceeded) {
		// Engel, SRS çöktüğü için "yayın bitti" bildirimi gelmemiş bayat bir işaret olabilir.
		if h.clearStalePublishers(ctx, id) {
			started, err = h.store.MarkLive(ctx, id, ev.ClientID)
		}
	}
	if errors.Is(err, store.ErrQuotaExceeded) {
		log.Printf("hooks: kanal %d yayını reddedildi: yayıncı %d canlı kanal kotasında", id, ch.TenantID)
		return http.StatusForbidden
	}
	if err != nil {
		return failure(err)
	}
	if !started {
		log.Printf("hooks: kanal %d yayını reddedildi: kanal zaten yayında", id)
		return http.StatusForbidden
	}
	return http.StatusOK
}

// clearStalePublishers, yayıncının yayında görünen kanallarından yayını açan bağlantısı origin
// SRS'te artık olmayanları çevrimdışı yapar. Origin yanıt verdiyse ve temizlik yapıldıysa true döner;
// bu sorgunun kendisi hiçbir kanalı çevrimdışı yapmamış olsa da: SRS yeniden başlayınca yayıncılar
// aynı anda yeniden bağlanır ve bayat işareti başka bir kanalın sorgusu temizlemiş olabilir.
//
// SRS çöküp yeniden başlayınca on_unpublish gelmez; eşitleme döngüsü (reconcile) temizleyene kadar
// kanallar canlı görünür ve yeniden bağlanan yayıncı "ikinci yayın" ya da kota gerekçesiyle
// reddedilirdi. Karar kanal adına değil bağlantı kimliğine göre verilir: yeni bağlantının kendisi
// ve edge'lerin çekme (play) bağlantıları da SRS'in listesindedir, ama kayıtlı yayıncı kimliğiyle
// eşleşmezler. Yayıncı bağlantısı yayın sorgusundan önce listeye girdiği için kabul edilmiş ve
// sürmekte olan bir yayın listede görünür ve dokunulmaz. SRS'e ulaşılamazsa hiçbir kanala dokunulmaz.
func (h *Handler) clearStalePublishers(ctx context.Context, channelID int64) bool {
	live, err := h.store.LivePublishers(ctx, channelID)
	if err != nil {
		log.Printf("hooks: kanal %d: yayında görünen kanallar okunamadı: %v", channelID, err)
		return false
	}
	listCtx, cancel := context.WithTimeout(ctx, originTimeout)
	defer cancel()
	ids, err := h.origin.ClientIDs(listCtx)
	if err != nil {
		log.Printf("hooks: kanal %d: origin'in bağlantı listesi alınamadı, bayat yayın işaretleri temizlenmedi: %v", channelID, err)
		return false
	}
	connected := make(map[string]bool, len(ids))
	for _, id := range ids {
		connected[id] = true
	}
	var stale []store.LivePublisher
	for _, p := range live {
		if !connected[p.ClientID] {
			stale = append(stale, p)
		}
	}
	n, err := h.store.ClearStalePublishers(ctx, stale)
	if err != nil {
		log.Printf("hooks: kanal %d: bayat yayın işaretleri temizlenemedi: %v", channelID, err)
		return false
	}
	if n > 0 {
		log.Printf("hooks: kanal %d: yayını origin'de olmayan %d kanal çevrimdışı yapıldı", channelID, n)
	}
	return true
}

// onUnpublish her zaman kabul eder: SRS reddettiği bağlantılar için de bu bildirimi gönderebilir.
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

// onPlay, bir edge'deki kesintisiz .ts izlemesini yetkilendirir ve oturumunu açar. HLS bu yoldan
// geçmez (bkz. hlsgw).
func (h *Handler) onPlay(ctx context.Context, edgeID int64, ev Event) int {
	id, ok := channelID(ev)
	if !ok {
		return http.StatusForbidden
	}
	now := h.now()
	claims, err := h.signer.Verify(param(ev.Param, "token"), now)
	if err != nil || claims.Kind != token.KindTS || claims.ChannelID != id {
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
	// İzleyici limitindeyse en eski bağlantısı kesilir; yayıncının kotası doluysa izleme reddedilir.
	if err := h.sessions.OpenTS(ctx, edgeID, v.ID, ch.ID, ev.ClientID, ev.IP); err != nil {
		if errors.Is(err, store.ErrTenantConnectionLimit) || errors.Is(err, store.ErrSessionRevoked) {
			return http.StatusForbidden
		}
		return failure(err)
	}
	return http.StatusOK
}

// onStop her zaman kabul eder; izleme zaten bitmiştir.
func (h *Handler) onStop(ctx context.Context, edgeID int64, ev Event) int {
	if err := h.sessions.CloseTS(ctx, edgeID, ev.ClientID); err != nil {
		log.Printf("hooks: oturum kapatılamadı, eşitleme döngüsü temizleyecek: %v", err)
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
