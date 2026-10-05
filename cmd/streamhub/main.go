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
	"syscall"
	"time"

	"streamhub/internal/config"
	"streamhub/internal/hlsgw"
	"streamhub/internal/hooks"
	"streamhub/internal/play"
	"streamhub/internal/reconcile"
	"streamhub/internal/store"
	"streamhub/internal/token"
)

const (
	reconcileInterval = 15 * time.Second
	reconcileGrace    = 10 * time.Second
)

func main() {
	commands := map[string]func(context.Context) error{
		"serve":    serve,
		"seed-dev": seedDev,
	}
	if len(os.Args) != 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "kullanım: streamhub serve | seed-dev")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := commands[os.Args[1]](ctx); err != nil {
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
	st, err := openStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	signer := token.NewSigner(cfg.TokenKey)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	hooks.New(st, signer, time.Now).Register(mux, cfg.HookSecret)
	play.New(st, signer, play.Options{
		TSBaseURL:   cfg.EdgeTSBaseURL,
		TSTokenTTL:  cfg.TokenTTL,
		HLSBaseURL:  cfg.EdgeHLSBaseURL,
		HLSTokenTTL: cfg.HLSTokenTTL,
	}, time.Now).Register(mux)
	hlsgw.New(st, signer, srsHLS, time.Now).Register(mux)

	go reconcile.New(st, cfg.SRSAPIURL, reconcileGrace).Run(ctx, reconcileInterval)

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Printf("streamhub %s adresinde dinliyor", cfg.HTTPAddr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
		"channel_id":    channelID,
		"stream_secret": secret,
		"username":      username,
		"password":      password,
	})
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
