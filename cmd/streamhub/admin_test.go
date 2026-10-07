package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"streamhub/internal/passhash"
	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

// Şifresini unutan yönetici komutla yeni şifre alır: eski şifre ve açık panel oturumları geçersiz olur.
func TestNewAdminPasswordReplacesThePasswordAndEndsSessions(t *testing.T) {
	st, ctx := testdb.New(t), context.Background()
	oldHash, err := passhash.Hash("eski-sifre")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAdmin(ctx, "Yonetici@example.com", oldHash)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateAdmin(ctx, "diger@example.com", oldHash)
	if err != nil {
		t.Fatal(err)
	}
	for hash, admin := range map[string]int64{"oturum-1": id, "oturum-2": other} {
		if err := st.CreatePanelSession(ctx, []byte(hash), "admin", admin, time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	password, err := newAdminPassword(ctx, st, "yonetici@EXAMPLE.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 16 {
		t.Fatalf("yeni şifre en az 16 karakter olmalı: %d", len(password))
	}
	admin, err := st.AdminByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !passhash.Verify(admin.PasswordHash, password) || passhash.Verify(admin.PasswordHash, "eski-sifre") {
		t.Fatal("yalnızca yeni şifre geçerli olmalı")
	}
	if _, err := st.PanelSessionByHash(ctx, []byte("oturum-1")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("şifresi sıfırlanan yöneticinin oturumu kapanmalıydı: %v", err)
	}
	if _, err := st.PanelSessionByHash(ctx, []byte("oturum-2")); err != nil {
		t.Fatalf("başka yöneticinin oturumu kapanmamalıydı: %v", err)
	}
	untouched, err := st.AdminByID(ctx, other)
	if err != nil || untouched.PasswordHash != oldHash {
		t.Fatalf("başka yöneticinin şifresi değişmemeliydi: %v", err)
	}

	if _, err := newAdminPassword(ctx, st, "yok@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("kayıtlı olmayan e-posta için ErrNotFound bekleniyordu: %v", err)
	}
}
