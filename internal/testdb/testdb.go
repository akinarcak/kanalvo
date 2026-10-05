// Package testdb, testlere göçleri uygulanmış ve boşaltılmış bir veritabanı verir.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"streamhub/internal/store"
)

// Exec, testlerin veritabanı durumunu doğrudan hazırlaması içindir (ör. bir kaydı geçmişe çekmek).
func Exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("testdb.Exec: %v", err)
	}
}

// Count, tek bir sayı döndüren sorguyu çalıştırır.
func Count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("testdb.Count: %v", err)
	}
	return n
}

func url() string {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return defaultURL
}

const defaultURL = "postgres://streamhub:streamhub@localhost:5432/streamhub_test?sslmode=disable"

func New(t *testing.T) *store.Store {
	t.Helper()
	url := url()
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
	if _, err := conn.Exec(ctx, `TRUNCATE panel_sessions, admins, sessions, viewers, channels, categories, tenants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("tablolar boşaltılamadı: %v", err)
	}
	return s
}
