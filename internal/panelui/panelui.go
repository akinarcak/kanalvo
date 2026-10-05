// Package panelui, derlenmiş yönetim paneli arayüzünü (web/ klasöründeki React uygulaması)
// programın içine gömer ve sunar.
package panelui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist, `npm run build` çıktısıdır (bkz. web/vite.config.ts). Depoda yalnızca .gitkeep bulunur;
// Dockerfile arayüzü derleyip buraya yazar.
//
//go:embed all:dist
var dist embed.FS

// Embedded, programa gömülü arayüzü döner.
func Embedded() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist klasörü derleme zamanında gömülür; bulunmaması programlama hatasıdır
	}
	return sub
}

// contentSecurityPolicy: arayüz yalnızca kendi betiklerini çalıştırır ve yalnızca kendi API'siyle
// konuşur. Kanal logoları dış adreslerden görüntü olarak yüklenebilir.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https: http:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// New, tek sayfalı uygulamayı sunar: /assets/ altındaki dosyalar olduğu gibi, diğer tüm yollar
// index.html ile yanıtlanır (sayfa yönlendirmesini tarayıcıdaki uygulama yapar).
func New(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		switch {
		case clean == "/api" || strings.HasPrefix(clean, "/api/"):
			http.NotFound(w, r)
		case clean == "/assets":
			http.NotFound(w, r)
		case strings.HasPrefix(clean, "/assets/"):
			serveAsset(w, r, files, strings.TrimPrefix(clean, "/"))
		case strings.HasPrefix(path.Base(clean), "."):
			http.NotFound(w, r)
		default:
			serveIndex(w, files)
		}
	})
}

func serveAsset(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	info, err := fs.Stat(files, name)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	// Dosya adları içerik özeti taşır (app-abc123.js); içerik değişince ad da değişir.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFileFS(w, r, files, name)
}

func serveIndex(w http.ResponseWriter, files fs.FS) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		http.Error(w, "Panel arayüzü derlenmemiş. `web` klasöründe `npm run build` çalıştırın veya Docker imajını kullanın.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(index)
}
