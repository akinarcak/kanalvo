// Package play, izleyicinin Xtream biçimli yayın isteğini doğrular ve edge'e yönlendirir.
package play

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"streamhub/internal/auth"
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
	auth   *auth.Authenticator
	signer *token.Signer
	opts   Options
	now    func() time.Time
}

func New(s *store.Store, a *auth.Authenticator, signer *token.Signer, opts Options, now func() time.Time) *Handler {
	return &Handler{store: s, auth: a, signer: signer, opts: opts, now: now}
}

// Register, Xtream oynatıcılarının kullandığı iki yol biçimini kaydeder:
// /live/<kullanıcı>/<şifre>/<kanal>[.ts|.m3u8] ve kısa biçimi /<kullanıcı>/<şifre>/<kanal>.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /live/{username}/{password}/{file}", h.serve)
	mux.HandleFunc("GET /{username}/{password}/{file}", h.serve)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	channelID, ext, ok := parseFile(r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := h.now()

	v, err := h.auth.Viewer(r, r.PathValue("username"), r.PathValue("password"))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	case errors.Is(err, auth.ErrInvalid):
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	case err != nil:
		internalError(w, err)
		return
	}
	if !v.Usable(now) {
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
		claims.Session = newSessionKey()
		return fmt.Sprintf("%s/hls/%s/%d.m3u8", h.opts.HLSBaseURL, h.signer.Sign(claims), channelID)
	}
	claims.Kind, claims.ExpiresAt = token.KindTS, now.Add(h.opts.TSTokenTTL)
	return fmt.Sprintf("%s/live/%d.ts?token=%s", h.opts.TSBaseURL, channelID, h.signer.Sign(claims))
}

// newSessionKey, bir HLS izlemesini diğerlerinden ayıran rastgele anahtarı üretir.
func newSessionKey() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// parseFile, "<kanal>", "<kanal>.ts" veya "<kanal>.m3u8" biçimini çözer; uzantısız ad .ts sayılır.
func parseFile(file string) (int64, string, bool) {
	name, ext, hasExt := strings.Cut(file, ".")
	if !hasExt {
		ext = "ts"
	}
	if ext != "ts" && ext != "m3u8" {
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
