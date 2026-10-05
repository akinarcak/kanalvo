package panelapi

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"streamhub/internal/store"
)

// Bu dosyadaki her işlem yayıncı kimliğini a.id'den (oturumdan) alır.

func (h *Handler) overview(w http.ResponseWriter, r *http.Request, a actor) {
	t, err := h.store.TenantByID(r.Context(), a.id)
	if err != nil {
		h.internal(w, err)
		return
	}
	u, err := h.store.TenantUsage(r.Context(), a.id, h.cfg.Idle)
	if err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{
		"name":       t.Name,
		"email":      t.Email,
		"quotas":     toQuotasJSON(t.Quotas),
		"usage":      map[string]int{"channels": u.Channels, "viewers": u.Viewers, "connections": u.Connections},
		"ingest_url": h.cfg.IngestURL,
		"xtream_url": h.cfg.PublicBaseURL,
	})
}

// --- kategoriler ---

type categoryJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request, a actor) {
	list, err := h.store.CategoriesByTenant(r.Context(), a.id)
	if err != nil {
		h.internal(w, err)
		return
	}
	out := make([]categoryJSON, 0, len(list))
	for _, c := range list {
		out = append(out, categoryJSON{ID: c.ID, Name: c.Name})
	}
	ok(w, http.StatusOK, out)
}

func (h *Handler) categoryName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return "", false
	}
	name, good := cleanName(in.Name)
	if !good {
		fail(w, http.StatusBadRequest, "invalid", "Kategori adı gerekli (en çok 100 karakter).")
	}
	return name, good
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request, a actor) {
	name, good := h.categoryName(w, r)
	if !good {
		return
	}
	id, err := h.store.CreateCategory(r.Context(), a.id, name)
	if err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusCreated, categoryJSON{ID: id, Name: name})
}

func (h *Handler) renameCategory(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	name, good := h.categoryName(w, r)
	if !good {
		return
	}
	if err := h.store.RenameCategory(r.Context(), a.id, id, name); err != nil {
		h.storeError(w, err, "")
		return
	}
	ok(w, http.StatusOK, categoryJSON{ID: id, Name: name})
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	if err := h.store.DeleteCategory(r.Context(), a.id, id); err != nil {
		h.storeError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- kanallar ---

type channelJSON struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CategoryID *int64 `json:"category_id"`
	LogoURL    string `json:"logo_url"`
	Live       bool   `json:"live"`
	CreatedAt  string `json:"created_at"`
	// OBS ayarları: sunucu IngestURL, yayın anahtarı StreamKey.
	IngestURL string `json:"ingest_url"`
	StreamKey string `json:"stream_key"`
}

func (h *Handler) toChannelJSON(c store.Channel) channelJSON {
	return channelJSON{
		ID: c.ID, Name: c.Name, CategoryID: c.CategoryID, LogoURL: c.LogoURL, Live: c.Live, CreatedAt: formatTime(c.CreatedAt),
		IngestURL: h.cfg.IngestURL, StreamKey: fmt.Sprintf("%d?secret=%s", c.ID, c.StreamSecret),
	}
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request, a actor) {
	list, err := h.store.ChannelsByTenant(r.Context(), a.id)
	if err != nil {
		h.internal(w, err)
		return
	}
	out := make([]channelJSON, 0, len(list))
	for _, c := range list {
		out = append(out, h.toChannelJSON(c))
	}
	ok(w, http.StatusOK, out)
}

type channelInput struct {
	Name       string `json:"name"`
	CategoryID *int64 `json:"category_id"`
	LogoURL    string `json:"logo_url"`
}

func (h *Handler) channelInput(w http.ResponseWriter, r *http.Request) (channelInput, bool) {
	var in channelInput
	if !decode(w, r, &in) {
		return in, false
	}
	name, good := cleanName(in.Name)
	if !good {
		fail(w, http.StatusBadRequest, "invalid", "Kanal adı gerekli (en çok 100 karakter).")
		return in, false
	}
	if !validLogoURL(in.LogoURL) {
		fail(w, http.StatusBadRequest, "invalid", "Logo adresi http:// veya https:// ile başlamalı.")
		return in, false
	}
	in.Name = name
	return in, true
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request, a actor) {
	in, good := h.channelInput(w, r)
	if !good {
		return
	}
	ctx := r.Context()
	id, err := h.store.CreateChannel(ctx, a.id, in.Name, randomHex(16))
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	if in.CategoryID != nil || in.LogoURL != "" {
		if err := h.store.UpdateChannel(ctx, a.id, id, in.Name, in.LogoURL, in.CategoryID); err != nil {
			// Kategori bu yayıncıya ait değil: yarım kalmış kanalı bırakma.
			h.store.DeleteChannel(ctx, a.id, id)
			h.storeError(w, err, "")
			return
		}
	}
	h.writeChannel(w, r, a, id, http.StatusCreated)
}

func (h *Handler) writeChannel(w http.ResponseWriter, r *http.Request, a actor, id int64, status int) {
	c, err := h.store.ChannelOfTenant(r.Context(), a.id, id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	ok(w, status, h.toChannelJSON(c))
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	in, good := h.channelInput(w, r)
	if !good {
		return
	}
	if err := h.store.UpdateChannel(r.Context(), a.id, id, in.Name, in.LogoURL, in.CategoryID); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.writeChannel(w, r, a, id, http.StatusOK)
}

// deleteChannel, kanalı siler ve süren yayını ile .ts izlemelerini keser.
func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	ctx := r.Context()
	// Önce sahiplik doğrulanır; başka yayıncının kanalı için hiçbir bağlantı kesilmez.
	if _, err := h.store.ChannelOfTenant(ctx, a.id, id); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.sessions.EndChannel(ctx, id)
	if err := h.store.DeleteChannel(ctx, a.id, id); err != nil {
		h.storeError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// regenerateSecret, yayın anahtarını değiştirir. Eski anahtarla süren yayın kesilir.
func (h *Handler) regenerateSecret(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	ctx := r.Context()
	if err := h.store.SetChannelSecret(ctx, a.id, id, randomHex(16)); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.sessions.KickPublisher(ctx, id)
	h.writeChannel(w, r, a, id, http.StatusOK)
}

// --- izleyiciler ---

type viewerJSON struct {
	ID             int64   `json:"id"`
	Username       string  `json:"username"`
	Password       string  `json:"password"`
	Status         string  `json:"status"`
	ExpiresAt      *string `json:"expires_at"`
	MaxConnections int     `json:"max_connections"`
	CreatedAt      string  `json:"created_at"`
	PlaylistURL    string  `json:"playlist_url"`
}

func (h *Handler) toViewerJSON(v store.Viewer) viewerJSON {
	out := viewerJSON{
		ID: v.ID, Username: v.Username, Password: v.Password, Status: v.Status,
		MaxConnections: v.MaxConnections, CreatedAt: formatTime(v.CreatedAt),
		PlaylistURL: fmt.Sprintf("%s/get.php?username=%s&password=%s&type=m3u_plus&output=ts",
			h.cfg.PublicBaseURL, url.QueryEscape(v.Username), url.QueryEscape(v.Password)),
	}
	if v.ExpiresAt != nil {
		s := formatTime(*v.ExpiresAt)
		out.ExpiresAt = &s
	}
	return out
}

func (h *Handler) listViewers(w http.ResponseWriter, r *http.Request, a actor) {
	list, err := h.store.ViewersByTenant(r.Context(), a.id)
	if err != nil {
		h.internal(w, err)
		return
	}
	out := make([]viewerJSON, 0, len(list))
	for _, v := range list {
		out = append(out, h.toViewerJSON(v))
	}
	ok(w, http.StatusOK, out)
}

const maxViewerConnections = 100

// parseExpiry: nil süresiz demektir.
func parseExpiry(s *string) (*time.Time, bool) {
	if s == nil {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil, false
	}
	return &t, true
}

func (h *Handler) writeViewer(w http.ResponseWriter, r *http.Request, a actor, id int64, status int) {
	v, err := h.store.ViewerOfTenant(r.Context(), a.id, id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	ok(w, status, h.toViewerJSON(v))
}

// createViewer, izleyiciyi rastgele bir şifreyle oluşturur (Xtream şifresi adreslerde açık taşındığı
// için kullanıcı seçmez; erişim anahtarı gibi ele alınır).
func (h *Handler) createViewer(w http.ResponseWriter, r *http.Request, a actor) {
	var in struct {
		Username       string  `json:"username"`
		MaxConnections *int    `json:"max_connections"`
		ExpiresAt      *string `json:"expires_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validUsername(in.Username) {
		fail(w, http.StatusBadRequest, "invalid", "Kullanıcı adı 3-32 karakter olmalı; yalnızca harf, rakam, nokta, alt çizgi ve tire içerebilir.")
		return
	}
	maxConn := 1
	if in.MaxConnections != nil {
		maxConn = *in.MaxConnections
	}
	expires, good := parseExpiry(in.ExpiresAt)
	if !good || maxConn < 1 || maxConn > maxViewerConnections {
		fail(w, http.StatusBadRequest, "invalid", "Bağlantı limiti 1-100 arasında, bitiş tarihi geçerli bir tarih olmalı.")
		return
	}
	ctx := r.Context()
	id, err := h.store.CreateViewer(ctx, a.id, in.Username, randomHex(8), maxConn)
	if err != nil {
		h.storeError(w, err, "Bu kullanıcı adı alınmış.")
		return
	}
	if expires != nil {
		if err := h.store.UpdateViewer(ctx, a.id, id, "active", expires, maxConn); err != nil {
			h.storeError(w, err, "")
			return
		}
	}
	h.writeViewer(w, r, a, id, http.StatusCreated)
}

func (h *Handler) updateViewer(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	var in struct {
		Status         string  `json:"status"`
		ExpiresAt      *string `json:"expires_at"`
		MaxConnections int     `json:"max_connections"`
	}
	if !decode(w, r, &in) {
		return
	}
	expires, good := parseExpiry(in.ExpiresAt)
	if !good || (in.Status != "active" && in.Status != "suspended") || in.MaxConnections < 1 || in.MaxConnections > maxViewerConnections {
		fail(w, http.StatusBadRequest, "invalid", "Durum, bağlantı limiti (1-100) veya bitiş tarihi geçersiz.")
		return
	}
	if err := h.store.UpdateViewer(r.Context(), a.id, id, in.Status, expires, in.MaxConnections); err != nil {
		h.storeError(w, err, "")
		return
	}
	// Askıya alınan veya süresi dolan izleyicinin süren izlemesini uygulama döngüsü birkaç saniyede keser.
	h.writeViewer(w, r, a, id, http.StatusOK)
}

// deleteViewer, izleyiciyi siler ve süren .ts izlemelerini keser.
func (h *Handler) deleteViewer(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	ctx := r.Context()
	if _, err := h.store.ViewerOfTenant(ctx, a.id, id); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.sessions.EndViewer(ctx, id)
	if err := h.store.DeleteViewer(ctx, a.id, id); err != nil {
		h.storeError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) regeneratePassword(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	if err := h.store.SetViewerPassword(r.Context(), a.id, id, randomHex(8)); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.writeViewer(w, r, a, id, http.StatusOK)
}

// --- oturumlar ---

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request, a actor) {
	list, err := h.store.ActiveSessionsByTenant(r.Context(), a.id, h.cfg.Idle)
	if err != nil {
		h.internal(w, err)
		return
	}
	type sessionJSON struct {
		ID        int64  `json:"id"`
		Viewer    string `json:"viewer"`
		Channel   string `json:"channel"`
		Kind      string `json:"kind"`
		IP        string `json:"ip"`
		StartedAt string `json:"started_at"`
	}
	out := make([]sessionJSON, 0, len(list))
	for _, s := range list {
		out = append(out, sessionJSON{ID: s.ID, Viewer: s.Viewer, Channel: s.Channel, Kind: s.Kind, IP: s.IP, StartedAt: formatTime(s.StartedAt)})
	}
	ok(w, http.StatusOK, out)
}
