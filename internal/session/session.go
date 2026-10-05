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

const (
	// kickTimeout, bir istek sırasında tek bir bağlantıyı kesmek için beklenen en uzun süredir.
	// Kesme başarısız olursa Enforce döngüsü yeniden dener.
	kickTimeout = 2 * time.Second
	// flushTimeout, bir panel işleminin ardından bekleyen kesmeleri hemen denemek için ayrılan süredir.
	flushTimeout = 5 * time.Second
	// reconcileGrace, yeni açılmış bir .ts oturumunun SRS listesinde görünmesi için tanınan süredir.
	reconcileGrace = 10 * time.Second
	// passTimeout, tek bir uygulama geçişinin en uzun süresidir.
	passTimeout = 20 * time.Second
	// staleKickAge: bu süredir kesilemeyen bir bağlantının kaydı silinir (SRS yeniden başlamış,
	// bağlantı zaten yok olmuştur).
	staleKickAge = time.Hour
)

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

// Flush, kesilmeyi bekleyen bağlantıları hemen kesmeyi dener. Bir kanal veya izleyici silindikten,
// yayın anahtarı ya da izleyici şifresi yenilendikten sonra çağrılır; böylece etkisi bir sonraki
// döngü geçişini beklemez. İsteğin iptalinden etkilenmez; başarısız olanları Enforce döngüsü yeniden dener.
func (m *Manager) Flush(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
	defer cancel()
	if err := m.kickPass(ctx); err != nil {
		log.Printf("session: bekleyen bağlantılar kesilemedi, yeniden denenecek: %v", err)
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
//  1. kesilmeyi bekleyen bağlantıları (silinen kanal ve izleyiciler, yenilenen yayın anahtarları),
//     yerinden edilmiş veya izleme hakkını yitirmiş .ts bağlantılarını ve askıdaki yayıncıların
//     süren yayınlarını keser,
//  2. SRS'te artık olmayan .ts oturumlarını siler,
//  3. saklama süresi dolan HLS oturum kayıtlarını ve eskimiş kesme kayıtlarını siler.
//
// Geçişin tamamı passTimeout ile sınırlıdır.
func (m *Manager) EnforceOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()
	return errors.Join(m.kickPass(ctx), m.reconcile(ctx), m.expire(ctx))
}

// kickPass, iki SRS'teki kesmeleri birbirinden bağımsız yürütür: biri yanıt vermese de diğerinin
// işi gecikmez.
func (m *Manager) kickPass(ctx context.Context) error {
	var originErr error
	originDone := make(chan struct{})
	go func() {
		defer close(originDone)
		originErr = errors.Join(
			m.kickPending(ctx, m.origin, "origin"),
			m.kickAll(ctx, m.origin, m.store.SuspendedLivePublishers, nil),
		)
	}()
	tsErr := errors.Join(
		m.kickPending(ctx, m.ts, "ts"),
		m.kickAll(ctx, m.ts, m.store.TSSessionsToKick, nil),
	)
	<-originDone
	return errors.Join(tsErr, originErr)
}

// kickPending, kuyruktaki bağlantıları keser ve kesilenleri (ya da SRS'te artık olmayanları) kuyruktan çıkarır.
func (m *Manager) kickPending(ctx context.Context, srs SRS, target string) error {
	list := func(ctx context.Context) ([]string, error) { return m.store.PendingKicks(ctx, target) }
	done := func(id string) error { return m.store.ResolveKick(ctx, target, id) }
	return m.kickAll(ctx, srs, list, done)
}

// kickAll, listedeki bağlantıları keser. SRS'in reddettiği bir kesme (bağlantı zaten kopmuş)
// tamamlanmış sayılır; SRS'e ulaşılamıyorsa bu geçişte o SRS için denemeler durur.
// done verilmişse tamamlanan her bağlantı için çağrılır.
func (m *Manager) kickAll(ctx context.Context, srs SRS, list func(context.Context) ([]string, error), done func(id string) error) error {
	ids, err := list(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		kickCtx, cancel := context.WithTimeout(ctx, kickTimeout)
		err := srs.Kick(kickCtx, id)
		cancel()
		var rejected *srsapi.StatusError
		switch {
		case err == nil, errors.As(err, &rejected):
			if done != nil {
				errs = append(errs, done(id))
			} else if err != nil {
				errs = append(errs, err)
			}
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
	_, hlsErr := m.store.DeleteExpiredHLSSessions(ctx, m.retention)
	_, kickErr := m.store.DeleteStaleKicks(ctx, staleKickAge)
	return errors.Join(hlsErr, kickErr)
}
