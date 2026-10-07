// Package panelapi, yönetim panelinin JSON API'sidir: giriş, yönetici işlemleri (yayıncılar, edge'ler) ve
// yayıncı işlemleri (kategori, kanal, izleyici, oturum).
//
// Yayıncı kimliği her zaman oturumdan alınır; istekle gelen hiçbir değer hangi yayıncının
// kayıtlarına dokunulacağını belirleyemez.
package panelapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"streamhub/internal/clientip"
	"streamhub/internal/passhash"
	"streamhub/internal/ratelimit"
	"streamhub/internal/session"
	"streamhub/internal/store"
)

const (
	cookieName = "sh_panel"
	// csrfHeader: tarayıcılar başka siteden gelen bir isteğe özel başlık ekleyemez (CORS izni
	// verilmediği sürece). Değişiklik yapan her istek bu başlığı taşımak zorundadır.
	csrfHeader   = "X-StreamHub-Panel"
	maxBodyBytes = 64 << 10
)

type Config struct {
	SessionTTL time.Duration
	// SecureCookie, çerezin yalnızca HTTPS üzerinden gönderilmesini sağlar. Yalnızca yerel
	// geliştirmede kapatılmalıdır.
	SecureCookie      bool
	TrustProxyHeaders bool
	// IngestURL, yayıncının OBS'e yazacağı sunucu adresidir (ör. rtmp://yayin.example.com/live).
	IngestURL string
	// PublicBaseURL, izleyicilerin oynatıcıya yazacağı sunucu adresidir.
	PublicBaseURL string
	// Idle, HLS oturumunun bağlantı sayımından düşme süresidir (session.Manager ile aynı olmalı).
	Idle time.Duration
	// EdgeHealthWindow: bu süre içinde sağlık sinyali vermiş edge sağlıklı gösterilir (yönlendirme
	// kararını veren balancer ile aynı olmalı).
	EdgeHealthWindow time.Duration
}

// Aynı anda yürüyen şifre işlemi sayısı ve sıra bekleme süresi (bkz. passhash.Gate).
const (
	hashConcurrency = 4
	hashWait        = 3 * time.Second
)

type Handler struct {
	store    *store.Store
	sessions *session.Manager
	limiter  *ratelimit.Limiter
	hashes   *passhash.Gate
	cfg      Config
}

func New(s *store.Store, sessions *session.Manager, limiter *ratelimit.Limiter, cfg Config) *Handler {
	return &Handler{store: s, sessions: sessions, limiter: limiter, hashes: passhash.NewGate(hashConcurrency, hashWait), cfg: cfg}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/logout", h.as(anyRole, h.logout))
	mux.HandleFunc("GET /api/me", h.as(anyRole, h.me))
	mux.HandleFunc("POST /api/password", h.as(anyRole, h.changePassword))

	mux.HandleFunc("GET /api/admin/stats", h.as(roleAdmin, h.adminStats))
	mux.HandleFunc("GET /api/admin/tenants", h.as(roleAdmin, h.adminListTenants))
	mux.HandleFunc("POST /api/admin/tenants", h.as(roleAdmin, h.adminCreateTenant))
	mux.HandleFunc("GET /api/admin/tenants/{id}", h.as(roleAdmin, h.adminGetTenant))
	mux.HandleFunc("PATCH /api/admin/tenants/{id}", h.as(roleAdmin, h.adminUpdateTenant))
	mux.HandleFunc("POST /api/admin/tenants/{id}/reset-password", h.as(roleAdmin, h.adminResetPassword))

	mux.HandleFunc("GET /api/admin/edges", h.as(roleAdmin, h.adminListEdges))
	mux.HandleFunc("POST /api/admin/edges", h.as(roleAdmin, h.adminCreateEdge))
	mux.HandleFunc("PATCH /api/admin/edges/{id}", h.as(roleAdmin, h.adminUpdateEdge))
	mux.HandleFunc("DELETE /api/admin/edges/{id}", h.as(roleAdmin, h.adminDeleteEdge))
	mux.HandleFunc("POST /api/admin/edge-enrollments", h.as(roleAdmin, h.adminCreateEnrollment))
	mux.HandleFunc("GET /api/admin/edge-enrollments/{id}", h.as(roleAdmin, h.adminGetEnrollment))

	mux.HandleFunc("GET /api/tenant/overview", h.as(roleTenant, h.overview))
	mux.HandleFunc("GET /api/tenant/categories", h.as(roleTenant, h.listCategories))
	mux.HandleFunc("POST /api/tenant/categories", h.as(roleTenant, h.createCategory))
	mux.HandleFunc("PATCH /api/tenant/categories/{id}", h.as(roleTenant, h.renameCategory))
	mux.HandleFunc("DELETE /api/tenant/categories/{id}", h.as(roleTenant, h.deleteCategory))
	mux.HandleFunc("GET /api/tenant/channels", h.as(roleTenant, h.listChannels))
	mux.HandleFunc("POST /api/tenant/channels", h.as(roleTenant, h.createChannel))
	mux.HandleFunc("PATCH /api/tenant/channels/{id}", h.as(roleTenant, h.updateChannel))
	mux.HandleFunc("DELETE /api/tenant/channels/{id}", h.as(roleTenant, h.deleteChannel))
	mux.HandleFunc("POST /api/tenant/channels/{id}/regenerate-secret", h.as(roleTenant, h.regenerateSecret))
	mux.HandleFunc("GET /api/tenant/viewers", h.as(roleTenant, h.listViewers))
	mux.HandleFunc("POST /api/tenant/viewers", h.as(roleTenant, h.createViewer))
	mux.HandleFunc("PATCH /api/tenant/viewers/{id}", h.as(roleTenant, h.updateViewer))
	mux.HandleFunc("DELETE /api/tenant/viewers/{id}", h.as(roleTenant, h.deleteViewer))
	mux.HandleFunc("POST /api/tenant/viewers/{id}/regenerate-password", h.as(roleTenant, h.regeneratePassword))
	mux.HandleFunc("GET /api/tenant/sessions", h.as(roleTenant, h.listSessions))
}

// --- oturum ve yetki ---

const (
	roleAdmin  = "admin"
	roleTenant = "tenant"
	anyRole    = ""
)

// actor, isteği yapan giriş yapmış hesaptır.
type actor struct {
	role      string
	id        int64 // yönetici veya yayıncı numarası
	tokenHash []byte
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, a actor)

// as, isteği yalnızca verilen roldeki giriş yapmış hesap için çalıştırır.
// Sıra: oturum (401), panel başlığı (403), rol (403), yayıncının askıda olmaması (403).
func (h *Handler) as(role string, next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := h.actor(r)
		if !ok {
			fail(w, http.StatusUnauthorized, "unauthenticated", "Oturum açmanız gerekiyor.")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get(csrfHeader) == "" {
			fail(w, http.StatusForbidden, "csrf", "İstek panelden gelmiyor.")
			return
		}
		if role != anyRole && a.role != role {
			fail(w, http.StatusForbidden, "forbidden", "Bu işlem için yetkiniz yok.")
			return
		}
		if a.role == roleTenant {
			t, err := h.store.TenantByID(r.Context(), a.id)
			if err != nil {
				h.internal(w, err)
				return
			}
			if t.Status != "active" {
				fail(w, http.StatusForbidden, "suspended", "Hesabınız askıya alınmış.")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next(w, r, a)
	}
}

func (h *Handler) actor(r *http.Request) (actor, bool) {
	ck, err := r.Cookie(cookieName)
	if err != nil || ck.Value == "" {
		return actor{}, false
	}
	hash := hashToken(ck.Value)
	ps, err := h.store.PanelSessionByHash(r.Context(), hash)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("panelapi: oturum okunamadı: %v", err)
		}
		return actor{}, false
	}
	return actor{role: ps.Role, id: ps.SubjectID, tokenHash: hash}, true
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (h *Handler) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) == "" {
		fail(w, http.StatusForbidden, "csrf", "İstek panelden gelmiyor.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := clientip.Key(r, h.cfg.TrustProxyHeaders)
	if !h.limiter.Allow(key) {
		fail(w, http.StatusTooManyRequests, "rate_limited", "Çok fazla hatalı deneme. Birkaç dakika sonra yeniden deneyin.")
		return
	}

	acc, status, err := h.authenticate(r.Context(), strings.TrimSpace(in.Email), in.Password)
	if errors.Is(err, errBadCredentials) {
		fail(w, http.StatusUnauthorized, "invalid_credentials", "E-posta veya şifre hatalı.")
		return
	}
	h.limiter.Success(key)
	if err != nil {
		h.hashError(w, err)
		return
	}
	role, id := acc.Role, acc.ID
	if status != "active" {
		fail(w, http.StatusForbidden, "suspended", "Hesabınız askıya alınmış.")
		return
	}

	token := randomHex(32)
	if err := h.store.CreatePanelSession(r.Context(), hashToken(token), role, id, h.cfg.SessionTTL); err != nil {
		h.internal(w, err)
		return
	}
	h.setCookie(w, token, int(h.cfg.SessionTTL.Seconds()))
	ok(w, http.StatusOK, acc)
}

var errBadCredentials = errors.New("panelapi: e-posta veya şifre hatalı")

// authenticate, önce yöneticilere sonra yayıncılara bakar. Hesap bulunamasa da bir şifre
// doğrulaması kadar zaman harcar.
func (h *Handler) authenticate(ctx context.Context, email, password string) (account, string, error) {
	var acc account
	var hash, status string

	admin, err := h.store.AdminByEmail(ctx, email)
	switch {
	case err == nil:
		acc, hash, status = account{Role: roleAdmin, ID: admin.ID, Name: admin.Email, Email: admin.Email}, admin.PasswordHash, "active"
	case !errors.Is(err, store.ErrNotFound):
		return account{}, "", err
	default:
		tenant, err := h.store.TenantByEmail(ctx, email)
		switch {
		case err == nil:
			acc, hash, status = account{Role: roleTenant, ID: tenant.ID, Name: tenant.Name, Email: tenant.Email}, tenant.PasswordHash, tenant.Status
		case !errors.Is(err, store.ErrNotFound):
			return account{}, "", err
		}
	}
	// hash boşsa (hesap yok veya panel hesabı yok) sahte bir doğrulama yapılır.
	valid, err := h.hashes.Verify(ctx, hash, password)
	if err != nil {
		return account{}, "", err
	}
	if !valid {
		return account{}, "", errBadCredentials
	}
	return acc, status, nil
}

// hashError, şifre işlemi sırasında oluşan hatayı yanıtlar: sunucu meşgulse 503, aksi halde 500.
func (h *Handler) hashError(w http.ResponseWriter, err error) {
	if errors.Is(err, passhash.ErrBusy) {
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "busy", "Sunucu şu an meşgul. Birkaç saniye sonra yeniden deneyin.")
		return
	}
	h.internal(w, err)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, a actor) {
	if err := h.store.DeletePanelSession(r.Context(), a.tokenHash); err != nil {
		h.internal(w, err)
		return
	}
	h.setCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

type account struct {
	Role  string `json:"role"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request, a actor) {
	acc, _, err := h.account(r.Context(), a)
	if err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusOK, acc)
}

// account, giriş yapmış hesabın bilgilerini ve şifre özetini döner.
func (h *Handler) account(ctx context.Context, a actor) (account, string, error) {
	if a.role == roleAdmin {
		adm, err := h.store.AdminByID(ctx, a.id)
		return account{Role: roleAdmin, ID: adm.ID, Name: adm.Email, Email: adm.Email}, adm.PasswordHash, err
	}
	t, err := h.store.TenantByID(ctx, a.id)
	return account{Role: roleTenant, ID: t.ID, Name: t.Name, Email: t.Email}, t.PasswordHash, err
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request, a actor) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &in) {
		return
	}
	if msg := validPassword(in.New); msg != "" {
		fail(w, http.StatusBadRequest, "invalid", msg)
		return
	}
	_, currentHash, err := h.account(r.Context(), a)
	if err != nil {
		h.internal(w, err)
		return
	}
	// Mevcut şifre denemeleri hesap başına sınırlanır.
	key := fmt.Sprintf("password:%s:%d", a.role, a.id)
	if !h.limiter.Allow(key) {
		fail(w, http.StatusTooManyRequests, "rate_limited", "Çok fazla hatalı deneme. Birkaç dakika sonra yeniden deneyin.")
		return
	}
	valid, err := h.hashes.Verify(r.Context(), currentHash, in.Current)
	if err != nil {
		h.limiter.Success(key)
		h.hashError(w, err)
		return
	}
	if !valid {
		fail(w, http.StatusForbidden, "wrong_password", "Mevcut şifre hatalı.")
		return
	}
	h.limiter.Success(key)
	newHash, err := h.hashes.Hash(r.Context(), in.New)
	if err != nil {
		h.hashError(w, err)
		return
	}
	if a.role == roleAdmin {
		err = h.store.SetAdminPassword(r.Context(), a.id, newHash)
	} else {
		err = h.store.SetTenantPassword(r.Context(), a.id, newHash)
	}
	if err == nil {
		// Bu tarayıcı dışındaki tüm oturumlar kapanır.
		err = h.store.DeletePanelSessionsOf(r.Context(), a.role, a.id, a.tokenHash)
	}
	if err != nil {
		h.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- yardımcılar ---

// decode, gövdeyi tek bir JSON nesnesi olarak çözer; bilinmeyen alanları ve fazladan veriyi reddeder.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "İstek gövdesi geçersiz.")
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		fail(w, http.StatusBadRequest, "invalid", "İstek gövdesi geçersiz.")
		return false
	}
	return true
}

// pathID, yoldaki {id} değerini çözer; geçersizse 404 yazar.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusNotFound, "not_found", "Kayıt bulunamadı.")
		return 0, false
	}
	return id, true
}

func ok(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("panelapi: yanıt yazılamadı: %v", err)
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(w http.ResponseWriter, status int, code, message string) {
	ok(w, status, map[string]apiError{"error": {Code: code, Message: message}})
}

func (h *Handler) internal(w http.ResponseWriter, err error) {
	log.Printf("panelapi: %v", err)
	fail(w, http.StatusInternalServerError, "internal", "Beklenmeyen bir hata oluştu.")
}

// storeError, veri katmanı hatalarını yanıt koduna çevirir.
func (h *Handler) storeError(w http.ResponseWriter, err error, conflictMessage string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "Kayıt bulunamadı.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "conflict", conflictMessage)
	case errors.Is(err, store.ErrQuotaExceeded):
		fail(w, http.StatusConflict, "quota", "Kotanız doldu. Daha fazlası için platform yöneticisine başvurun.")
	default:
		h.internal(w, err)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // işletim sistemi rastgele sayı veremiyorsa güvenli devam edilemez
	}
	return hex.EncodeToString(b)
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }
