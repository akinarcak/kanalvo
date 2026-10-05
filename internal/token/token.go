// Package token, izleyiciyi edge'e yönlendiren imzalı değeri üretir ve doğrular.
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

// Kind, imzanın hangi izleme yolunda geçerli olduğunu belirtir. Uzun ömürlü HLS imzasının
// yalnızca başlangıçta doğrulanan .ts yolunda kullanılmasını engeller.
type Kind string

const (
	KindTS  Kind = "t"
	KindHLS Kind = "h"
)

type Claims struct {
	Kind      Kind
	ViewerID  int64
	ChannelID int64
	ExpiresAt time.Time
}

type Signer struct {
	key []byte
}

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

// Sign, "<tür>.<izleyici>.<kanal>.<bitiş unix>.<imza>" biçiminde bir değer üretir.
func (s *Signer) Sign(c Claims) string {
	payload := fmt.Sprintf("%s.%d.%d.%d", c.Kind, c.ViewerID, c.ChannelID, c.ExpiresAt.Unix())
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
	if len(parts) != 4 {
		return Claims{}, ErrInvalid
	}
	kind := Kind(parts[0])
	if kind != KindTS && kind != KindHLS {
		return Claims{}, ErrInvalid
	}
	var n [3]int64
	for j, p := range parts[1:] {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return Claims{}, ErrInvalid
		}
		n[j] = v
	}
	c := Claims{Kind: kind, ViewerID: n[0], ChannelID: n[1], ExpiresAt: time.Unix(n[2], 0)}
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
