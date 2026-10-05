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
	"syscall"
	"time"

	"streamhub/internal/auth"
	"streamhub/internal/config"
	"streamhub/internal/hlsgw"
	"streamhub/internal/hooks"
	"streamhub/internal/httpserve"
	"streamhub/internal/play"
	"streamhub/internal/ratelimit"
	"streamhub/internal/reconcile"
	"streamhub/internal/session"
	"streamhub/internal/srsapi"
	"streamhub/internal/store"
	"streamhub/internal/token"
	"streamhub/internal/xtream"
)

const (
	reconcileInterval  = 15 * time.Second
	reconcileGrace     = 10 * time.Second
	srsResponseTimeout = 10 * time.Second
	shutdownGrace      = 10 * time.Second
	// hlsSessionIdle: bu süre boyunca istek gelmeyen HLS oturumu bağlantı limitinden düşer.
	hlsSessionIdle  = 30 * time.Second
	enforceInterval = 5 * time.Second
)

func main() {
	commands := map[string]func(context.Context, []string) error{
		"serve":             func(ctx context.Context, _ []string) error { return serve(ctx) },
		"seed-dev":          func(ctx context.Context, _ []string) error { return seedDev(ctx) },
		"set-tenant-status": setTenantStatus,
	}
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "kullanım: streamhub serve | seed-dev | set-tenant-status <yayıncı no> <active|suspended>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := commands[os.Args[1]](ctx, os.Args[2:]); err != nil {
		log.Fatalf("streamhub %s: %v", os.Args[1], err)
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

	// SRS yetki sorguları ayrı, dışarıya açılmayan bir adreste dinlenir.
	sessions := session.New(st, srsapi.New(cfg.SRSTSAPIURL), srsapi.New(cfg.SRSAPIURL), hlsSessionIdle, cfg.HLSTokenTTL)
	internal := http.NewServeMux()
	hooks.New(st, signer, sessions, time.Now).Register(internal, cfg.HookSecret)
	mux := publicMux(st, signer, sessions, cfg, publicBase, srsHLS)

	go sessions.Run(ctx, enforceInterval)

	go reconcile.New(st, cfg.SRSAPIURL, reconcileGrace).Run(ctx, reconcileInterval)

	log.Printf("streamhub dinliyor: izleyiciler %s, SRS sorguları %s", cfg.HTTPAddr, cfg.HooksAddr)
	return httpserve.Run(ctx, shutdownGrace, httpserve.New(cfg.HTTPAddr, mux), httpserve.New(cfg.HooksAddr, internal))
}

// publicMux, izleyicilere açık tüm uçları tek yönlendiricide toplar.
func publicMux(st *store.Store, signer *token.Signer, sessions *session.Manager, cfg config.Config, publicBase, srsHLS *url.URL) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })

	limiter := ratelimit.New(cfg.LoginMaxFailures, cfg.LoginFailureWindow, time.Now)
	authn := auth.New(st, limiter, cfg.TrustProxyHeaders)
	xtream.New(st, authn, sessions, publicBase, time.Now).Register(mux)
	play.New(st, authn, signer, play.Options{
		TSBaseURL:   cfg.EdgeTSBaseURL,
		TSTokenTTL:  cfg.TokenTTL,
		HLSBaseURL:  cfg.EdgeHLSBaseURL,
		HLSTokenTTL: cfg.HLSTokenTTL,
	}, time.Now).Register(mux)
	hlsgw.New(st, signer, sessions, srsHLS, srsResponseTimeout, cfg.TrustProxyHeaders, time.Now).Register(mux)
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
	return st.SetTenantStatus(ctx, id, args[1])
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
