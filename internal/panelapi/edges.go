package panelapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"streamhub/internal/enroll"
	"streamhub/internal/store"
)

// Edge yönetimi yalnızca platform yöneticisine açıktır.

const (
	defaultEdgeWeight = 100
	maxEdgeWeight     = 1000
	localEdgeFixed    = "Yerel sunucunun adresleri sunucu ayarlarından gelir; panelden değiştirilemez."
	edgeNameTaken     = "Bu adla başka bir sunucu kayıtlı."
	// enrollmentTTL, kurulum kodunun geçerlilik süresidir.
	enrollmentTTL = 30 * time.Minute
)

type edgeJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
	// BaseURL, izleyicilerin .ts için yönlendirildiği adrestir; uzak edge'de HLS adresiyle aynıdır.
	BaseURL        string  `json:"base_url"`
	HLSBaseURL     string  `json:"hls_base_url"`
	ControlURL     string  `json:"control_url"`
	PullIP         string  `json:"pull_ip"`
	Weight         int     `json:"weight"`
	Enabled        bool    `json:"enabled"`
	Healthy        bool    `json:"healthy"`
	LastSeenAt     *string `json:"last_seen_at"`
	ActiveSessions int     `json:"active_sessions"`
	// Setup, uzak edge'in .env dosyasına yazılacak satırlardır (edge anahtarını içerir).
	Setup string `json:"setup,omitempty"`
}

func (h *Handler) toEdgeJSON(e store.Edge, sessions int) edgeJSON {
	out := edgeJSON{
		ID: e.ID, Name: e.Name, Builtin: e.Builtin, BaseURL: e.TSBaseURL, HLSBaseURL: e.HLSBaseURL,
		ControlURL: e.ControlURL, PullIP: e.PullIP, Weight: e.Weight, Enabled: e.Enabled, Healthy: e.Healthy,
		ActiveSessions: sessions,
	}
	if e.LastSeenAt != nil {
		seen := formatTime(*e.LastSeenAt)
		out.LastSeenAt = &seen
	}
	if !e.Builtin {
		out.Setup = fmt.Sprintf("CONTROL_URL=%s\nEDGE_KEY=%s\nORIGIN_RTMP=%s\n", h.cfg.PublicBaseURL, e.Key, enroll.OriginRTMP(h.cfg.IngestURL))
	}
	return out
}

func (h *Handler) edgeJSONByID(ctx context.Context, id int64) (edgeJSON, error) {
	e, err := h.store.EdgeByID(ctx, id, h.cfg.EdgeHealthWindow)
	if err != nil {
		return edgeJSON{}, err
	}
	counts, err := h.store.EdgeSessionCounts(ctx, h.cfg.Idle)
	if err != nil {
		return edgeJSON{}, err
	}
	return h.toEdgeJSON(e, counts[id]), nil
}

func (h *Handler) adminListEdges(w http.ResponseWriter, r *http.Request, _ actor) {
	list, err := h.store.Edges(r.Context(), h.cfg.EdgeHealthWindow)
	if err != nil {
		h.internal(w, err)
		return
	}
	counts, err := h.store.EdgeSessionCounts(r.Context(), h.cfg.Idle)
	if err != nil {
		h.internal(w, err)
		return
	}
	out := make([]edgeJSON, 0, len(list))
	for _, e := range list {
		out = append(out, h.toEdgeJSON(e, counts[e.ID]))
	}
	ok(w, http.StatusOK, out)
}

// adminCreateEdge, uzak bir edge kaydeder ve anahtarını üretir. Edge, yönetim adresi yanıt
// vermeye başlayana kadar sağlıksız görünür ve yönlendirme almaz.
func (h *Handler) adminCreateEdge(w http.ResponseWriter, r *http.Request, _ actor) {
	var in struct {
		Name       string `json:"name"`
		BaseURL    string `json:"base_url"`
		ControlURL string `json:"control_url"`
		PullIP     string `json:"pull_ip"`
		Weight     *int   `json:"weight"`
	}
	if !decode(w, r, &in) {
		return
	}
	name, nameOK := cleanName(in.Name)
	base, baseOK := cleanBaseURL(in.BaseURL)
	control, controlOK := base, true
	if strings.TrimSpace(in.ControlURL) != "" {
		control, controlOK = cleanBaseURL(in.ControlURL)
	}
	pullIP, ipOK := cleanIP(in.PullIP)
	weight := defaultEdgeWeight
	if in.Weight != nil {
		weight = *in.Weight
	}
	if msg := edgeProblem(nameOK, baseOK, controlOK, ipOK, weight); msg != "" {
		fail(w, http.StatusBadRequest, "invalid", msg)
		return
	}
	id, err := h.store.CreateEdge(r.Context(), store.NewEdge{
		Name: name, BaseURL: base, ControlURL: control, Key: randomHex(24), PullIP: pullIP, Weight: weight,
	})
	if err != nil {
		h.storeError(w, err, edgeNameTaken)
		return
	}
	out, err := h.edgeJSONByID(r.Context(), id)
	if err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusCreated, out)
}

func edgeProblem(nameOK, baseOK, controlOK, ipOK bool, weight int) string {
	switch {
	case !nameOK:
		return "Ad geçersiz."
	case !baseOK:
		return "İzleyici adresi http:// veya https:// ile başlayan, yol içermeyen bir adres olmalı."
	case !controlOK:
		return "Yönetim adresi http:// veya https:// ile başlayan, yol içermeyen bir adres olmalı."
	case !ipOK:
		return "Çekme adresi geçerli bir IP adresi olmalı."
	case weight < 1 || weight > maxEdgeWeight:
		return "Ağırlık 1 ile 1000 arasında olmalı."
	}
	return ""
}

// adminUpdateEdge, gönderilen alanları değiştirir; gönderilmeyenlere dokunmaz. Tek istisna: yönetim
// adresi izleyici adresiyle aynıysa (ayrıca verilmemişse) izleyici adresiyle birlikte değişir.
func (h *Handler) adminUpdateEdge(w http.ResponseWriter, r *http.Request, _ actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	var in struct {
		Name       *string `json:"name"`
		BaseURL    *string `json:"base_url"`
		ControlURL *string `json:"control_url"`
		PullIP     *string `json:"pull_ip"`
		Weight     *int    `json:"weight"`
		Enabled    *bool   `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	current, err := h.store.EdgeByID(ctx, id, h.cfg.EdgeHealthWindow)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	if current.Builtin && (in.BaseURL != nil || in.ControlURL != nil || in.PullIP != nil) {
		fail(w, http.StatusConflict, "builtin", localEdgeFixed)
		return
	}

	u := store.EdgeUpdate{Weight: in.Weight, Enabled: in.Enabled}
	nameOK, baseOK, controlOK, ipOK, weight := true, true, true, true, current.Weight
	if in.Name != nil {
		var name string
		name, nameOK = cleanName(*in.Name)
		u.Name = &name
	}
	if in.BaseURL != nil {
		var base string
		base, baseOK = cleanBaseURL(*in.BaseURL)
		u.BaseURL = &base
	}
	if in.ControlURL != nil {
		var control string
		control, controlOK = cleanBaseURL(*in.ControlURL)
		u.ControlURL = &control
	} else if u.BaseURL != nil && current.ControlURL == current.TSBaseURL {
		u.ControlURL = u.BaseURL
	}
	if in.PullIP != nil {
		var ip string
		ip, ipOK = cleanIP(*in.PullIP)
		u.PullIP = &ip
	}
	if in.Weight != nil {
		weight = *in.Weight
	}
	if msg := edgeProblem(nameOK, baseOK, controlOK, ipOK, weight); msg != "" {
		fail(w, http.StatusBadRequest, "invalid", msg)
		return
	}
	if err := h.store.UpdateEdge(ctx, id, u); err != nil {
		h.storeError(w, err, edgeNameTaken)
		return
	}
	out, err := h.edgeJSONByID(ctx, id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	ok(w, http.StatusOK, out)
}

// adminDeleteEdge, uzak bir edge'i oturum kayıtlarıyla birlikte siler. Edge'de süren izlemeler
// kesilmez (silinen edge'e artık ulaşılamayabilir); yeni izleme ise yetkilendirilemez.
func (h *Handler) adminDeleteEdge(w http.ResponseWriter, r *http.Request, _ actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	err := h.store.DeleteEdge(r.Context(), id)
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "builtin", "Yerel sunucu silinemez; yönlendirme almaması için devre dışı bırakabilirsiniz.")
		return
	}
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cleanBaseURL, "http(s)://sunucu[:port]" biçimindeki bir adresi sondaki / olmadan döner.
// Yol, sorgu veya kullanıcı bilgisi içeren adres geçersizdir.
func cleanBaseURL(raw string) (string, bool) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" || len(s) > 200 {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func cleanIP(raw string) (string, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || ip.Zone() != "" {
		return "", false
	}
	return ip.Unmap().String(), true
}

// --- kurulum kodu ---

// enrollmentJSON, bir kurulum kodunun durumudur: "waiting" (cihaz bekleniyor), "enrolled" (cihaz
// kaydoldu; Edge doludur) ya da "expired" (kullanılmadan süresi doldu).
type enrollmentJSON struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
	// Command yalnızca kod üretilirken döner: kod saklanmadığı için sonradan gösterilemez.
	Command string    `json:"command,omitempty"`
	Edge    *edgeJSON `json:"edge,omitempty"`
}

// adminCreateEnrollment, yeni bir sunucuyu tek komutla kurmak için tek kullanımlık kod üretir.
func (h *Handler) adminCreateEnrollment(w http.ResponseWriter, r *http.Request, _ actor) {
	token := randomHex(16)
	id, err := h.store.CreateEdgeEnrollment(r.Context(), token, enrollmentTTL)
	if err != nil {
		h.internal(w, err)
		return
	}
	en, err := h.store.EdgeEnrollmentByID(r.Context(), id)
	if err != nil {
		h.internal(w, err)
		return
	}
	ok(w, http.StatusCreated, enrollmentJSON{
		ID: id, Status: "waiting", ExpiresAt: formatTime(en.ExpiresAt), Command: enroll.Command(h.cfg.PublicBaseURL, token),
	})
}

func (h *Handler) adminGetEnrollment(w http.ResponseWriter, r *http.Request, _ actor) {
	id, valid := pathID(w, r)
	if !valid {
		return
	}
	en, err := h.store.EdgeEnrollmentByID(r.Context(), id)
	if err != nil {
		h.storeError(w, err, "")
		return
	}
	out := enrollmentJSON{ID: en.ID, Status: "waiting", ExpiresAt: formatTime(en.ExpiresAt)}
	switch {
	case en.Expired:
		out.Status = "expired"
	case en.UsedAt != nil:
		out.Status = "enrolled"
		// Sunucu sonradan silindiyse kayıt "enrolled" kalır ama sunucusu yoktur.
		if en.EdgeID != nil {
			edge, err := h.edgeJSONByID(r.Context(), *en.EdgeID)
			if err != nil {
				h.storeError(w, err, "")
				return
			}
			out.Edge = &edge
		}
	}
	ok(w, http.StatusOK, out)
}
