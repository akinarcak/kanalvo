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
	"unicode/utf8"

	"streamhub/internal/store"
	"streamhub/internal/token"
)

type Options struct {
	// TSBaseURL, kesintisiz .ts veren SRS'in dış adresidir. İmza sorgu parametresinde gider
	// ve yalnızca izleme başlarken doğrulanır.
	TSBaseURL  string
	TSTokenTTL time.Duration
	// HLSBaseURL, /hls geçidinin dış adresidir. İmza yolun içinde gider (göreli parça
	// adresleri onu devralsın diye) ve her istekte doğrulanır.
	HLSBaseURL  string
	HLSTokenTTL time.Duration
}

type Handler struct {
	store  *store.Store
	signer *token.Signer
	opts   Options
	now    func() time.Time
}

func New(s *store.Store, signer *token.Signer, opts Options, now func() time.Time) *Handler {
	return &Handler{store: s, signer: signer, opts: opts, now: now}
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

	// PostgreSQL geçersiz UTF-8 ve NUL baytını hatayla reddeder; böyle bir ad zaten var olamaz.
	username := r.PathValue("username")
	if !utf8.ValidString(username) || strings.ContainsRune(username, 0) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	v, err := h.store.ViewerByUsername(r.Context(), username)
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

	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, h.target(v.ID, ch.ID, ext, now), http.StatusFound)
}

func (h *Handler) target(viewerID, channelID int64, ext string, now time.Time) string {
	claims := token.Claims{ViewerID: viewerID, ChannelID: channelID}
	if ext == "m3u8" {
		claims.Kind, claims.ExpiresAt = token.KindHLS, now.Add(h.opts.HLSTokenTTL)
		return fmt.Sprintf("%s/hls/%s/%d.m3u8", h.opts.HLSBaseURL, h.signer.Sign(claims), channelID)
	}
	claims.Kind, claims.ExpiresAt = token.KindTS, now.Add(h.opts.TSTokenTTL)
	return fmt.Sprintf("%s/live/%d.ts?token=%s", h.opts.TSBaseURL, channelID, h.signer.Sign(claims))
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
