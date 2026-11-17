package hooks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SRS çöküp yeniden başlayınca on_unpublish gelmez; kanal veritabanında eski bağlantıyla canlı
// kalır. Yeniden bağlanan yayıncı, eski bağlantı origin'in listesinde yoksa hemen kabul edilir.
func TestPublishReplacesStaleLiveAfterSRSRestart(t *testing.T) {
	f := setup(t)
	if code := f.publish("eski", "?secret=sek"); code != http.StatusOK {
		t.Fatalf("ilk yayın: durum %d", code)
	}
	// Yeni bağlantı ve bir edge'in çekme bağlantısı listede; eski yayıncı yok.
	f.originConnected("yeni", "edge-cekme")
	if code := f.publish("yeni", "?secret=sek"); code != http.StatusOK {
		t.Fatalf("bayat işaretli kanala yeniden yayın kabul edilmeli: durum %d", code)
	}
	// Eski bağlantının geç gelen bildirimi yeni yayına dokunmaz; yeni yayıncınınki kanalı kapatır.
	f.unpublishFrom(f.channel, "eski")
	if !f.live() {
		t.Fatal("eski bağlantının bildirimi yeni yayını çevrimdışı yapmamalı")
	}
	f.unpublishFrom(f.channel, "yeni")
	if f.live() {
		t.Fatal("kanalın yayıncısı yeni bağlantı olmalı")
	}
}

// Gerçekten süren bir yayına ikinci bağlantı, SRS'e sorulsa da reddedilir (tasarım §7).
func TestSecondPublisherRejectedWhenTheFirstIsStillOnSRS(t *testing.T) {
	f := setup(t)
	if code := f.publish("a", "?secret=sek"); code != http.StatusOK {
		t.Fatalf("ilk yayın: durum %d", code)
	}
	f.originConnected("a", "b")
	if code := f.publish("b", "?secret=sek"); code != http.StatusForbidden {
		t.Fatalf("ikinci yayın reddedilmeli: durum %d", code)
	}
	if f.originCalls() != 1 {
		t.Fatalf("origin'e bir kez sorulmalı, sorulan: %d", f.originCalls())
	}
	f.unpublishFrom(f.channel, "a")
	if f.live() {
		t.Fatal("süren yayının yayıncısı değişmemeli")
	}
}

// SRS'e ulaşılamazsa bugünkü davranış: yayın reddedilir, hiçbir kanala dokunulmaz.
func TestPublishRejectedAndNothingClearedWhenSRSIsUnreachable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := must(f.store.CreateChannel(ctx, f.tenant, "b", "sek-b"))
	c := must(f.store.CreateChannel(ctx, f.tenant, "c", "sek-c"))
	f.setChannelQuota(2)
	if f.publishTo(f.channel, "pa", "sek") != http.StatusOK || f.publishTo(b, "pb", "sek-b") != http.StatusOK {
		t.Fatal("kota içindeki iki yayın kabul edilmeli")
	}
	// fixture'ın origin'i varsayılan olarak ulaşılamaz.
	if code := f.publish("pa2", "?secret=sek"); code != http.StatusForbidden {
		t.Fatalf("aynı kanala yayın: durum %d", code)
	}
	if code := f.publishTo(c, "pc", "sek-c"); code != http.StatusForbidden {
		t.Fatalf("kota üstü yayın: durum %d", code)
	}
	if f.originCalls() != 2 {
		t.Fatalf("her ret öncesi origin'e sorulmalı, sorulan: %d", f.originCalls())
	}
	f.unpublishFrom(f.channel, "pa")
	f.unpublishFrom(b, "pb")
	if f.live() || f.channelLive(b) || f.channelLive(c) {
		t.Fatal("kanalların yayıncıları değişmemeli")
	}
}

// Kota bayat işaretli kanallarla doluysa yeni yayın kabul edilir; gerçekten süren yayınlarla
// doluysa reddedilir. Yalnızca yayını origin'de olmayan kanallar çevrimdışı yapılır.
func TestChannelQuotaIgnoresStaleLiveChannels(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := must(f.store.CreateChannel(ctx, f.tenant, "b", "sek-b"))
	c := must(f.store.CreateChannel(ctx, f.tenant, "c", "sek-c"))
	f.setChannelQuota(2)
	if f.publishTo(f.channel, "pa", "sek") != http.StatusOK || f.publishTo(b, "pb", "sek-b") != http.StatusOK {
		t.Fatal("kota içindeki iki yayın kabul edilmeli")
	}

	// İki yayın da sürüyor: kota gerçekten dolu.
	f.originConnected("pa", "pb", "pc")
	if code := f.publishTo(c, "pc", "sek-c"); code != http.StatusForbidden {
		t.Fatalf("süren yayınlarla dolu kotada yeni yayın reddedilmeli: durum %d", code)
	}
	if !f.live() || !f.channelLive(b) || f.channelLive(c) {
		t.Fatal("süren yayınlara dokunulmamalı")
	}

	// b'nin yayını SRS'te yok (bağlantısı listede değil); a sürüyor.
	f.originConnected("pa", "pc2")
	if code := f.publishTo(c, "pc2", "sek-c"); code != http.StatusOK {
		t.Fatalf("bayat kanalla dolu kotada yeni yayın kabul edilmeli: durum %d", code)
	}
	if !f.live() || f.channelLive(b) || !f.channelLive(c) {
		t.Fatal("yalnızca bayat kanal çevrimdışı olmalı")
	}
}

// SRS yeniden başlayınca yayıncının bütün kanalları bayat kalır; yeniden bağlanan yayıncılar sırayla
// kabul edilir ve kota yeniden doğru sayılır.
func TestAllChannelsReconnectAfterSRSRestart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := must(f.store.CreateChannel(ctx, f.tenant, "b", "sek-b"))
	c := must(f.store.CreateChannel(ctx, f.tenant, "c", "sek-c"))
	f.setChannelQuota(2)
	if f.publishTo(f.channel, "pa", "sek") != http.StatusOK || f.publishTo(b, "pb", "sek-b") != http.StatusOK {
		t.Fatal("kota içindeki iki yayın kabul edilmeli")
	}
	f.originConnected("pa2", "pb2", "pc")
	if code := f.publishTo(f.channel, "pa2", "sek"); code != http.StatusOK {
		t.Fatalf("a yeniden kabul edilmeli: durum %d", code)
	}
	if code := f.publishTo(b, "pb2", "sek-b"); code != http.StatusOK {
		t.Fatalf("b yeniden kabul edilmeli: durum %d", code)
	}
	// İki yeni yayın gerçekten sürüyor; kota yine geçerli.
	if code := f.publishTo(c, "pc", "sek-c"); code != http.StatusForbidden {
		t.Fatalf("kota üstü yayın reddedilmeli: durum %d", code)
	}
	if !f.live() || !f.channelLive(b) || f.channelLive(c) {
		t.Fatal("yeniden açılan iki yayın canlı, üçüncü çevrimdışı olmalı")
	}
}

// SRS yeniden başlayınca yayıncıların hepsi aynı anda yeniden bağlanır. Bir sorgu öbür kanalın
// bayat işaretini de temizlemiş olabilir; o kanalın sorgusu da kabul edilmelidir.
func TestConcurrentReconnectsAfterSRSRestartAreAllAccepted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const n = 4
	f.setChannelQuota(n)
	channels := []int64{f.channel}
	secrets := []string{"sek"}
	for i := 1; i < n; i++ {
		channels = append(channels, must(f.store.CreateChannel(ctx, f.tenant, fmt.Sprint("k", i), fmt.Sprint("sek-", i))))
		secrets = append(secrets, fmt.Sprint("sek-", i))
	}
	for round := range 20 {
		var connected []string
		for i, ch := range channels {
			// İlk turda yayınlar açılır; sonraki turlarda önceki turun yayınları bayat kalır.
			if round == 0 && f.publishTo(ch, fmt.Sprint("eski-", i), secrets[i]) != http.StatusOK {
				t.Fatal("ilk yayın kabul edilmeli")
			}
			connected = append(connected, fmt.Sprint("yeni-", round, "-", i))
		}
		f.originConnected(connected...)
		codes := make(chan int, n)
		start := make(chan struct{})
		for i, ch := range channels {
			go func() {
				<-start
				req := httptest.NewRequest(http.MethodPost, "/hooks/srs/"+hookSecret+"/publish",
					strings.NewReader(event(connected[i], "live", fmt.Sprint(ch), "?secret="+secrets[i])))
				rec := httptest.NewRecorder()
				f.mux.ServeHTTP(rec, req)
				codes <- rec.Code
			}()
		}
		close(start)
		for range n {
			if code := <-codes; code != http.StatusOK {
				t.Fatalf("tur %d: yeniden bağlanan yayın reddedildi: durum %d", round, code)
			}
		}
	}
}
