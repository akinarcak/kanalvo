package passhash

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("doğru-at-pil-zımba")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("beklenmeyen biçim: %s", hash)
	}
	if strings.Contains(hash, "doğru") {
		t.Fatal("özet şifreyi içermemeli")
	}
	if !Verify(hash, "doğru-at-pil-zımba") {
		t.Fatal("doğru şifre kabul edilmeli")
	}
	for _, wrong := range []string{"", "dogru-at-pil-zimba", "doğru-at-pil-zımba "} {
		if Verify(hash, wrong) {
			t.Errorf("%q kabul edilmemeli", wrong)
		}
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := Hash("aynı-şifre-123")
	b, _ := Hash("aynı-şifre-123")
	if a == b {
		t.Fatal("aynı şifrenin iki özeti farklı olmalı")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	good, _ := Hash("x")
	parts := strings.Split(good, "$")
	bad := []string{
		"",
		"düz-metin",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=19456,t=2,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=0,t=2,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=99999999999,t=2,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$" + parts[5],
		"$argon2id$v=19$m=19456,t=2,p=1$" + parts[4] + "$",
		strings.Join(parts[:5], "$"),
	}
	for _, h := range bad {
		if Verify(h, "x") {
			t.Errorf("bozuk özet kabul edildi: %q", h)
		}
	}
}

func TestDummyVerifyNeverSucceeds(t *testing.T) {
	if VerifyDummy("herhangi") {
		t.Fatal("sahte doğrulama her zaman başarısız olmalı")
	}
}
