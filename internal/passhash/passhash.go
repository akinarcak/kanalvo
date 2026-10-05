// Package passhash, panel şifrelerini Argon2id ile özetler ve doğrular.
package passhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// OWASP'ın Argon2id için önerdiği en düşük ayar: 19 MiB bellek, 2 geçiş, 1 iş parçacığı.
const (
	memoryKiB = 19456
	passes    = 2
	threads   = 1
	saltLen   = 16
	keyLen    = 32
)

// Doğrulamada kabul edilen üst sınırlar; bozuk veya kötü niyetli bir özet sunucuyu yormasın diye.
const (
	maxMemoryKiB = 262144
	maxPasses    = 10
	maxThreads   = 16
)

var b64 = base64.RawStdEncoding

// Hash, "$argon2id$v=19$m=…,t=…,p=…$<tuz>$<özet>" biçiminde bir değer üretir.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, passes, memoryKiB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, passes, threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify, şifrenin özetle eşleşip eşleşmediğini söyler. Bozuk özet eşleşmez.
func Verify(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	var memory, time uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &par); err != nil {
		return false
	}
	if memory < 8 || memory > maxMemoryKiB || time < 1 || time > maxPasses || par < 1 || par > maxThreads {
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummy = func() string {
	h, err := Hash("var-olmayan-hesap")
	if err != nil {
		panic(err)
	}
	return h
}()

// VerifyDummy, var olmayan bir hesap için gerçek bir doğrulama kadar zaman harcar ve her zaman
// false döner; böylece yanıt süresi bir e-postanın kayıtlı olup olmadığını ele vermez.
func VerifyDummy(password string) bool {
	Verify(dummy, password)
	return false
}
