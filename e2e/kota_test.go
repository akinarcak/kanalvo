//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Tasarım §7: kota aşımında SRS yayını reddeder. Kanal kotası, yayın kabulünde yayıncının
// eşzamanlı canlı kanal üst sınırıdır; kota düşürülünce süren yayınlar kesilmez.
func TestPublishBeyondChannelQuotaIsRejectedBySRS(t *testing.T) {
	admin := newAdmin(t, "kota-yonetici")
	f := newTenant(t, admin, "kota", "320x180")
	type channel struct {
		ID        int64  `json:"id"`
		StreamKey string `json:"stream_key"`
	}
	channels := []channel{{ID: f.Channel.ID, StreamKey: f.Channel.StreamKey}}
	for _, name := range []string{"Kota B", "Kota C"} {
		var c channel
		f.Owner.call("POST", "/api/tenant/channels", map[string]any{"name": name}, &c, http.StatusCreated)
		channels = append(channels, c)
	}
	setChannelQuota := func(n int) {
		t.Helper()
		admin.call("PATCH", fmt.Sprintf("/api/admin/tenants/%d", f.ID),
			map[string]any{"quotas": map[string]int{"max_channels": n, "max_viewers": 10, "max_connections": 10}}, nil, http.StatusOK)
	}
	liveSet := func() map[int64]bool {
		var list []struct {
			ID   int64 `json:"id"`
			Live bool  `json:"live"`
		}
		f.Owner.call("GET", "/api/tenant/channels", nil, &list, http.StatusOK)
		out := map[int64]bool{}
		for _, c := range list {
			out[c.ID] = c.Live
		}
		return out
	}
	pub := func(i int, suffix string) string {
		name := fmt.Sprintf("%s-e2e-kota-%c%s", project, 'a'+i, suffix)
		publish(t, name, channels[i].StreamKey, "320x180")
		return name
	}
	setChannelQuota(2)

	// Kota içindeki iki yayın açılır.
	pubA, pubB := pub(0, ""), pub(1, "")
	waitFor(t, "kota içindeki iki kanalın yayında görünmesi", 30*time.Second, func() bool {
		live := liveSet()
		return live[channels[0].ID] && live[channels[1].ID]
	})

	// Üçüncü yayını SRS reddeder: FFmpeg bağlantısı kapanır, kanal çevrimdışı kalır.
	pubC := pub(2, "")
	waitFor(t, "kota üstü üçüncü yayının reddedilmesi", 30*time.Second, func() bool { return !containerRunning(pubC) })
	if live := liveSet(); live[channels[2].ID] || !live[channels[0].ID] || !live[channels[1].ID] {
		t.Fatalf("reddedilen kanal çevrimdışı, süren iki yayın canlı kalmalı: %v", live)
	}
	if !containerRunning(pubA) || !containerRunning(pubB) {
		t.Fatal("reddedilen yayın süren yayınları etkilememeli")
	}

	// Bir yayın kapanınca üçüncü kanal açılabilir.
	stopContainer(pubA)
	waitFor(t, "kapanan yayının çevrimdışı görünmesi", 30*time.Second, func() bool { return !liveSet()[channels[0].ID] })
	pubC = pub(2, "-2")
	waitFor(t, "yer açılınca üçüncü kanalın yayında görünmesi", 30*time.Second, func() bool { return liveSet()[channels[2].ID] })

	// Kota 1'e düşürülünce süren iki yayın sürer, yenisi reddedilir.
	setChannelQuota(1)
	pubA = pub(0, "-2")
	waitFor(t, "kota düşürüldükten sonra yeni yayının reddedilmesi", 30*time.Second, func() bool { return !containerRunning(pubA) })
	// Uygulama döngüsü (5 sn) birkaç kez geçsin; süren yayınlar kesilmemeli.
	time.Sleep(12 * time.Second)
	if live := liveSet(); !live[channels[1].ID] || !live[channels[2].ID] || live[channels[0].ID] {
		t.Fatalf("kota düşürülünce süren yayınlar sürmeli, yenisi açılmamalı: %v", live)
	}
	if !containerRunning(pubB) || !containerRunning(pubC) {
		t.Fatal("kota düşürülünce süren yayınlar kesilmemeli")
	}
}
