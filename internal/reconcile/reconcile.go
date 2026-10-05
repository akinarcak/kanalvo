// Package reconcile, veritabanındaki "yayında" durumunu SRS'teki gerçek yayınlarla eşitler.
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"streamhub/internal/store"
)

// maxStreams, SRS'ten tek istekte istenen yayın sayısıdır; SRS varsayılan olarak yalnızca 10 döner.
const maxStreams = 10000

type Reconciler struct {
	store     *store.Store
	srsAPIURL string
	grace     time.Duration
	client    *http.Client
}

func New(s *store.Store, srsAPIURL string, grace time.Duration) *Reconciler {
	return &Reconciler{store: s, srsAPIURL: srsAPIURL, grace: grace, client: &http.Client{Timeout: 5 * time.Second}}
}

func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if n, err := r.RunOnce(ctx); err != nil {
			log.Printf("reconcile: %v", err)
		} else if n > 0 {
			log.Printf("reconcile: %d kanal çevrimdışı yapıldı", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce, SRS'e ulaşılamazsa hiçbir kanala dokunmadan hata döner.
func (r *Reconciler) RunOnce(ctx context.Context) (int64, error) {
	active, err := r.activeChannelIDs(ctx)
	if err != nil {
		return 0, err
	}
	return r.store.ReconcileLive(ctx, active, r.grace)
}

func (r *Reconciler) activeChannelIDs(ctx context.Context) ([]int64, error) {
	// Sondaki / gerekli: SRS onsuz yönlendirme döner.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/streams/?count=%d", r.srsAPIURL, maxStreams), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SRS yayın listesi: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Code    int `json:"code"`
		Streams []struct {
			Name    string `json:"name"`
			App     string `json:"app"`
			Publish struct {
				Active bool `json:"active"`
			} `json:"publish"`
		} `json:"streams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("SRS yayın listesi çözülemedi: %w", err)
	}
	if body.Code != 0 {
		return nil, fmt.Errorf("SRS yayın listesi: kod %d", body.Code)
	}
	ids := []int64{}
	for _, s := range body.Streams {
		if s.App != "live" || !s.Publish.Active {
			continue
		}
		if id, err := strconv.ParseInt(s.Name, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
