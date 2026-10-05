package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func valid() map[string]string {
	return map[string]string{
		"DATABASE_URL":      "postgres://x",
		"TOKEN_KEY":         strings.Repeat("k", 32),
		"HOOK_SECRET":       strings.Repeat("h", 16),
		"EDGE_TS_BASE_URL":  "http://edge:8081/",
		"EDGE_HLS_BASE_URL": "http://edge:8000/",
		"SRS_API_URL":       "http://srs:1985",
		"SRS_HLS_URL":       "http://srs:8080/",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8000" || c.HooksAddr != ":8001" || c.TokenTTL != 5*time.Minute || c.HLSTokenTTL != 6*time.Hour {
		t.Fatalf("varsayılanlar yanlış: %+v", c)
	}
	if c.EdgeTSBaseURL != "http://edge:8081" || c.EdgeHLSBaseURL != "http://edge:8000" || c.SRSHLSURL != "http://srs:8080" {
		t.Fatalf("sondaki / silinmeli: %+v", c)
	}
}

func TestLoadDurations(t *testing.T) {
	m := valid()
	m["TOKEN_TTL"], m["HLS_TOKEN_TTL"] = "90s", "2h"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.TokenTTL != 90*time.Second || c.HLSTokenTTL != 2*time.Hour {
		t.Fatalf("süreler yanlış: %+v", c)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]func(map[string]string){
		"eksik DATABASE_URL":      func(m map[string]string) { delete(m, "DATABASE_URL") },
		"eksik EDGE_TS_BASE_URL":  func(m map[string]string) { delete(m, "EDGE_TS_BASE_URL") },
		"eksik EDGE_HLS_BASE_URL": func(m map[string]string) { delete(m, "EDGE_HLS_BASE_URL") },
		"eksik SRS_API_URL":       func(m map[string]string) { delete(m, "SRS_API_URL") },
		"eksik SRS_HLS_URL":       func(m map[string]string) { delete(m, "SRS_HLS_URL") },
		"bozuk SRS_HLS_URL":       func(m map[string]string) { m["SRS_HLS_URL"] = "srs:8080" },
		"kısa TOKEN_KEY":          func(m map[string]string) { m["TOKEN_KEY"] = "short" },
		"kısa HOOK_SECRET":        func(m map[string]string) { m["HOOK_SECRET"] = "short" },
		"bozuk TOKEN_TTL":         func(m map[string]string) { m["TOKEN_TTL"] = "abc" },
		"negatif TOKEN_TTL":       func(m map[string]string) { m["TOKEN_TTL"] = "-1m" },
		"bozuk HLS_TOKEN_TTL":     func(m map[string]string) { m["HLS_TOKEN_TTL"] = "abc" },
		"aynı dinleme adresi":     func(m map[string]string) { m["HTTP_ADDR"], m["HOOKS_ADDR"] = ":9000", ":9000" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			mutate(m)
			if _, err := Load(env(m)); err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}
