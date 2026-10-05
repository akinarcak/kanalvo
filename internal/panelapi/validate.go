package panelapi

import (
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// cleanName, bir görünen adı kırpar; boşsa, çok uzunsa veya denetim karakteri içeriyorsa geçersizdir.
func cleanName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 100 {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

func cleanEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || !strings.Contains(s[strings.LastIndex(s, "@")+1:], ".") {
		return "", false
	}
	return s, true
}

// validPassword, panel şifresi uygunsa boş, değilse kullanıcıya gösterilecek açıklamayı döner.
func validPassword(s string) string {
	switch {
	case utf8.RuneCountInString(s) < 10:
		return "Şifre en az 10 karakter olmalı."
	case len(s) > 200:
		return "Şifre çok uzun."
	}
	return ""
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

// reservedUsernames: kısa yayın adresi /<kullanıcı>/<şifre>/<kanal> biçimindedir; bu adlar
// başka uçların ilk yol parçasıyla çakışır.
var reservedUsernames = map[string]bool{"hls": true, "live": true, "api": true, "panel": true, "hooks": true, "healthz": true, "assets": true, "edge": true}

func validUsername(s string) bool {
	if !usernamePattern.MatchString(s) || reservedUsernames[strings.ToLower(s)] || strings.HasSuffix(strings.ToLower(s), ".php") {
		return false
	}
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

// validLogoURL: boş veya http(s) adresi. Oynatıcılar ve panel bu adresi görüntü olarak yükler.
func validLogoURL(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 500 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
