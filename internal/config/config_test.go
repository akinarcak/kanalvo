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
		"PUBLIC_BASE_URL":   "http://tv.example.com:8000/",
		"INGEST_BASE_URL":   "rtmp://yayin.example.com/live/",
		"SRS_API_URL":       "http://srs:1985",
		"SRS_TS_API_URL":    "http://srs-ts:1985/",
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
	if c.PublicBaseURL != "http://tv.example.com:8000" || c.TrustProxyHeaders || c.LoginMaxFailures != 20 || c.LoginFailureWindow != 5*time.Minute {
		t.Fatalf("giriş ayarı varsayılanları yanlış: %+v", c)
	}
	if c.PanelAddr != ":8002" || c.PanelSessionTTL != 12*time.Hour || c.PanelInsecureCookie || c.IngestBaseURL != "rtmp://yayin.example.com/live" {
		t.Fatalf("panel ayarı varsayılanları yanlış: %+v", c)
	}
	if c.SRSTSAPIURL != "http://srs-ts:1985" {
		t.Fatalf("SRS_TS_API_URL: %q", c.SRSTSAPIURL)
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

func TestLoadLoginSettings(t *testing.T) {
	m := valid()
	m["TRUST_PROXY_HEADERS"], m["LOGIN_MAX_FAILURES"], m["LOGIN_FAILURE_WINDOW"] = "true", "7", "90s"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if !c.TrustProxyHeaders || c.LoginMaxFailures != 7 || c.LoginFailureWindow != 90*time.Second {
		t.Fatalf("giriş ayarları yanlış: %+v", c)
	}
}

func TestLoadPanelSettings(t *testing.T) {
	m := valid()
	m["PANEL_ADDR"], m["PANEL_SESSION_TTL"], m["PANEL_INSECURE_COOKIE"] = ":9002", "30m", "true"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.PanelAddr != ":9002" || c.PanelSessionTTL != 30*time.Minute || !c.PanelInsecureCookie {
		t.Fatalf("panel ayarları yanlış: %+v", c)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]func(map[string]string){
		"eksik DATABASE_URL":      func(m map[string]string) { delete(m, "DATABASE_URL") },
		"eksik EDGE_TS_BASE_URL":  func(m map[string]string) { delete(m, "EDGE_TS_BASE_URL") },
		"eksik EDGE_HLS_BASE_URL": func(m map[string]string) { delete(m, "EDGE_HLS_BASE_URL") },
		"eksik SRS_API_URL":       func(m map[string]string) { delete(m, "SRS_API_URL") },
		"eksik SRS_TS_API_URL":    func(m map[string]string) { delete(m, "SRS_TS_API_URL") },
		"eksik SRS_HLS_URL":       func(m map[string]string) { delete(m, "SRS_HLS_URL") },
		"bozuk SRS_HLS_URL":       func(m map[string]string) { m["SRS_HLS_URL"] = "srs:8080" },
		"kısa TOKEN_KEY":          func(m map[string]string) { m["TOKEN_KEY"] = "short" },
		"kısa HOOK_SECRET":        func(m map[string]string) { m["HOOK_SECRET"] = "short" },
		"bozuk TOKEN_TTL":         func(m map[string]string) { m["TOKEN_TTL"] = "abc" },
		"negatif TOKEN_TTL":       func(m map[string]string) { m["TOKEN_TTL"] = "-1m" },
		"bozuk HLS_TOKEN_TTL":     func(m map[string]string) { m["HLS_TOKEN_TTL"] = "abc" },
		"eksik PUBLIC_BASE_URL":   func(m map[string]string) { delete(m, "PUBLIC_BASE_URL") },
		"yollu PUBLIC_BASE_URL":   func(m map[string]string) { m["PUBLIC_BASE_URL"] = "http://tv.example.com/iptv" },
		"bozuk TRUST_PROXY":       func(m map[string]string) { m["TRUST_PROXY_HEADERS"] = "belki" },
		"sıfır LOGIN_MAX":         func(m map[string]string) { m["LOGIN_MAX_FAILURES"] = "0" },
		"bozuk LOGIN_MAX":         func(m map[string]string) { m["LOGIN_MAX_FAILURES"] = "çok" },
		"bozuk LOGIN_WINDOW":      func(m map[string]string) { m["LOGIN_FAILURE_WINDOW"] = "abc" },
		"aynı dinleme adresi":     func(m map[string]string) { m["HTTP_ADDR"], m["HOOKS_ADDR"] = ":9000", ":9000" },
		"panel aynı adreste":      func(m map[string]string) { m["PANEL_ADDR"] = ":8000" },
		"panel sorgu adresinde":   func(m map[string]string) { m["PANEL_ADDR"] = ":8001" },
		"eksik INGEST_BASE_URL":   func(m map[string]string) { delete(m, "INGEST_BASE_URL") },
		"http INGEST_BASE_URL":    func(m map[string]string) { m["INGEST_BASE_URL"] = "http://yayin.example.com/live" },
		"bozuk PANEL_SESSION_TTL": func(m map[string]string) { m["PANEL_SESSION_TTL"] = "uzun" },
		"bozuk PANEL_INSECURE":    func(m map[string]string) { m["PANEL_INSECURE_COOKIE"] = "belki" },
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
