package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	// HTTPAddr, izleyicilere açık dinleme adresidir.
	HTTPAddr string
	// HooksAddr, SRS yetki sorgularının dinlendiği adrestir; dışarıya açılmamalıdır.
	HooksAddr   string
	DatabaseURL string
	TokenKey    []byte
	HookSecret  string
	// EdgeTSBaseURL, izleyicinin kesintisiz .ts için yönlendirildiği dış adrestir.
	EdgeTSBaseURL string
	// EdgeHLSBaseURL, izleyicinin HLS için yönlendirildiği /hls geçidinin dış adresidir.
	EdgeHLSBaseURL string
	SRSAPIURL      string
	// SRSHLSURL, geçidin HLS dosyalarını çektiği SRS HTTP sunucusunun iç adresidir.
	SRSHLSURL   string
	TokenTTL    time.Duration
	HLSTokenTTL time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		HTTPAddr:       getenv("HTTP_ADDR"),
		HooksAddr:      getenv("HOOKS_ADDR"),
		DatabaseURL:    getenv("DATABASE_URL"),
		TokenKey:       []byte(getenv("TOKEN_KEY")),
		HookSecret:     getenv("HOOK_SECRET"),
		EdgeTSBaseURL:  strings.TrimRight(getenv("EDGE_TS_BASE_URL"), "/"),
		EdgeHLSBaseURL: strings.TrimRight(getenv("EDGE_HLS_BASE_URL"), "/"),
		SRSAPIURL:      strings.TrimRight(getenv("SRS_API_URL"), "/"),
		SRSHLSURL:      strings.TrimRight(getenv("SRS_HLS_URL"), "/"),
		TokenTTL:       5 * time.Minute,
		HLSTokenTTL:    6 * time.Hour,
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8000"
	}
	if c.HooksAddr == "" {
		c.HooksAddr = ":8001"
	}
	if c.HooksAddr == c.HTTPAddr {
		return Config{}, fmt.Errorf("HOOKS_ADDR ile HTTP_ADDR aynı olamaz: %q", c.HTTPAddr)
	}
	for name, dst := range map[string]*time.Duration{"TOKEN_TTL": &c.TokenTTL, "HLS_TOKEN_TTL": &c.HLSTokenTTL} {
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
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL boş olamaz")
	}
	for name, v := range map[string]string{
		"EDGE_TS_BASE_URL":  c.EdgeTSBaseURL,
		"EDGE_HLS_BASE_URL": c.EdgeHLSBaseURL,
		"SRS_API_URL":       c.SRSAPIURL,
		"SRS_HLS_URL":       c.SRSHLSURL,
	} {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Config{}, fmt.Errorf("%s http(s) adresi olmalı: %q", name, v)
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
