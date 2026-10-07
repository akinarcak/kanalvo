package panelapi

import (
	"context"
	"net/http"

	"streamhub/internal/store"
)

type quotasJSON struct {
	MaxChannels    int `json:"max_channels"`
	MaxViewers     int `json:"max_viewers"`
	MaxConnections int `json:"max_connections"`
}

func toQuotasJSON(q store.Quotas) quotasJSON {
	return quotasJSON{MaxChannels: q.MaxChannels, MaxViewers: q.MaxViewers, MaxConnections: q.MaxConnections}
}

const maxQuota = 100000

func (q quotasJSON) valid() bool {
	for _, v := range []int{q.MaxChannels, q.MaxViewers, q.MaxConnections} {
		if v < 0 || v > maxQuota {
			return false
		}
	}
	return true
}

type tenantJSON struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	Status         string     `json:"status"`
	Quotas         quotasJSON `json:"quotas"`
	CreatedAt      string     `json:"created_at"`
	Channels       int        `json:"channels"`
	Viewers        int        `json:"viewers"`
	LiveChannels   int        `json:"live_channels"`
	ActiveSessions int        `json:"active_sessions"`
}

func toTenantJSON(t store.TenantSummary) tenantJSON {
	return tenantJSON{
		ID: t.ID, Name: t.Name, Email: t.Email, Status: t.Status, Quotas: toQuotasJSON(t.Quotas), CreatedAt: formatTime(t.CreatedAt),
		Channels: t.Channels, Viewers: t.Viewers, LiveChannels: t.LiveChannels, ActiveSessions: t.ActiveSessions,
	}
}

func (h *Handler) adminStats(w http.ResponseWriter, r *http.Request, _ actor) {
	st, err := h.store.PlatformStats(r.Context(), h.cfg.Idle)
	if err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]int{
		"tenants": st.Tenants, "channels": st.Channels, "live_channels": st.LiveChannels,
		"viewers": st.Viewers, "active_sessions": st.ActiveSessions,
	})
}

func (h *Handler) adminListTenants(w http.ResponseWriter, r *http.Request, _ actor) {
	list, err := h.store.ListTenants(r.Context(), h.cfg.Idle)
	if err != nil {
		h.internal(w, err)
		return
	}
	out := make([]tenantJSON, 0, len(list))
	for _, t := range list {
		out = append(out, toTenantJSON(t))
	}
	ok(w, http.StatusOK, out)
}

// tenantSummary, tek bir yayıncıyı sayılarıyla birlikte döner.
func (h *Handler) tenantSummary(ctx context.Context, id int64) (store.TenantSummary, error) {
	list, err := h.store.ListTenants(ctx, h.cfg.Idle)
	if err != nil {
		return store.TenantSummary{}, err
	}
	for _, t := range list {
		if t.ID == id {
			return t, nil
		}
	}
	return store.TenantSummary{}, store.ErrNotFound
}

func (h *Handler) adminGetTenant(w http.ResponseWriter, r *http.Request, _ actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	t, err := h.tenantSummary(r.Context(), id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	ok(w, http.StatusOK, toTenantJSON(t))
}

const emailTaken = "Bu e-posta adresi başka bir hesapta kullanılıyor."

// adminCreateTenant, yayıncıyı rastgele bir şifreyle oluşturur. Şifre yalnızca bu yanıtta görünür.
func (h *Handler) adminCreateTenant(w http.ResponseWriter, r *http.Request, _ actor) {
	var in struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	name, nameOK := cleanName(in.Name)
	email, emailOK := cleanEmail(in.Email)
	if !nameOK || !emailOK {
		fail(w, http.StatusBadRequest, "invalid", "Ad ve geçerli bir e-posta adresi gerekli.")
		return
	}
	password := randomHex(10)
	hash, err := h.hashes.Hash(r.Context(), password)
	if err != nil {
		h.hashError(w, err)
		return
	}
	id, err := h.store.CreateTenantAccount(r.Context(), name, email, hash)
	if err != nil {
		h.storeError(w, err, emailTaken)
		return
	}
	t, err := h.tenantSummary(r.Context(), id)
	if err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusCreated, map[string]any{"tenant": toTenantJSON(t), "password": password})
}

// adminUpdateTenant, gönderilen alanları değiştirir; gönderilmeyenlere dokunmaz.
func (h *Handler) adminUpdateTenant(w http.ResponseWriter, r *http.Request, _ actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	var in struct {
		Name   *string     `json:"name"`
		Email  *string     `json:"email"`
		Status *string     `json:"status"`
		Quotas *quotasJSON `json:"quotas"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	current, err := h.store.TenantByID(ctx, id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}

	// Önce tüm alanlar doğrulanır; hiçbir şey yarım uygulanmaz.
	name, email := current.Name, current.Email
	if in.Name != nil {
		var good bool
		if name, good = cleanName(*in.Name); !good {
			fail(w, http.StatusBadRequest, "invalid", "Ad geçersiz.")
			return
		}
	}
	if in.Email != nil {
		var good bool
		if email, good = cleanEmail(*in.Email); !good {
			fail(w, http.StatusBadRequest, "invalid", "E-posta adresi geçersiz.")
			return
		}
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "suspended" {
		fail(w, http.StatusBadRequest, "invalid", "Durum geçersiz.")
		return
	}
	if in.Quotas != nil && !in.Quotas.valid() {
		fail(w, http.StatusBadRequest, "invalid", "Kotalar 0 ile 100000 arasında olmalı.")
		return
	}

	// Askıya alınan yayıncının panel oturumları aynı işlemde kapanır; süren yayın ve izlemelerini
	// uygulama döngüsü birkaç saniyede keser.
	u := store.TenantUpdate{Status: in.Status}
	if in.Name != nil || in.Email != nil {
		if email == "" {
			fail(w, http.StatusBadRequest, "invalid", "Bu yayıncının panel hesabı yok; önce e-posta adresi verin.")
			return
		}
		u.Profile = &store.TenantProfile{Name: name, Email: email}
	}
	if in.Quotas != nil {
		u.Quotas = &store.Quotas{MaxChannels: in.Quotas.MaxChannels, MaxViewers: in.Quotas.MaxViewers, MaxConnections: in.Quotas.MaxConnections}
	}
	if err := h.store.UpdateTenant(ctx, id, u); err != nil {
		h.storeError(w, err, emailTaken)
		return
	}
	t, err := h.tenantSummary(ctx, id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	ok(w, http.StatusOK, toTenantJSON(t))
}

// adminResetPassword, yayıncıya yeni bir rastgele şifre verir ve tüm panel oturumlarını kapatır.
func (h *Handler) adminResetPassword(w http.ResponseWriter, r *http.Request, _ actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	ctx := r.Context()
	t, err := h.store.TenantByID(ctx, id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	if t.Email == "" {
		fail(w, http.StatusConflict, "no_account", "Bu yayıncının panel hesabı yok; önce e-posta adresi verin.")
		return
	}
	password := randomHex(10)
	hash, err := h.hashes.Hash(ctx, password)
	if err != nil {
		h.hashError(w, err)
		return
	}
	if err := h.store.SetTenantPassword(ctx, id, hash); err != nil {
		h.storeError(w, err, "")
		return
	}
	if err := h.store.DeletePanelSessionsOf(ctx, roleTenant, id, nil); err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]string{"password": password})
}
