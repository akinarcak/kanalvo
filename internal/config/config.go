package config

import (
	"fmt"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	TokenKey    []byte
	HookSecret  string
	EdgeBaseURL string
	SRSAPIURL   string
	TokenTTL    time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		HTTPAddr:    getenv("HTTP_ADDR"),
		DatabaseURL: getenv("DATABASE_URL"),
		TokenKey:    []byte(getenv("TOKEN_KEY")),
		HookSecret:  getenv("HOOK_SECRET"),
		EdgeBaseURL: strings.TrimRight(getenv("EDGE_BASE_URL"), "/"),
		SRSAPIURL:   strings.TrimRight(getenv("SRS_API_URL"), "/"),
		TokenTTL:    5 * time.Minute,
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8000"
	}
	if v := getenv("TOKEN_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("TOKEN_TTL geçersiz: %q", v)
		}
		c.TokenTTL = d
	}
	for name, v := range map[string]string{
		"DATABASE_URL":  c.DatabaseURL,
		"EDGE_BASE_URL": c.EdgeBaseURL,
		"SRS_API_URL":   c.SRSAPIURL,
	} {
		if v == "" {
			return Config{}, fmt.Errorf("%s boş olamaz", name)
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
