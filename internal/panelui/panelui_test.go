package panelui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

var built = fstest.MapFS{
	"index.html":           {Data: []byte("<!doctype html><title>Panel</title>")},
	"assets/app-abc123.js": {Data: []byte("console.log('panel')")},
	".gitkeep":             {Data: nil},
}

func TestServesTheAppForEveryPageRoute(t *testing.T) {
	h := New(built)
	for _, path := range []string{"/", "/kanallar", "/yayincilar/12", "/index.html"} {
		rec := get(h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Panel</title>") {
			t.Errorf("%s: durum %d gövde %q", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: sayfa önbelleğe alınmamalı (yeni sürüm hemen görünsün)", path)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: içerik türü %q", path, ct)
		}
	}
}

func TestServesAssetsWithLongCache(t *testing.T) {
	rec := get(New(built), "/assets/app-abc123.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('panel')" {
		t.Fatalf("durum %d gövde %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("adı içerik özetli dosyalar kalıcı önbelleğe alınmalı: %q", cc)
	}
}

// Eksik dosya veya bilinmeyen API yolu için sayfa değil 404 dönmeli; aksi halde tarayıcı
// HTML'i betik ya da JSON sanıp anlaşılmaz hatalar verir.
func TestMissingAssetsAndAPIPathsAreNotFound(t *testing.T) {
	h := New(built)
	for _, path := range []string{"/assets/yok.js", "/assets/", "/api/bilinmeyen", "/api", "/.gitkeep", "/assets"} {
		rec := get(h, path)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently {
			t.Errorf("%s: durum %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<title>Panel</title>") {
			t.Errorf("%s sayfayı döndürmemeli", path)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := New(built)
	for _, path := range []string{"/", "/assets/app-abc123.js", "/assets/yok.js"} {
		hdr := get(h, path).Header()
		csp := hdr.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "script-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q içinde %q yok", path, csp, want)
			}
		}
		if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: CSP satır içi betiğe izin vermemeli: %q", path, csp)
		}
		if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("X-Frame-Options") != "DENY" || hdr.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: güvenlik başlıkları eksik: %v", path, hdr)
		}
	}
}

func TestUnbuiltPanelExplainsItself(t *testing.T) {
	rec := get(New(fstest.MapFS{".gitkeep": {}}), "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "derlenmemiş") {
		t.Fatalf("durum %d gövde %q", rec.Code, rec.Body.String())
	}
}

func TestOnlyGETIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	New(built).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("durum %d", rec.Code)
	}
}
