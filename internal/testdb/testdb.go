// Package testdb, testlere göçleri uygulanmış ve boşaltılmış bir veritabanı verir.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"streamhub/internal/store"
)

const defaultURL = "postgres://streamhub:streamhub@localhost:5432/streamhub_test?sslmode=disable"

func New(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = defaultURL
	}
	ctx := context.Background()

	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("test veritabanına bağlanılamadı (docker compose up -d postgres çalışıyor mu?): %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("göçler uygulanamadı: %v", err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `TRUNCATE viewers, channels, tenants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("tablolar boşaltılamadı: %v", err)
	}
	return s
}
