// Package enroll, yeni bir sunucunun tek komutla kurulmasını sağlar. Yönetici panelden tek
// kullanımlık bir kurulum kodu alır; yeni sunucuda çalıştırılan betik bu kodla kurulum dosyalarını
// indirir ve sunucuyu kaydeder. Kaydolan sunucu, yönetici onaylayana kadar izleyici almaz.
//
// Bu uçlar izleyicilere açık adreste dinlenir ve oturum istemez: tek kimlik kurulum kodudur.
package enroll

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	edgefiles "streamhub/deploy/edge"
	"streamhub/internal/clientip"
	"streamhub/internal/store"
)

//go:embed install.sh.tmpl
var scriptSource string

var script = template.Must(template.New("install.sh").Parse(scriptSource))

// tokenPattern, NewToken'ın ürettiği biçimdir; başka biçimdeki kodlar veritabanına sorulmadan reddedilir.
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

const (
	defaultPort   = 80
	defaultWeight = 100
	refused       = "Kurulum kodu geçersiz, kullanılmış ya da süresi dolmuş. Panelden yeni bir kod alın.\n"
)

type Config struct {
	// PublicBaseURL, yeni sunucunun ana sunucuya ulaşacağı adrestir.
	PublicBaseURL string
	// IngestURL, yayıncıların OBS'e yazdığı adrestir; yeni sunucu yayını bu sunucudan çeker.
	IngestURL string
	// TrustProxyHeaders için bkz. clientip.Key. Yeni sunucunun adresi kayıt isteğinden okunur.
	TrustProxyHeaders bool
}

type Handler struct {
	store *store.Store
	cfg   Config
	// newKey, kaydolan sunucunun anahtarını üretir.
	newKey func() string
}

func New(s *store.Store, cfg Config, newKey func() string) *Handler {
	return &Handler{store: s, cfg: cfg, newKey: newKey}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /edge/install/{token}", h.install)
	mux.HandleFunc("POST /edge/enroll/{token}", h.enroll)
}

// Command, yeni sunucuda çalıştırılacak kurulum komutudur.
func Command(publicBaseURL, token string) string {
	return fmt.Sprintf("curl -fsSL %s/edge/install/%s | sudo sh", publicBaseURL, token)
}

// OriginRTMP, yayın adresinden (rtmp://sunucu[:port]/live) sunucunun çekeceği "sunucu:port" değerini üretir.
func OriginRTMP(ingestURL string) string {
	u, err := url.Parse(ingestURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "1935"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// install, kurulum betiğini verir. Kodu tüketmez: betik yarıda kalırsa aynı komut yeniden çalıştırılabilir.
func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !h.open(w, r, token) {
		return
	}
	var out bytes.Buffer
	err := script.Execute(&out, map[string]string{
		"ControlURL": shellQuote(h.cfg.PublicBaseURL),
		"Token":      shellQuote(token),
		"Compose":    strings.TrimRight(edgefiles.Compose, "\n"),
		"Nginx":      strings.TrimRight(edgefiles.Nginx, "\n"),
		"SRS":        strings.TrimRight(edgefiles.SRS, "\n"),
	})
	if err != nil {
		h.internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(out.Bytes())
}

func (h *Handler) open(w http.ResponseWriter, r *http.Request, token string) bool {
	if !tokenPattern.MatchString(token) {
		http.Error(w, refused, http.StatusNotFound)
		return false
	}
	open, err := h.store.EdgeEnrollmentOpen(r.Context(), token)
	if err != nil {
		h.internal(w, err)
		return false
	}
	if !open {
		http.Error(w, refused, http.StatusNotFound)
	}
	return open
}

// enroll, kodu kullanır: isteğin geldiği adresi sunucunun adresi olarak kaydeder ve sunucunun
// .env dosyasına yazacağı satırları döner.
func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !tokenPattern.MatchString(token) {
		http.Error(w, refused, http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	port := defaultPort
	if raw := r.PostFormValue("port"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			http.Error(w, "Port 1 ile 65535 arasında olmalı.\n", http.StatusBadRequest)
			return
		}
		port = n
	}
	ip := clientip.Addr(r, h.cfg.TrustProxyHeaders)
	if !ip.IsValid() {
		http.Error(w, "Sunucunun adresi belirlenemedi.\n", http.StatusBadRequest)
		return
	}
	host := ip.String()
	if ip.Is6() {
		host = "[" + host + "]"
	}
	base := "http://" + host
	if port != defaultPort {
		base += ":" + strconv.Itoa(port)
	}
	key := h.newKey()
	_, err := h.store.RedeemEdgeEnrollment(r.Context(), token, store.NewEdge{
		Name: "Sunucu " + ip.String(), BaseURL: base, ControlURL: base, Key: key, PullIP: ip.String(), Weight: defaultWeight,
	})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, refused, http.StatusNotFound)
		return
	}
	if err != nil {
		h.internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "CONTROL_URL=%s\nEDGE_KEY=%s\nORIGIN_RTMP=%s\nEDGE_PORT=%d\n",
		h.cfg.PublicBaseURL, key, OriginRTMP(h.cfg.IngestURL), port)
}

func (h *Handler) internal(w http.ResponseWriter, err error) {
	log.Printf("enroll: %v", err)
	http.Error(w, "Sunucu hatası.\n", http.StatusInternalServerError)
}

// shellQuote, değeri kabukta tek bir sözcük olarak okunacak biçimde tırnaklar.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
