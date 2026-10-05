package token

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	now    = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	signer = NewSigner([]byte(strings.Repeat("k", 32)))
)

func TestSignVerifyRoundTrip(t *testing.T) {
	cases := []Claims{
		{Kind: KindTS, ViewerID: 7, ChannelID: 42, ExpiresAt: now.Add(5 * time.Minute)},
		{Kind: KindHLS, ViewerID: 7, ChannelID: 42, Session: "0a1b2c3d4e5f6071", ExpiresAt: now.Add(5 * time.Minute)},
	}
	for _, in := range cases {
		got, err := signer.Verify(signer.Sign(in), now)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != in.Kind || got.Session != in.Session || got.ViewerID != 7 || got.ChannelID != 42 || !got.ExpiresAt.Equal(in.ExpiresAt) {
			t.Fatalf("beklenmeyen içerik: %+v", got)
		}
	}
}

func TestTokenIsURLSafe(t *testing.T) {
	tok := signer.Sign(Claims{Kind: KindHLS, ViewerID: 1, ChannelID: 2, Session: "ab12", ExpiresAt: now})
	if strings.ContainsAny(tok, "+/=?&# ") {
		t.Fatalf("adres için güvenli olmayan karakter: %q", tok)
	}
}

func TestVerifyExpiry(t *testing.T) {
	tok := signer.Sign(Claims{Kind: KindTS, ViewerID: 1, ChannelID: 2, ExpiresAt: now})
	if _, err := signer.Verify(tok, now.Add(-time.Second)); err != nil {
		t.Fatalf("süresi dolmamış imza kabul edilmeliydi: %v", err)
	}
	if _, err := signer.Verify(tok, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("tam bitiş anında ErrExpired bekleniyordu, gelen: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	exp := now.Add(time.Minute)
	claims := Claims{Kind: KindTS, ViewerID: 1, ChannelID: 2, ExpiresAt: exp}
	tok := signer.Sign(claims)
	other := NewSigner([]byte(strings.Repeat("x", 32)))
	bad := []string{
		"",
		"abc",
		"t.1.2.3",
		"t.1.2.3.",
		strings.Replace(tok, "t.1.2.", "t.1.3.", 1),
		strings.Replace(tok, "t.", "h.", 1),
		tok + "A",
		other.Sign(claims),
		signer.Sign(Claims{Kind: "x", ViewerID: 1, ChannelID: 2, ExpiresAt: exp}),
		signer.Sign(Claims{ViewerID: 1, ChannelID: 2, ExpiresAt: exp}),
		// HLS imzası oturum anahtarı taşımak zorundadır; anahtar yalnızca onaltılık olabilir.
		signer.Sign(Claims{Kind: KindHLS, ViewerID: 1, ChannelID: 2, ExpiresAt: exp}),
		signer.Sign(Claims{Kind: KindHLS, ViewerID: 1, ChannelID: 2, Session: "../x", ExpiresAt: exp}),
		signer.Sign(Claims{Kind: KindHLS, ViewerID: 1, ChannelID: 2, Session: "a.b", ExpiresAt: exp}),
		signer.Sign(Claims{Kind: KindHLS, ViewerID: 1, ChannelID: 2, Session: strings.Repeat("a", 65), ExpiresAt: exp}),
	}
	for _, b := range bad {
		if _, err := signer.Verify(b, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q için ErrInvalid bekleniyordu, gelen: %v", b, err)
		}
	}
}
