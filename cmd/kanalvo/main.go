package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kanalvo/internal/auth"
	"kanalvo/internal/balancer"
	"kanalvo/internal/config"
	"kanalvo/internal/enroll"
	"kanalvo/internal/hlsgw"
	"kanalvo/internal/hooks"
	"kanalvo/internal/httpserve"
	"kanalvo/internal/panelapi"
	"kanalvo/internal/panelui"
	"kanalvo/internal/passhash"
	"kanalvo/internal/play"
	"kanalvo/internal/ratelimit"
	"kanalvo/internal/reconcile"
	"kanalvo/internal/session"
	"kanalvo/internal/srsapi"
	"kanalvo/internal/store"
	"kanalvo/internal/token"
	"kanalvo/internal/xtream"
)

const (
	reconcileInterval  = 15 * time.Second
	reconcileGrace     = 10 * time.Second
	srsResponseTimeout = 10 * time.Second
	shutdownGrace      = 10 * time.Second
	// hlsSessionIdle: bu süre boyunca istek gelmeyen HLS oturumu bağlantı limitinden düşer.
	hlsSessionIdle  = 30 * time.Second
	enforceInterval = 5 * time.Second
	// edgeHealthWindow: bu süre içinde yönetim API'si yanıt vermiş edge sağlıklı sayılır. Her edge
	// enforceInterval aralığıyla sorulduğu için iki ardışık sorgunun kaçması edge'i yönlendirmeden çıkarır.
	edgeHealthWindow = 3 * enforceInterval
	// edgeListTTL: edge listesinin bellekte tutulduğu süre.
	edgeListTTL = 2 * time.Second
)

func main() {
	commands := map[string]func(context.Context, []string) error{
		"serve":                func(ctx context.Context, _ []string) error { return serve(ctx) },
		"seed-dev":             func(ctx context.Context, _ []string) error { return seedDev(ctx) },
		"set-tenant-status":    setTenantStatus,
		"create-admin":         createAdmin,
		"reset-admin-password": resetAdminPassword,
	}
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "kullanım: kanalvo serve | seed-dev | create-admin <e-posta> | reset-admin-password <e-posta> | set-tenant-status <yayıncı no> <active|suspended>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := commands[os.Args[1]](ctx, os.Args[2:]); err != nil {
		log.Fatalf("kanalvo %s: %v", os.Args[1], err)
	}
}

func serve(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	srsHLS, err := url.Parse(cfg.SRSHLSURL)
	if err != nil {
		return err
	}
	publicBase, err := url.Parse(cfg.PublicBaseURL)
	if err != nil {
		return err
	}
	st, err := openStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	signer := token.NewSigner(cfg.TokenKey)

	// Kontrol sunucusundaki dağıtım "yerel" edge'dir; adresleri ayarlardan gelir.
	localEdge, err := st.SyncLocalEdge(ctx, cfg.EdgeTSBaseURL, cfg.EdgeHLSBaseURL)
	if err != nil {
		return fmt.Errorf("yerel edge kaydı: %w", err)
	}
	edges := balancer.New(st, edgeHealthWindow, edgeListTTL)
	localSRS := srsapi.New(cfg.SRSTSAPIURL)
	srsFor := func(e store.Edge) session.SRS {
		if e.Builtin {
			return localSRS
		}
		// Uzak edge'in SRS yönetim API'si, nginx'inde anahtarla korunan /_srs yolundadır.
		return srsapi.NewWithHeader(e.ControlURL+"/_srs", balancer.KeyHeader, e.Key)
	}
	sessions := session.New(st, srsFor, srsapi.New(cfg.SRSAPIURL), hlsSessionIdle, cfg.HLSTokenTTL)

	// Kontrol sunucusundaki SRS'lerin yetki sorguları ayrı, dışarıya açılmayan bir adreste dinlenir.
	hook := hooks.New(st, signer, sessions, edges, time.Now)
	internal := http.NewServeMux()
	hook.Register(internal, cfg.HookSecret, localEdge)
	mux := publicMux(st, signer, sessions, edges, hook, localEdge, cfg, publicBase, srsHLS)

	// Panel ayrı bir adreste dinlenir ve kendi hatalı giriş sayacını kullanır.
	panel := http.NewServeMux()
	panelapi.New(st, sessions, ratelimit.New(cfg.LoginMaxFailures, cfg.LoginFailureWindow, time.Now), panelapi.Config{
		SessionTTL:        cfg.PanelSessionTTL,
		SecureCookie:      !cfg.PanelInsecureCookie,
		TrustProxyHeaders: cfg.PanelTrustProxyHeaders,
		IngestURL:         cfg.IngestBaseURL,
		PublicBaseURL:     cfg.PublicBaseURL,
		Idle:              hlsSessionIdle,
		EdgeHealthWindow:  edgeHealthWindow,
	}).Register(panel)
	panel.Handle("/", panelui.New(panelui.Embedded()))

	go sessions.Run(ctx, enforceInterval)
	go expirePanelSessions(ctx, st)

	go reconcile.New(st, cfg.SRSAPIURL, reconcileGrace).Run(ctx, reconcileInterval)

	log.Printf("kanalvo dinliyor: izleyiciler %s, SRS sorguları %s, panel %s", cfg.HTTPAddr, cfg.HooksAddr, cfg.PanelAddr)
	return httpserve.Run(ctx, shutdownGrace,
		httpserve.New(cfg.HTTPAddr, mux), httpserve.New(cfg.HooksAddr, internal), httpserve.New(cfg.PanelAddr, panel))
}

// publicMux, izleyicilere açık tüm uçları ve uzak edge'lerin kullandığı /edge uçlarını tek
// yönlendiricide toplar.
func publicMux(st *store.Store, signer *token.Signer, sessions *session.Manager, edges *balancer.Balancer, hook *hooks.Handler,
	localEdge int64, cfg config.Config, publicBase, srsHLS *url.URL) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })

	limiter := ratelimit.New(cfg.LoginMaxFailures, cfg.LoginFailureWindow, time.Now)
	authn := auth.New(st, limiter, cfg.TrustProxyHeaders)
	xtream.New(st, authn, sessions, publicBase, time.Now).Register(mux)
	play.New(st, authn, signer, edges, play.Options{TSTokenTTL: cfg.TokenTTL, HLSTokenTTL: cfg.HLSTokenTTL}, time.Now).Register(mux)
	gateway := hlsgw.New(st, signer, sessions, edges, localEdge, srsHLS, srsResponseTimeout, cfg.TrustProxyHeaders, time.Now)
	gateway.Register(mux)
	gateway.RegisterEdge(mux)
	hook.RegisterEdge(mux)
	enroll.New(st, enroll.Config{
		PublicBaseURL: cfg.PublicBaseURL, IngestURL: cfg.IngestBaseURL, TrustProxyHeaders: cfg.TrustProxyHeaders,
	}, func() string { return randomHex(24) }).Register(mux)
	return mux
}

// seedDev, yerel deneme için bir yayıncı, kanal ve izleyici oluşturur ve bilgilerini JSON olarak yazar.
func seedDev(ctx context.Context) error {
	st, err := openStore(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer st.Close()

	tenantID, err := st.CreateTenant(ctx, "dev")
	if err != nil {
		return err
	}
	secret, username, password := randomHex(16), "dev-"+randomHex(4), randomHex(8)
	channelID, err := st.CreateChannel(ctx, tenantID, "Dev Kanal", secret)
	if err != nil {
		return err
	}
	if _, err := st.CreateViewer(ctx, tenantID, username, password, 1); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"tenant_id":     tenantID,
		"channel_id":    channelID,
		"stream_secret": secret,
		"username":      username,
		"password":      password,
	})
}

// expirePanelSessions, süresi dolan panel oturumlarını saatte bir siler.
func expirePanelSessions(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if _, err := st.DeleteExpiredPanelSessions(ctx); err != nil && ctx.Err() == nil {
			log.Printf("panel oturumları temizlenemedi: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// createAdmin, bir platform yöneticisi oluşturur ve rastgele şifresini bir kez yazar.
func createAdmin(ctx context.Context, args []string) error {
	if len(args) != 1 || !strings.Contains(args[0], "@") {
		return errors.New("kullanım: create-admin <e-posta>")
	}
	st, err := openStore(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer st.Close()
	password := randomHex(10)
	hash, err := passhash.Hash(password)
	if err != nil {
		return err
	}
	if _, err := st.CreateAdmin(ctx, args[0], hash); err != nil {
		return fmt.Errorf("yönetici oluşturulamadı (e-posta kullanılıyor olabilir): %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"email": args[0], "password": password})
}

// resetAdminPassword, şifresini unutan yöneticiye yeni bir rastgele şifre verir ve bir kez yazar.
func resetAdminPassword(ctx context.Context, args []string) error {
	if len(args) != 1 || !strings.Contains(args[0], "@") {
		return errors.New("kullanım: reset-admin-password <e-posta>")
	}
	st, err := openStore(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer st.Close()
	password, err := newAdminPassword(ctx, st, args[0])
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s adresiyle kayıtlı bir yönetici yok", args[0])
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"email": args[0], "password": password})
}

// newAdminPassword, yöneticinin şifresini rastgele bir şifreyle değiştirir ve açık panel
// oturumlarını kapatır. Böyle bir yönetici yoksa store.ErrNotFound döner.
func newAdminPassword(ctx context.Context, st *store.Store, email string) (string, error) {
	admin, err := st.AdminByEmail(ctx, email)
	if err != nil {
		return "", err
	}
	password := randomHex(10)
	hash, err := passhash.Hash(password)
	if err != nil {
		return "", err
	}
	if err := st.ResetAdminPassword(ctx, admin.ID, hash); err != nil {
		return "", err
	}
	return password, nil
}

// setTenantStatus, bir yayıncıyı askıya alır veya yeniden etkinleştirir. Askıya alınan yayıncının
// süren yayınları ve izleyicilerinin bağlantıları birkaç saniye içinde kesilir.
func setTenantStatus(ctx context.Context, args []string) error {
	if len(args) != 2 || (args[1] != "active" && args[1] != "suspended") {
		return errors.New("kullanım: set-tenant-status <yayıncı no> <active|suspended>")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("geçersiz yayıncı numarası %q", args[0])
	}
	st, err := openStore(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.TenantQuotas(ctx, id); err != nil {
		return fmt.Errorf("yayıncı %d: %w", id, err)
	}
	// Panelle aynı yol: askıya alınan yayıncının panel oturumları da kapanır.
	return st.UpdateTenant(ctx, id, store.TenantUpdate{Status: &args[1]})
}

func openStore(ctx context.Context, url string) (*store.Store, error) {
	if url == "" {
		return nil, errors.New("DATABASE_URL boş olamaz")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
