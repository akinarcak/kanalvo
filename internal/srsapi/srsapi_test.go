package srsapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"streamhub/internal/srsapi"
)

// clientsBody, SRS 5.0.225'in /api/v1/clients/ yanıtının biçimidir (bkz. docs/srs-findings.md).
const clientsBody = `{"code":0,"server":"vid-1","clients":[
	{"id":"kw44x0j8","vhost":"vid-v","stream":"vid-s","ip":"172.21.0.3","url":"/live/12","name":"12","type":"flash-publish","publish":true,"alive":15.39},
	{"id":"s2c6fm37","vhost":"vid-v","stream":"vid-s","ip":"172.21.0.1","url":"/live/12","name":"12","type":"flv-play","publish":false,"alive":9.78}]}`

func server(t *testing.T, handler http.HandlerFunc) *srsapi.Client {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srsapi.New(srv.URL)
}

func TestClientIDs(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/clients/" || r.URL.Query().Get("count") == "" {
			t.Errorf("beklenmeyen istek: %s %s", r.Method, r.URL)
		}
		io.WriteString(w, clientsBody)
	})
	ids, err := c.ClientIDs(context.Background())
	if err != nil || fmt.Sprint(ids) != "[kw44x0j8 s2c6fm37]" {
		t.Fatalf("kimlikler %v, hata %v", ids, err)
	}
}

func TestClientIDsEmptyList(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"code":0,"clients":[]}`) })
	ids, err := c.ClientIDs(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("boş liste geçerlidir: %v, %v", ids, err)
	}
}

func TestClientIDsErrors(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"HTTP hatası":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"SRS hata kodu": func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"code":100}`) },
		"bozuk JSON":    func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"code":0,"clients":`) },
		// Liste alanı yoksa "kimse bağlı değil" sanılmamalı; aksi halde tüm oturumlar silinirdi.
		"clients alanı yok": func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"code":0,"server":"vid-1"}`) },
		"clients null":      func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"code":0,"clients":null}`) },
	}
	for name, h := range cases {
		if _, err := server(t, h).ClientIDs(context.Background()); err == nil {
			t.Errorf("%s: hata bekleniyordu", name)
		}
	}
	if _, err := srsapi.New("http://127.0.0.1:1").ClientIDs(context.Background()); err == nil {
		t.Error("ulaşılamayan SRS için hata bekleniyordu")
	}
}

func TestKick(t *testing.T) {
	var got string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		io.WriteString(w, `{"code":0,"server":"vid-1"}`)
	})
	if err := c.Kick(context.Background(), "s2c6fm37"); err != nil {
		t.Fatal(err)
	}
	if got != "DELETE /api/v1/clients/s2c6fm37" {
		t.Fatalf("beklenmeyen istek: %s", got)
	}
}

func TestKickRejectsUnsafeIDsWithoutCallingSRS(t *testing.T) {
	c := server(t, func(http.ResponseWriter, *http.Request) { t.Error("SRS çağrılmamalıydı") })
	for _, id := range []string{"", "../streams", "a/b", "a?b=c", "a b"} {
		if err := c.Kick(context.Background(), id); err == nil {
			t.Errorf("%q için hata bekleniyordu", id)
		}
	}
}

func TestKickErrors(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"code":2049}`) })
	if err := c.Kick(context.Background(), "abc"); err == nil {
		t.Error("SRS hata kodu için hata bekleniyordu")
	}
}
