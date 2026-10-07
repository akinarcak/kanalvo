// Package xtream, IPTV oynatıcılarının beklediği Xtream Codes uyumlu uçları sunar:
// player_api.php (giriş ve kanal listesi), get.php (M3U) ve xmltv.php (program rehberi).
package xtream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kanalvo/internal/auth"
	"kanalvo/internal/store"
)

// Kategorisi olmayan kanallar bu sanal kategoride gösterilir; birçok oynatıcı kategorisiz
// kanalı hiç listelemez.
const (
	uncategorizedID   = 0
	uncategorizedName = "Genel"
)

const maxFormBytes = 64 << 10

// ConnectionCounter, bir izleyicinin süren bağlantılarını sayar (bkz. session.Manager).
type ConnectionCounter interface {
	ActiveCount(ctx context.Context, viewerID int64) (int, error)
}

type Handler struct {
	store    *store.Store
	auth     *auth.Authenticator
	sessions ConnectionCounter
	base     *url.URL
	now      func() time.Time
}

// New: publicBaseURL, oynatıcıların sunucuya ulaştığı dış adrestir (ör. http://tv.example.com:8000).
func New(s *store.Store, a *auth.Authenticator, sessions ConnectionCounter, publicBaseURL *url.URL, now func() time.Time) *Handler {
	return &Handler{store: s, auth: a, sessions: sessions, base: publicBaseURL, now: now}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/player_api.php", h.playerAPI) // oynatıcılar GET de POST da kullanır
	mux.HandleFunc("GET /get.php", h.playlist)
	mux.HandleFunc("GET /xmltv.php", h.guide)
}

// --- player_api.php ---

type userInfo struct {
	Username             string   `json:"username"`
	Password             string   `json:"password"`
	Message              string   `json:"message"`
	Auth                 int      `json:"auth"`
	Status               string   `json:"status"`
	ExpDate              *string  `json:"exp_date"`
	IsTrial              string   `json:"is_trial"`
	ActiveCons           string   `json:"active_cons"`
	CreatedAt            string   `json:"created_at"`
	MaxConnections       string   `json:"max_connections"`
	AllowedOutputFormats []string `json:"allowed_output_formats"`
}

type serverInfo struct {
	URL            string `json:"url"`
	Port           string `json:"port"`
	HTTPSPort      string `json:"https_port"`
	ServerProtocol string `json:"server_protocol"`
	RTMPPort       string `json:"rtmp_port"`
	Timezone       string `json:"timezone"`
	TimestampNow   int64  `json:"timestamp_now"`
	TimeNow        string `json:"time_now"`
}

type liveCategory struct {
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
	ParentID     int    `json:"parent_id"`
}

type liveStream struct {
	Num               int     `json:"num"`
	Name              string  `json:"name"`
	StreamType        string  `json:"stream_type"`
	StreamID          int64   `json:"stream_id"`
	StreamIcon        string  `json:"stream_icon"`
	EPGChannelID      *string `json:"epg_channel_id"`
	Added             string  `json:"added"`
	CategoryID        string  `json:"category_id"`
	CategoryIDs       []int64 `json:"category_ids"`
	CustomSID         string  `json:"custom_sid"`
	TVArchive         int     `json:"tv_archive"`
	DirectSource      string  `json:"direct_source"`
	TVArchiveDuration int     `json:"tv_archive_duration"`
}

func (h *Handler) playerAPI(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	v, err := h.auth.Viewer(r, r.FormValue("username"), r.FormValue("password"))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	case errors.Is(err, auth.ErrInvalid):
		writeJSON(w, map[string]any{"user_info": map[string]int{"auth": 0}})
		return
	case err != nil:
		internalError(w, err)
		return
	}
	now := h.now()

	switch r.FormValue("action") {
	case "get_live_categories":
		h.liveCategories(w, r.Context(), v, now)
	case "get_live_streams":
		h.liveStreams(w, r.Context(), v, now, r.FormValue("category_id"))
	case "get_vod_categories", "get_vod_streams", "get_series_categories", "get_series":
		writeJSON(w, []struct{}{})
	case "get_short_epg", "get_simple_data_table":
		writeJSON(w, map[string]any{"epg_listings": []struct{}{}})
	default:
		active, err := h.sessions.ActiveCount(r.Context(), v.ID)
		if err != nil {
			internalError(w, err)
			return
		}
		writeJSON(w, map[string]any{"user_info": h.userInfo(v, active, now), "server_info": h.serverInfo(now)})
	}
}

func (h *Handler) userInfo(v store.Viewer, activeConnections int, now time.Time) userInfo {
	info := userInfo{
		Username:             v.Username,
		Password:             v.Password,
		Auth:                 1,
		Status:               status(v, now),
		IsTrial:              "0",
		ActiveCons:           strconv.Itoa(activeConnections),
		CreatedAt:            strconv.FormatInt(v.CreatedAt.Unix(), 10),
		MaxConnections:       strconv.Itoa(v.MaxConnections),
		AllowedOutputFormats: []string{"m3u8", "ts"},
	}
	if v.ExpiresAt != nil {
		exp := strconv.FormatInt(v.ExpiresAt.Unix(), 10)
		info.ExpDate = &exp
	}
	return info
}

// status, Xtream oynatıcılarının tanıdığı hesap durumlarından birini döner.
func status(v store.Viewer, now time.Time) string {
	switch {
	case v.TenantStatus != "active":
		return "Disabled"
	case v.Status != "active":
		return "Banned"
	case v.ExpiresAt != nil && !now.Before(*v.ExpiresAt):
		return "Expired"
	default:
		return "Active"
	}
}

func (h *Handler) serverInfo(now time.Time) serverInfo {
	info := serverInfo{
		URL:            h.base.Hostname(),
		Port:           "80",
		HTTPSPort:      "443",
		ServerProtocol: h.base.Scheme,
		RTMPPort:       "1935",
		Timezone:       "UTC",
		TimestampNow:   now.Unix(),
		TimeNow:        now.UTC().Format("2006-01-02 15:04:05"),
	}
	if port := h.base.Port(); port != "" {
		if h.base.Scheme == "https" {
			info.HTTPSPort = port
		} else {
			info.Port = port
		}
	}
	return info
}

func (h *Handler) liveCategories(w http.ResponseWriter, ctx context.Context, v store.Viewer, now time.Time) {
	out := []liveCategory{}
	if v.Usable(now) {
		cat, err := h.catalog(ctx, v.TenantID)
		if err != nil {
			internalError(w, err)
			return
		}
		for _, c := range cat.categories {
			out = append(out, liveCategory{CategoryID: strconv.FormatInt(c.ID, 10), CategoryName: c.Name})
		}
	}
	writeJSON(w, out)
}

func (h *Handler) liveStreams(w http.ResponseWriter, ctx context.Context, v store.Viewer, now time.Time, categoryFilter string) {
	out := []liveStream{}
	if v.Usable(now) {
		cat, err := h.catalog(ctx, v.TenantID)
		if err != nil {
			internalError(w, err)
			return
		}
		for i, ch := range cat.channels {
			categoryID := cat.categoryID(ch)
			if categoryFilter != "" && categoryFilter != strconv.FormatInt(categoryID, 10) {
				continue
			}
			out = append(out, liveStream{
				Num:         i + 1,
				Name:        ch.Name,
				StreamType:  "live",
				StreamID:    ch.ID,
				StreamIcon:  ch.LogoURL,
				Added:       strconv.FormatInt(ch.CreatedAt.Unix(), 10),
				CategoryID:  strconv.FormatInt(categoryID, 10),
				CategoryIDs: []int64{categoryID},
			})
		}
	}
	writeJSON(w, out)
}

// --- get.php ve xmltv.php ---

func (h *Handler) playlist(w http.ResponseWriter, r *http.Request) {
	v, ok := h.usableViewer(w, r)
	if !ok {
		return
	}
	cat, err := h.catalog(r.Context(), v.TenantID)
	if err != nil {
		internalError(w, err)
		return
	}
	ext := "ts"
	if out := r.FormValue("output"); out == "m3u8" || out == "hls" {
		ext = "m3u8"
	}
	plain := r.FormValue("type") == "m3u"
	prefix := fmt.Sprintf("%s/live/%s/%s/", strings.TrimRight(h.base.String(), "/"), url.PathEscape(v.Username), url.PathEscape(v.Password))

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, ch := range cat.channels {
		title := oneLine(ch.Name)
		if plain {
			fmt.Fprintf(&b, "#EXTINF:-1,%s\n", title)
		} else {
			fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=\"\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
				attr(ch.Name), attr(ch.LogoURL), attr(cat.categoryName(ch)), title)
		}
		fmt.Fprintf(&b, "%s%d.%s\n", prefix, ch.ID, ext)
	}

	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="playlist.m3u"`)
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, b.String())
}

func (h *Handler) guide(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.usableViewer(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<tv generator-info-name="Kanalvo"></tv>`+"\n")
}

// usableViewer, girişi doğrular ve izleyici kullanılamıyorsa yanıtı kendisi yazar.
func (h *Handler) usableViewer(w http.ResponseWriter, r *http.Request) (store.Viewer, bool) {
	v, err := h.auth.Viewer(r, r.FormValue("username"), r.FormValue("password"))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return v, false
	case errors.Is(err, auth.ErrInvalid):
		http.Error(w, "forbidden", http.StatusForbidden)
		return v, false
	case err != nil:
		internalError(w, err)
		return v, false
	}
	if !v.Usable(h.now()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return v, false
	}
	return v, true
}

// --- ortak ---

// catalog, bir yayıncının izleyiciye gösterilen kategorileri ve kanallarıdır.
type catalog struct {
	categories []store.Category
	channels   []store.Channel
	names      map[int64]string
}

func (h *Handler) catalog(ctx context.Context, tenantID int64) (catalog, error) {
	categories, err := h.store.CategoriesByTenant(ctx, tenantID)
	if err != nil {
		return catalog{}, err
	}
	channels, err := h.store.ChannelsByTenant(ctx, tenantID)
	if err != nil {
		return catalog{}, err
	}
	c := catalog{categories: categories, channels: channels, names: map[int64]string{uncategorizedID: uncategorizedName}}
	for _, k := range categories {
		c.names[k.ID] = k.Name
	}
	for _, ch := range channels {
		if ch.CategoryID == nil {
			c.categories = append(c.categories, store.Category{ID: uncategorizedID, TenantID: tenantID, Name: uncategorizedName})
			break
		}
	}
	return c, nil
}

func (c catalog) categoryID(ch store.Channel) int64 {
	if ch.CategoryID == nil {
		return uncategorizedID
	}
	return *ch.CategoryID
}

func (c catalog) categoryName(ch store.Channel) string { return c.names[c.categoryID(ch)] }

// oneLine, M3U satır yapısını bozabilecek satır sonlarını boşluğa çevirir.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// attr, bir değeri çift tırnaklı M3U özniteliğine güvenle yazılacak hale getirir.
func attr(s string) string {
	return strings.ReplaceAll(oneLine(s), `"`, "'")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("xtream: yanıt yazılamadı: %v", err)
	}
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("xtream: veritabanı hatası: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
