package reconcile_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kanalvo/internal/reconcile"
	"kanalvo/internal/store"
	"kanalvo/internal/testdb"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func srs(t *testing.T, status int, body string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SRS, sondaki / olmadan yönlendirme döner (bkz. docs/srs-findings.md).
		if r.URL.Path != "/api/v1/streams/" || r.URL.Query().Get("count") == "" {
			t.Errorf("beklenmeyen istek: %s", r.URL)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func twoLiveChannels(t *testing.T) (*store.Store, int64, int64) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	a := must(s.CreateChannel(ctx, tid, "a", "s1"))
	b := must(s.CreateChannel(ctx, tid, "b", "s2"))
	must(s.MarkLive(ctx, a, "ca"))
	must(s.MarkLive(ctx, b, "cb"))
	return s, a, b
}

func TestRunOnceClearsStaleLive(t *testing.T) {
	s, a, b := twoLiveChannels(t)
	ctx := context.Background()
	body := `{"code":0,"server":"vid-1","streams":[
		{"id":"vid-a","name":"1","vhost":"vid-v","app":"live","clients":4,"publish":{"active":true,"cid":"ca"}},
		{"id":"vid-b","name":"2","vhost":"vid-v","app":"live","clients":0,"publish":{"active":false,"cid":""}},
		{"id":"vid-c","name":"1","vhost":"vid-v","app":"other","publish":{"active":true,"cid":"x"}},
		{"id":"vid-d","name":"abc","vhost":"vid-v","app":"live","publish":{"active":true,"cid":"y"}}]}`
	if a != 1 || b != 2 {
		t.Fatalf("test, kanal numaralarının 1 ve 2 olmasına dayanıyor: %d, %d", a, b)
	}

	n, err := reconcile.New(s, srs(t, 200, body), 0).RunOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("1 kanal temizlenmeliydi: n=%d err=%v", n, err)
	}
	if !must(s.ChannelByID(ctx, a)).Live {
		t.Fatal("SRS'te süren yayın çevrimdışı yapılmamalı")
	}
	if must(s.ChannelByID(ctx, b)).Live {
		t.Fatal("SRS'te yayını bitmiş kanal çevrimdışı yapılmalı")
	}
	if !must(s.MarkLive(ctx, b, "yeni")) {
		t.Fatal("temizlenen kanala yeniden yayın açılabilmeli")
	}
}

func TestRunOnceLeavesChannelsAloneWhenSRSFails(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"HTTP hatası":   {500, `oops`},
		"SRS hata kodu": {200, `{"code":100}`},
		"bozuk JSON":    {200, `{"code":0,"streams":`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, a, b := twoLiveChannels(t)
			ctx := context.Background()
			if _, err := reconcile.New(s, srs(t, c.status, c.body), 0).RunOnce(ctx); err == nil {
				t.Fatal("hata bekleniyordu")
			}
			if !must(s.ChannelByID(ctx, a)).Live || !must(s.ChannelByID(ctx, b)).Live {
				t.Fatal("SRS'e ulaşılamadığında kanallara dokunulmamalı")
			}
		})
	}
}

func TestRunOnceLeavesChannelsAloneWhenSRSUnreachable(t *testing.T) {
	s, a, _ := twoLiveChannels(t)
	ctx := context.Background()
	if _, err := reconcile.New(s, "http://127.0.0.1:1", 0).RunOnce(ctx); err == nil {
		t.Fatal("hata bekleniyordu")
	}
	if !must(s.ChannelByID(ctx, a)).Live {
		t.Fatal("SRS'e ulaşılamadığında kanallara dokunulmamalı")
	}
}
