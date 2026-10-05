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

// onPlay, kesintisiz .ts ve RTMP izlemelerini yetkilendirir. HLS bu yoldan geçmez (bkz. hlsgw).
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
