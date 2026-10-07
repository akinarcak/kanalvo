package panelapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"kanalvo/internal/store"
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
	id, err := h.store.CreateChannelWith(r.Context(), a.id, store.NewChannel{
		Name: in.Name, Secret: randomHex(16), LogoURL: in.LogoURL, CategoryID: in.CategoryID,
	})
	if err != nil {
		h.storeError(w, err, "")
		return
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

// deleteChannel, kanalı siler. Süren yayını ve .ts izlemeleri kesilmek üzere kuyruğa alınır ve
// hemen denenir; o an kesilemeyenleri uygulama döngüsü yeniden dener.
func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	if err := h.store.DeleteChannel(r.Context(), a.id, id); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.sessions.Flush(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// regenerateSecret, yayın anahtarını değiştirir. Eski anahtarla süren yayın kesilir.
func (h *Handler) regenerateSecret(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	if err := h.store.SetChannelSecret(r.Context(), a.id, id, randomHex(16)); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.sessions.Flush(r.Context())
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

const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxSearchLength = 64
	// totalHeader, dilimlenen bir listenin toplam kayıt sayısını taşır.
	totalHeader = "X-Total-Count"
)

// pageOf, limit ve offset sorgu değerlerini okur; verilmeyen limit defaultPageSize sayılır.
func pageOf(w http.ResponseWriter, r *http.Request) (store.Page, bool) {
	p := store.Page{Limit: defaultPageSize}
	q := r.URL.Query()
	var err error
	if v := q.Get("limit"); v != "" {
		if p.Limit, err = strconv.Atoi(v); err != nil || p.Limit < 1 || p.Limit > maxPageSize {
			fail(w, http.StatusBadRequest, "invalid", "limit 1 ile 200 arasında olmalı.")
			return p, false
		}
	}
	if v := q.Get("offset"); v != "" {
		if p.Offset, err = strconv.Atoi(v); err != nil || p.Offset < 0 {
			fail(w, http.StatusBadRequest, "invalid", "offset geçersiz.")
			return p, false
		}
	}
	return p, true
}

// listViewers: ?q= kullanıcı adında arar; ?limit= ve ?offset= listeyi dilimler (en yeni önce).
func (h *Handler) listViewers(w http.ResponseWriter, r *http.Request, a actor) {
	page, valid := pageOf(w, r)
	if !valid {
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(search) > maxSearchLength {
		fail(w, http.StatusBadRequest, "invalid", "Arama metni en çok 64 karakter olabilir.")
		return
	}
	list, total, err := h.store.ViewersByTenant(r.Context(), a.id, search, page)
	if err != nil {
		h.internal(w, err)
		return
	}
	w.Header().Set(totalHeader, strconv.Itoa(total))
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
	if !good || !validConnections(maxConn) {
		fail(w, http.StatusBadRequest, "invalid", "Bağlantı limiti 1-100 arasında, bitiş tarihi geçerli bir tarih olmalı.")
		return
	}
	id, err := h.store.CreateViewerWith(r.Context(), a.id, store.NewViewer{
		Username: in.Username, Password: randomHex(8), MaxConnections: maxConn, ExpiresAt: expires,
	})
	if err != nil {
		h.storeError(w, err, "Bu kullanıcı adı alınmış.")
		return
	}
	h.writeViewer(w, r, a, id, http.StatusCreated)
}

func validConnections(n int) bool { return n >= 1 && n <= maxViewerConnections }

// updateViewer, yalnızca gönderilen alanları değiştirir. expires_at alanı null gönderilirse bitiş
// tarihi kaldırılır; hiç gönderilmezse dokunulmaz.
func (h *Handler) updateViewer(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	var in struct {
		Status         *string         `json:"status"`
		ExpiresAt      json.RawMessage `json:"expires_at"`
		MaxConnections *int            `json:"max_connections"`
	}
	if !decode(w, r, &in) {
		return
	}
	update := store.ViewerUpdate{Status: in.Status, MaxConnections: in.MaxConnections}
	good := (in.Status == nil || *in.Status == "active" || *in.Status == "suspended") &&
		(in.MaxConnections == nil || validConnections(*in.MaxConnections))
	if in.ExpiresAt != nil {
		var raw *string
		if err := json.Unmarshal(in.ExpiresAt, &raw); err != nil {
			good = false
		} else if update.ExpiresAt, good = parseExpiryIf(good, raw); good {
			update.SetExpiry = true
		}
	}
	if !good {
		fail(w, http.StatusBadRequest, "invalid", "Durum, bağlantı limiti (1-100) veya bitiş tarihi geçersiz.")
		return
	}
	if err := h.store.UpdateViewer(r.Context(), a.id, id, update); err != nil {
		h.storeError(w, err, "")
		return
	}
	// Askıya alınan veya süresi dolan izleyicinin süren izlemesini uygulama döngüsü birkaç saniyede keser.
	h.writeViewer(w, r, a, id, http.StatusOK)
}

// parseExpiryIf, önceki denetimler geçtiyse bitiş tarihini çözer.
func parseExpiryIf(good bool, raw *string) (*time.Time, bool) {
	if !good {
		return nil, false
	}
	return parseExpiry(raw)
}

// deleteViewer, izleyiciyi siler. Süren .ts izlemeleri kesilmek üzere kuyruğa alınır ve hemen denenir.
func (h *Handler) deleteViewer(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	if err := h.store.DeleteViewer(r.Context(), a.id, id); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.sessions.Flush(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// regeneratePassword, izleyiciye yeni bir şifre verir ve süren izlemelerini sonlandırır.
func (h *Handler) regeneratePassword(w http.ResponseWriter, r *http.Request, a actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	if err := h.store.SetViewerPassword(r.Context(), a.id, id, randomHex(8)); err != nil {
		h.storeError(w, err, "")
		return
	}
	h.sessions.Flush(r.Context())
	h.writeViewer(w, r, a, id, http.StatusOK)
}

// --- oturumlar ---

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request, a actor) {
	page, valid := pageOf(w, r)
	if !valid {
		return
	}
	list, total, err := h.store.ActiveSessionsByTenant(r.Context(), a.id, h.cfg.Idle, page)
	if err != nil {
		h.internal(w, err)
		return
	}
	w.Header().Set(totalHeader, strconv.Itoa(total))
	type sessionJSON struct {
		ID        int64  `json:"id"`
		Viewer    string `json:"viewer"`
		Channel   string `json:"channel"`
		Kind      string `json:"kind"`
		IP        string `json:"ip"`
		StartedAt string `json:"started_at"`
		Edge      string `json:"edge"`
	}
	out := make([]sessionJSON, 0, len(list))
	for _, s := range list {
		out = append(out, sessionJSON{ID: s.ID, Viewer: s.Viewer, Channel: s.Channel, Kind: s.Kind, IP: s.IP, StartedAt: formatTime(s.StartedAt), Edge: s.Edge})
	}
	ok(w, http.StatusOK, out)
}
