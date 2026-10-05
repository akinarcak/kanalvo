// Package token, izleyiciyi edge'e yönlendiren imzalı değeri üretir ve doğrular.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid = errors.New("token: geçersiz")
	ErrExpired = errors.New("token: süresi dolmuş")
)

// Kind, imzanın hangi izleme yolunda geçerli olduğunu belirtir. Uzun ömürlü HLS imzasının
// yalnızca başlangıçta doğrulanan .ts yolunda kullanılmasını engeller.
type Kind string

const (
	KindTS  Kind = "t"
	KindHLS Kind = "h"
)

var sessionPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

type Claims struct {
	Kind      Kind
	ViewerID  int64
	ChannelID int64
	ExpiresAt time.Time
	// Session, HLS imzasının oturum anahtarıdır: her yönlendirmede rastgele üretilir ve geçit
	// bağlantı limitini bununla sayar. .ts imzasında bulunmaz (oturumu SRS bağlantısı belirler).
	Session string
}

type Signer struct {
	key []byte
}

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

// Sign, "<tür>.<izleyici>.<kanal>.<bitiş unix>[.<oturum>].<imza>" biçiminde bir değer üretir.
func (s *Signer) Sign(c Claims) string {
	payload := fmt.Sprintf("%s.%d.%d.%d", c.Kind, c.ViewerID, c.ChannelID, c.ExpiresAt.Unix())
	if c.Kind == KindHLS {
		payload += "." + c.Session
	}
	return payload + "." + s.mac(payload)
}

func (s *Signer) Verify(tok string, now time.Time) (Claims, error) {
	i := strings.LastIndexByte(tok, '.')
	if i < 0 {
		return Claims{}, ErrInvalid
	}
	payload, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(s.mac(payload))) {
		return Claims{}, ErrInvalid
	}
	parts := strings.Split(payload, ".")
	var c Claims
	switch {
	case len(parts) == 4 && Kind(parts[0]) == KindTS:
		c.Kind = KindTS
	case len(parts) == 5 && Kind(parts[0]) == KindHLS && sessionPattern.MatchString(parts[4]):
		c.Kind, c.Session = KindHLS, parts[4]
	default:
		return Claims{}, ErrInvalid
	}
	var n [3]int64
	for j, p := range parts[1:4] {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return Claims{}, ErrInvalid
		}
		n[j] = v
	}
	c.ViewerID, c.ChannelID, c.ExpiresAt = n[0], n[1], time.Unix(n[2], 0)
	if !now.Before(c.ExpiresAt) {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func (s *Signer) mac(payload string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
