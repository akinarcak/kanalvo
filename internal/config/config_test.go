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
		"DATABASE_URL":  "postgres://x",
		"TOKEN_KEY":     strings.Repeat("k", 32),
		"HOOK_SECRET":   strings.Repeat("h", 16),
		"EDGE_BASE_URL": "http://edge:8080/",
		"SRS_API_URL":   "http://srs:1985",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8000" || c.TokenTTL != 5*time.Minute {
		t.Fatalf("varsayılanlar yanlış: %+v", c)
	}
	if c.EdgeBaseURL != "http://edge:8080" {
		t.Fatalf("sondaki / silinmeli: %q", c.EdgeBaseURL)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]func(map[string]string){
		"eksik DATABASE_URL":  func(m map[string]string) { delete(m, "DATABASE_URL") },
		"eksik EDGE_BASE_URL": func(m map[string]string) { delete(m, "EDGE_BASE_URL") },
		"eksik SRS_API_URL":   func(m map[string]string) { delete(m, "SRS_API_URL") },
		"kısa TOKEN_KEY":      func(m map[string]string) { m["TOKEN_KEY"] = "short" },
		"kısa HOOK_SECRET":    func(m map[string]string) { m["HOOK_SECRET"] = "short" },
		"bozuk TOKEN_TTL":     func(m map[string]string) { m["TOKEN_TTL"] = "abc" },
		"negatif TOKEN_TTL":   func(m map[string]string) { m["TOKEN_TTL"] = "-1m" },
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
