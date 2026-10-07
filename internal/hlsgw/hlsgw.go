// Package hlsgw, HLS çalma listelerini ve parçalarını izleyiciye veren geçittir.
//
// SRS, HLS parçalarını hiçbir denetim yapmadan sunar ve parça adları tahmin edilebilir
// (bkz. docs/srs-findings.md). Bu yüzden SRS'in HTTP portu dışarıya açılmaz; her HLS isteği
// buradan geçer; imza, izleyici durumu ve bağlantı limiti her seferinde doğrulanır.
//
// Yerel edge'de geçit dosyayı kendisi verir. Uzak edge'de izleyici edge'in nginx'ine bağlanır;
// nginx her isteği buraya sorar, kabul edilirse dosyayı kendi önbelleğinden (yoksa buradan
// çekerek) verir. Yetki kararı her iki durumda da aynı koddadır.
package hlsgw

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kanalvo/internal/balancer"
	"kanalvo/internal/clientip"
	"kanalvo/internal/session"
	"kanalvo/internal/store"
	"kanalvo/internal/token"
)

// filePattern: "<kanal>.m3u8" veya "<kanal>-<sıra>.ts".
var filePattern = regexp.MustCompile(`^([1-9][0-9]*)(\.m3u8|-[0-9]+\.ts)$`)

const (
	// realIPHeader: edge'in nginx'i izleyicinin adresini bu başlıkla bildirir. Yalnızca geçerli
	// edge anahtarıyla gelen isteklerde okunur.
	realIPHeader = "X-Real-IP"
	// accelPrefix: edge'in nginx'inde dosyayı önbellekten veren iç konum (bkz. deploy/edge).
	accelPrefix = "/_segment/"
	// Edge önbelleğinin süreleri: çalma listesi her parça süresinde (2 sn) değişir, parçalar değişmez.
	playlistCache = "max-age=1"
	segmentCache  = "max-age=60"
)

// Edges, uzak edge'leri anahtarlarıyla tanır (bkz. balancer.Balancer).
type Edges interface {
	ByKey(ctx context.Context, key string) (store.Edge, bool, error)
}

type Handler struct {
	store             *store.Store
	signer            *token.Signer
	sessions          *session.Manager
	edges             Edges
	localEdgeID       int64
	trustProxyHeaders bool
	now               func() time.Time
	proxy             *httputil.ReverseProxy // izleyiciye doğrudan
	edgeProxy         *httputil.ReverseProxy // edge'in önbelleğine
}

// New, upstream olarak SRS HTTP sunucusunun iç adresini alır. SRS upstreamTimeout içinde
// yanıt vermeye başlamazsa istek 502 ile sonlanır. trustProxyHeaders için bkz. clientip.Key.
// localEdgeID, bu geçitten doğrudan izleyenlerin oturumlarının yazıldığı yerel edge'dir.
func New(s *store.Store, signer *token.Signer, sessions *session.Manager, edges Edges, localEdgeID int64, upstream *url.URL,
	upstreamTimeout time.Duration, trustProxyHeaders bool, now func() time.Time) *Handler {
	base := strings.TrimRight(upstream.Path, "/")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = upstreamTimeout
	newProxy := func(modify func(*http.Response) error) *httputil.ReverseProxy {
		return &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(upstream)
				// İmza, edge anahtarı ve izleyicinin sorgu parametreleri SRS'e aktarılmaz.
				pr.Out.URL.Path = base + "/live/" + pr.In.PathValue("file")
				pr.Out.URL.RawPath = ""
				pr.Out.URL.RawQuery = ""
				pr.Out.Header.Del(balancer.KeyHeader)
			},
			ModifyResponse: modify,
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				log.Printf("hlsgw: SRS'e ulaşılamadı: %v", err)
				http.Error(w, "bad gateway", http.StatusBadGateway)
			},
		}
	}
	return &Handler{
		store: s, signer: signer, sessions: sessions, edges: edges, localEdgeID: localEdgeID,
		trustProxyHeaders: trustProxyHeaders, now: now,
		proxy:     newProxy(nil),
		edgeProxy: newProxy(setEdgeCaching),
	}
}

// setEdgeCaching, edge'in nginx'ine dosyayı ne kadar süre önbellekte tutacağını söyler.
// Başarısız yanıtlar önbelleğe alınmaz.
func setEdgeCaching(resp *http.Response) error {
	switch {
	case resp.StatusCode != http.StatusOK:
		resp.Header.Set("Cache-Control", "no-store")
	case strings.HasSuffix(resp.Request.URL.Path, ".m3u8"):
		resp.Header.Set("Cache-Control", playlistCache)
	default:
		resp.Header.Set("Cache-Control", segmentCache)
	}
	return nil
}

// Register, izleyicinin doğrudan kullandığı yerel geçidi kaydeder.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /hls/{token}/{file}", h.serve)
}

// RegisterEdge, uzak edge'lerin nginx'inin kullandığı uçları kaydeder. İkisi de edge anahtarı ister:
//
//	/edge/hls/<imza>/<dosya> : izleyicinin isteğini yetkilendirir; kabul edilirse nginx'i dosyayı
//	                           önbelleğinden vermeye yönlendirir (X-Accel-Redirect)
//	/edge/segment/<dosya>    : nginx'in önbelleğini doldurması için dosyayı verir
func (h *Handler) RegisterEdge(mux *http.ServeMux) {
	mux.HandleFunc("GET /edge/hls/{token}/{file}", h.serveEdgeAuth)
	mux.HandleFunc("GET /edge/segment/{file}", h.serveEdgeFile)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	if h.authorize(w, r, h.localEdgeID, clientip.Key(r, h.trustProxyHeaders)) {
		h.proxy.ServeHTTP(w, r)
	}
}

func (h *Handler) serveEdgeAuth(w http.ResponseWriter, r *http.Request) {
	edge, ok := h.edge(w, r)
	if !ok {
		return
	}
	// İzleyicinin adresini edge bildirir; edge'in kimliği yukarıda doğrulandı.
	if !h.authorize(w, r, edge.ID, clientip.Normalize(r.Header.Get(realIPHeader))) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Redirect", accelPrefix+r.PathValue("file"))
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) serveEdgeFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.edge(w, r); !ok {
		return
	}
	if !filePattern.MatchString(r.PathValue("file")) {
		http.NotFound(w, r)
		return
	}
	h.edgeProxy.ServeHTTP(w, r)
}

// edge, isteği gönderen uzak edge'i anahtarından tanır; tanıyamazsa yanıtı yazar.
func (h *Handler) edge(w http.ResponseWriter, r *http.Request) (store.Edge, bool) {
	edge, known, err := h.edges.ByKey(r.Context(), r.Header.Get(balancer.KeyHeader))
	if err != nil {
		internalError(w, err)
		return store.Edge{}, false
	}
	if !known {
		http.Error(w, "forbidden", http.StatusForbidden)
		return store.Edge{}, false
	}
	return edge, true
}

// authorize, bir HLS isteğinin izlenip izlenemeyeceğine karar verir ve oturumuna işler.
// İzin yoksa yanıtı yazar ve false döner. ip, izleyicinin clientip biçimindeki adresidir.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, edgeID int64, ip string) bool {
	m := filePattern.FindStringSubmatch(r.PathValue("file"))
	if m == nil {
		http.NotFound(w, r)
		return false
	}
	channelID, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return false
	}
	now := h.now()

	claims, err := h.signer.Verify(r.PathValue("token"), now)
	if err != nil || claims.Kind != token.KindHLS || claims.ChannelID != channelID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	v, err := h.store.ViewerByID(r.Context(), claims.ViewerID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if err != nil {
		internalError(w, err)
		return false
	}
	ch, err := h.store.ChannelByID(r.Context(), channelID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return false
	}
	if err != nil {
		internalError(w, err)
		return false
	}
	if !v.Usable(now) || v.TenantID != ch.TenantID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if !ch.Live {
		http.NotFound(w, r)
		return false
	}

	// Oturumun kimliği imzadaki anahtardır ve aynı anda tek bir ağdan kullanılabilir: ağ değiştiren
	// izleyici devam eder, adresi paylaşan ikinci kişi ise ilkini düşürür ve kısa süre geri alınamaz.
	err = h.sessions.TouchHLS(r.Context(), edgeID, v.ID, ch.ID, claims.Session, ip)
	switch {
	case errors.Is(err, store.ErrSessionRevoked), errors.Is(err, store.ErrSessionMoved), errors.Is(err, store.ErrTenantConnectionLimit):
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	case err != nil:
		internalError(w, err)
		return false
	}
	return true
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("hlsgw: veritabanı hatası: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
