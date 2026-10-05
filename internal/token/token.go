// Package token, izleyiciyi edge'e yönlendiren kısa ömürlü imzalı değeri üretir ve doğrular.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid = errors.New("token: geçersiz")
	ErrExpired = errors.New("token: süresi dolmuş")
)

type Claims struct {
	ViewerID  int64
	ChannelID int64
	ExpiresAt time.Time
}

type Signer struct {
	key []byte
}

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

// Sign, "<izleyici>.<kanal>.<bitiş unix>.<imza>" biçiminde bir değer üretir.
func (s *Signer) Sign(c Claims) string {
	payload := fmt.Sprintf("%d.%d.%d", c.ViewerID, c.ChannelID, c.ExpiresAt.Unix())
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
	if len(parts) != 3 {
		return Claims{}, ErrInvalid
	}
	var n [3]int64
	for j, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return Claims{}, ErrInvalid
		}
		n[j] = v
	}
	c := Claims{ViewerID: n[0], ChannelID: n[1], ExpiresAt: time.Unix(n[2], 0)}
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
