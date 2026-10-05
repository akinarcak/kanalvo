// Package session, izleyici oturumlarını yönetir: bağlantı limitini uygular, yerinden edilen
// veya izleme hakkını yitiren bağlantıları SRS'te keser ve kayıtları SRS'le eşitler.
package session

import (
	"context"
	"errors"
	"log"
	"time"

	"streamhub/internal/srsapi"
	"streamhub/internal/store"
)

// kickTimeout, bir oturum açılırken yerinden edilen bağlantıyı kesmek için beklenen en uzun süredir.
// Kesme başarısız olursa Enforce döngüsü yeniden dener.
const kickTimeout = 2 * time.Second

// reconcileGrace, yeni açılmış bir .ts oturumunun SRS listesinde görünmesi için tanınan süredir.
const reconcileGrace = 10 * time.Second

// passTimeout, tek bir uygulama geçişinin en uzun süresidir.
const passTimeout = 20 * time.Second

// SRS, bir SRS'in yönetim API'sinin kullanılan kısmıdır (bkz. srsapi.Client).
type SRS interface {
	ClientIDs(ctx context.Context) ([]string, error)
	Kick(ctx context.Context, id string) error
}

type Manager struct {
	store     *store.Store
	ts        SRS // izleyicilerin bağlandığı .ts dağıtıcısı
	origin    SRS // yayıncıların bağlandığı origin
	idle      time.Duration
	retention time.Duration
}

// New: idle, istek gelmeyen bir HLS oturumunun bağlantı limitinden düşmesi için geçen süredir.
// retention, HLS oturum kayıtlarının saklanma süresidir ve HLS imzasının ömründen kısa olmamalıdır.
func New(s *store.Store, ts, origin SRS, idle, retention time.Duration) *Manager {
	return &Manager{store: s, ts: ts, origin: origin, idle: idle, retention: retention}
}

// OpenTS, SRS'in bildirdiği yeni bir .ts izlemesini kaydeder.
func (m *Manager) OpenTS(ctx context.Context, viewerID, channelID int64, clientID, ip string) error {
	evicted, err := m.store.OpenTSSession(ctx, viewerID, channelID, clientID, ip, m.idle)
	if err != nil {
		return err
	}
	m.kickEvicted(ctx, evicted)
	return nil
}

// TouchHLS, bir HLS isteğini oturumuna işler; gerekiyorsa oturumu açar.
func (m *Manager) TouchHLS(ctx context.Context, viewerID, channelID int64, key, ip string) error {
	evicted, err := m.store.TouchHLSSession(ctx, viewerID, channelID, key, ip, m.idle)
	if err != nil {
		return err
	}
	m.kickEvicted(ctx, evicted)
	return nil
}

func (m *Manager) CloseTS(ctx context.Context, clientID string) error {
	return m.store.CloseTSSession(ctx, clientID)
}

// ActiveCount, izleyicinin bağlantı limitinde sayılan oturumlarının sayısıdır.
func (m *Manager) ActiveCount(ctx context.Context, viewerID int64) (int, error) {
	return m.store.ActiveSessionCount(ctx, viewerID, m.idle)
}

func (m *Manager) kickEvicted(ctx context.Context, evicted []store.Evicted) {
	for _, e := range evicted {
		if e.Kind != "ts" {
			continue // sonlandırılmış HLS oturumunu geçit bir sonraki istekte reddeder
		}
		kickCtx, cancel := context.WithTimeout(ctx, kickTimeout)
		if err := m.ts.Kick(kickCtx, e.Key); err != nil {
			log.Printf("session: yerinden edilen bağlantı kesilemedi, sonra yeniden denenecek: %v", err)
		}
		cancel()
	}
}

// KickPublisher, bir kanalın süren yayınını keser (ör. yayın anahtarı yenilendiğinde).
// Kesme başarısız olursa yalnızca loglanır; çağıranın işlemi bundan etkilenmez.
func (m *Manager) KickPublisher(ctx context.Context, channelID int64) {
	publisher, _, err := m.store.ChannelConnections(ctx, channelID)
	if err != nil || publisher == "" {
		return
	}
	m.kickNow(ctx, m.origin, publisher)
}

// EndChannel, bir kanalın süren yayınını ve .ts izlemelerini keser (ör. kanal silinirken).
func (m *Manager) EndChannel(ctx context.Context, channelID int64) {
	publisher, viewers, err := m.store.ChannelConnections(ctx, channelID)
	if err != nil {
		return
	}
	if publisher != "" {
		m.kickNow(ctx, m.origin, publisher)
	}
	for _, id := range viewers {
		m.kickNow(ctx, m.ts, id)
	}
}

// EndViewer, bir izleyicinin süren .ts izlemelerini keser (ör. izleyici silinirken).
func (m *Manager) EndViewer(ctx context.Context, viewerID int64) {
	ids, err := m.store.ViewerTSConnections(ctx, viewerID)
	if err != nil {
		return
	}
	for _, id := range ids {
		m.kickNow(ctx, m.ts, id)
	}
}

func (m *Manager) kickNow(ctx context.Context, srs SRS, id string) {
	kickCtx, cancel := context.WithTimeout(ctx, kickTimeout)
	defer cancel()
	if err := srs.Kick(kickCtx, id); err != nil {
		log.Printf("session: bağlantı kesilemedi: %v", err)
	}
}

// Run, EnforceOnce'ı bağlam iptal edilene kadar düzenli olarak çalıştırır.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := m.EnforceOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("session: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// EnforceOnce:
//  1. yerinden edilmiş veya izleme hakkını yitirmiş .ts bağlantılarını keser,
//  2. askıdaki yayıncıların süren yayınlarını keser,
//  3. SRS'te artık olmayan .ts oturumlarını siler,
//  4. saklama süresi dolan HLS oturum kayıtlarını siler.
//
// Origin ve .ts dağıtıcısı ayrı SRS'lerdir ve adımları birbirinden bağımsız çalışır: biri yanıt
// vermese de diğerinin işi gecikmez. Geçişin tamamı passTimeout ile sınırlıdır.
func (m *Manager) EnforceOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()

	var originErr error
	originDone := make(chan struct{})
	go func() {
		defer close(originDone)
		originErr = m.kickAll(ctx, m.origin, m.store.SuspendedLivePublishers)
	}()
	tsErr := errors.Join(
		m.kickAll(ctx, m.ts, m.store.TSSessionsToKick),
		m.reconcile(ctx),
	)
	<-originDone
	return errors.Join(tsErr, originErr, m.expire(ctx))
}

// kickAll, listedeki bağlantıları keser. SRS'in reddettiği bir kesme (ör. bağlantı zaten kopmuş)
// diğerlerini engellemez; SRS'e ulaşılamıyorsa bu geçişte o SRS için denemeler durur.
func (m *Manager) kickAll(ctx context.Context, srs SRS, list func(context.Context) ([]string, error)) error {
	ids, err := list(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		err := srs.Kick(ctx, id)
		var rejected *srsapi.StatusError
		switch {
		case err == nil:
		case errors.As(err, &rejected):
			errs = append(errs, err)
		default:
			return errors.Join(append(errs, err)...)
		}
	}
	return errors.Join(errs...)
}

// reconcile, SRS'e ulaşılamazsa hiçbir oturuma dokunmadan hata döner.
func (m *Manager) reconcile(ctx context.Context) error {
	ids, err := m.ts.ClientIDs(ctx)
	if err != nil {
		return err
	}
	_, err = m.store.ReconcileTSSessions(ctx, ids, reconcileGrace)
	return err
}

func (m *Manager) expire(ctx context.Context) error {
	_, err := m.store.DeleteExpiredHLSSessions(ctx, m.retention)
	return err
}
