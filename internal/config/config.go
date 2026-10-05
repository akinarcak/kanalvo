package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// HTTPAddr, izleyicilere açık dinleme adresidir.
	HTTPAddr string
	// HooksAddr, SRS yetki sorgularının dinlendiği adrestir; dışarıya açılmamalıdır.
	HooksAddr string
	// PanelAddr, yönetim panelinin dinlendiği adrestir. İzleyici uçlarından ayrıdır; böylece
	// panel yalnızca HTTPS vekilinin arkasından ya da belirli ağlardan erişilebilir kılınabilir.
	PanelAddr       string
	PanelSessionTTL time.Duration
	// PanelInsecureCookie, panel çerezinin HTTP üzerinden de gönderilmesine izin verir.
	// Yalnızca yerel geliştirmede açılır.
	PanelInsecureCookie bool
	// PanelTrustProxyHeaders, panel isteklerinde istemci IP'sinin X-Forwarded-For başlığından
	// alınmasını sağlar. Panel bir HTTPS vekilinin arkasındaysa açılmalıdır; aksi halde tüm
	// girişler vekilin adresinden geliyor görünür ve hatalı giriş sınırı herkesi birden engeller.
	// İzleyici uçlarının ayarından (TrustProxyHeaders) bağımsızdır.
	PanelTrustProxyHeaders bool
	// IngestBaseURL, yayıncıların OBS'e yazacağı sunucu adresidir (ör. rtmp://yayin.example.com/live).
	IngestBaseURL string
	DatabaseURL string
	TokenKey    []byte
	HookSecret  string
	// PublicBaseURL, oynatıcıların Xtream uçlarına ulaştığı dış adrestir; M3U listesindeki
	// yayın adresleri ve giriş yanıtındaki sunucu bilgisi bundan üretilir.
	PublicBaseURL string
	// EdgeTSBaseURL, izleyicinin kesintisiz .ts için yönlendirildiği dış adrestir.
	EdgeTSBaseURL string
	// EdgeHLSBaseURL, izleyicinin HLS için yönlendirildiği /hls geçidinin dış adresidir.
	EdgeHLSBaseURL string
	SRSAPIURL      string
	// SRSTSAPIURL, .ts dağıtıcısının yönetim API'sidir; izleyici bağlantılarını listelemek ve kesmek için kullanılır.
	SRSTSAPIURL string
	// SRSHLSURL, geçidin HLS dosyalarını çektiği SRS HTTP sunucusunun iç adresidir.
	SRSHLSURL   string
	TokenTTL    time.Duration
	HLSTokenTTL time.Duration
	// TrustProxyHeaders, istemci IP'sinin X-Forwarded-For başlığından alınmasını sağlar.
	// Yalnızca API bu başlığı kendisi yazan bir vekilin arkasındayken açılmalıdır.
	TrustProxyHeaders bool
	// Bir IP, LoginFailureWindow içinde LoginMaxFailures hatalı giriş yaparsa pencere bitene kadar engellenir.
	LoginMaxFailures   int
	LoginFailureWindow time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		HTTPAddr:           getenv("HTTP_ADDR"),
		HooksAddr:          getenv("HOOKS_ADDR"),
		PanelAddr:          getenv("PANEL_ADDR"),
		PanelSessionTTL:    12 * time.Hour,
		IngestBaseURL:      strings.TrimRight(getenv("INGEST_BASE_URL"), "/"),
		DatabaseURL:        getenv("DATABASE_URL"),
		TokenKey:           []byte(getenv("TOKEN_KEY")),
		HookSecret:         getenv("HOOK_SECRET"),
		PublicBaseURL:      strings.TrimRight(getenv("PUBLIC_BASE_URL"), "/"),
		EdgeTSBaseURL:      strings.TrimRight(getenv("EDGE_TS_BASE_URL"), "/"),
		EdgeHLSBaseURL:     strings.TrimRight(getenv("EDGE_HLS_BASE_URL"), "/"),
		SRSAPIURL:          strings.TrimRight(getenv("SRS_API_URL"), "/"),
		SRSTSAPIURL:        strings.TrimRight(getenv("SRS_TS_API_URL"), "/"),
		SRSHLSURL:          strings.TrimRight(getenv("SRS_HLS_URL"), "/"),
		TokenTTL:           5 * time.Minute,
		HLSTokenTTL:        6 * time.Hour,
		LoginMaxFailures:   20,
		LoginFailureWindow: 5 * time.Minute,
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8000"
	}
	if c.HooksAddr == "" {
		c.HooksAddr = ":8001"
	}
	if c.PanelAddr == "" {
		c.PanelAddr = ":8002"
	}
	if c.HooksAddr == c.HTTPAddr || c.PanelAddr == c.HTTPAddr || c.PanelAddr == c.HooksAddr {
		return Config{}, fmt.Errorf("HTTP_ADDR, HOOKS_ADDR ve PANEL_ADDR birbirinden farklı olmalı")
	}
	for name, dst := range map[string]*time.Duration{
		"TOKEN_TTL":            &c.TokenTTL,
		"HLS_TOKEN_TTL":        &c.HLSTokenTTL,
		"LOGIN_FAILURE_WINDOW": &c.LoginFailureWindow,
		"PANEL_SESSION_TTL":    &c.PanelSessionTTL,
	} {
		v := getenv(name)
		if v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s geçersiz: %q", name, v)
		}
		*dst = d
	}
	if v := getenv("LOGIN_MAX_FAILURES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("LOGIN_MAX_FAILURES pozitif bir sayı olmalı: %q", v)
		}
		c.LoginMaxFailures = n
	}
	for name, dst := range map[string]*bool{
		"TRUST_PROXY_HEADERS":       &c.TrustProxyHeaders,
		"PANEL_INSECURE_COOKIE":     &c.PanelInsecureCookie,
		"PANEL_TRUST_PROXY_HEADERS": &c.PanelTrustProxyHeaders,
	} {
		v := getenv(name)
		if v == "" {
			continue
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s true veya false olmalı: %q", name, v)
		}
		*dst = b
	}
	if u, err := url.Parse(c.IngestBaseURL); err != nil || (u.Scheme != "rtmp" && u.Scheme != "rtmps") || u.Host == "" {
		return Config{}, fmt.Errorf("INGEST_BASE_URL rtmp:// adresi olmalı: %q", c.IngestBaseURL)
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL boş olamaz")
	}
	for name, v := range map[string]string{
		"PUBLIC_BASE_URL":   c.PublicBaseURL,
		"EDGE_TS_BASE_URL":  c.EdgeTSBaseURL,
		"EDGE_HLS_BASE_URL": c.EdgeHLSBaseURL,
		"SRS_API_URL":       c.SRSAPIURL,
		"SRS_TS_API_URL":    c.SRSTSAPIURL,
		"SRS_HLS_URL":       c.SRSHLSURL,
	} {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Config{}, fmt.Errorf("%s http(s) adresi olmalı: %q", name, v)
		}
		// Oynatıcılar sunucuyu alan adı ve port olarak kaydeder; alt yol taşıyamazlar.
		if name == "PUBLIC_BASE_URL" && u.Path != "" {
			return Config{}, fmt.Errorf("PUBLIC_BASE_URL yol içeremez: %q", v)
		}
	}
	if len(c.TokenKey) < 32 {
		return Config{}, fmt.Errorf("TOKEN_KEY en az 32 bayt olmalı")
	}
	if len(c.HookSecret) < 16 {
		return Config{}, fmt.Errorf("HOOK_SECRET en az 16 karakter olmalı")
	}
	return c, nil
}
