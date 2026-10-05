// Package hlsgw, HLS çalma listelerini ve parçalarını izleyiciye veren geçittir.
//
// SRS, HLS parçalarını hiçbir denetim yapmadan sunar ve parça adları tahmin edilebilir
// (bkz. docs/srs-findings.md). Bu yüzden SRS'in HTTP portu dışarıya açılmaz; her HLS isteği
// buradan geçer, imza ve izleyici durumu her seferinde doğrulanır.
package hlsgw

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"streamhub/internal/store"
	"streamhub/internal/token"
)

// filePattern: "<kanal>.m3u8" veya "<kanal>-<sıra>.ts".
var filePattern = regexp.MustCompile(`^([1-9][0-9]*)(\.m3u8|-[0-9]+\.ts)$`)

type Handler struct {
	store  *store.Store
	signer *token.Signer
	now    func() time.Time
	proxy  *httputil.ReverseProxy
}

// New, upstream olarak SRS HTTP sunucusunun iç adresini alır.
func New(s *store.Store, signer *token.Signer, upstream *url.URL, now func() time.Time) *Handler {
	base := strings.TrimRight(upstream.Path, "/")
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			// İmza ve izleyicinin sorgu parametreleri SRS'e aktarılmaz.
			pr.Out.URL.Path = base + "/live/" + pr.In.PathValue("file")
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = ""
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("hlsgw: SRS'e ulaşılamadı: %v", err)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
	return &Handler{store: s, signer: signer, now: now, proxy: proxy}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /hls/{token}/{file}", h.serve)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	m := filePattern.FindStringSubmatch(r.PathValue("file"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	channelID, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	now := h.now()

	claims, err := h.signer.Verify(r.PathValue("token"), now)
	if err != nil || claims.ChannelID != channelID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	v, err := h.store.ViewerByID(r.Context(), claims.ViewerID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err != nil {
		internalError(w, err)
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
	if !v.Usable(now) || v.TenantID != ch.TenantID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !ch.Live {
		http.NotFound(w, r)
		return
	}
	h.proxy.ServeHTTP(w, r)
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("hlsgw: veritabanı hatası: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
