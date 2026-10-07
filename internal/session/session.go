// Package session, izleyici oturumlarını yönetir: bağlantı limitini uygular, yerinden edilen
// veya izleme hakkını yitiren bağlantıları edge'lerin SRS'inde keser, kayıtları SRS'le eşitler
// ve edge'lerin sağlık sinyalini kaydeder.
package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sync"
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
	// deadEdgeAfter: bu süredir ulaşılamayan bir edge'in .ts oturumları silinir. Kısa kesintilerde
	// oturumlar korunur; kapanmış bir sunucunun izleyicileri ise kotayı süresiz doldurmaz.
	deadEdgeAfter = 2 * time.Minute
)

// edgeTimeout, bir geçişte tek bir SRS'e (bağlantı listesi ve kesmeler) ayrılan en uzun süredir.
// Denetim aralığından kısadır: yanıt vermeyen bir edge diğerlerinin sağlık sinyalini geciktirmez.
var edgeTimeout = 4 * time.Second

var (
	// Giden bağlantının geçici bağlantı noktası: "172.18.0.3:51234->172.18.0.7:80".
	sourcePortPattern = regexp.MustCompile(`:\d+->`)
	// Kesilmek istenen bağlantının kimliği: "DELETE /api/v1/clients/abc123".
	clientIDPattern = regexp.MustCompile(`/clients/[A-Za-z0-9_-]+`)
)

// failureKey, bir arızanın loglarda tekrarlanıp tekrarlanmadığını anlamak için kullanılan
// metindir. Her denemede değişen kısımlar (geçici bağlantı noktası, bağlantı kimliği) atılır;
// aksi halde süren tek bir arıza her geçişte yeni bir arıza gibi loga yazılırdı.
func failureKey(err error) string {
	if err == nil {
		return ""
	}
	key := sourcePortPattern.ReplaceAllString(err.Error(), "->")
	return clientIDPattern.ReplaceAllString(key, "/clients/*")
}

// SRS, bir SRS'in yönetim API'sinin kullanılan kısmıdır (bkz. srsapi.Client).
type SRS interface {
	ClientIDs(ctx context.Context) ([]string, error)
	Kick(ctx context.Context, id string) error
}

type Manager struct {
	store *store.Store
	// srsFor, bir edge'in izleyicilerinin bağlandığı SRS'in yönetim API'sini verir.
	srsFor    func(store.Edge) SRS
	origin    SRS // yayıncıların bağlandığı origin
	idle      time.Duration
	retention time.Duration

	mu sync.Mutex
	// down: son denetim geçişinde bağlantı listesi alınamayan edge'ler. İstek sırasındaki kesmeler
	// (yeni izleme, panel işlemi) bunlara gitmez; denetim döngüsü denemeyi sürdürür.
	down map[int64]bool
}

// New: idle, istek gelmeyen bir HLS oturumunun bağlantı limitinden düşmesi için geçen süredir.
// retention, HLS oturum kayıtlarının saklanma süresidir ve HLS imzasının ömründen kısa olmamalıdır.
func New(s *store.Store, srsFor func(store.Edge) SRS, origin SRS, idle, retention time.Duration) *Manager {
	return &Manager{store: s, srsFor: srsFor, origin: origin, idle: idle, retention: retention, down: map[int64]bool{}}
}

func (m *Manager) isDown(edgeID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.down[edgeID]
}

// forgetMissing, artık kayıtlı olmayan edge'lerin "ulaşılamıyor" kaydını siler.
func (m *Manager) forgetMissing(edges []store.Edge) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.down {
		if !slices.ContainsFunc(edges, func(e store.Edge) bool { return e.ID == id }) {
			delete(m.down, id)
		}
	}
}

func (m *Manager) setDown(edgeID int64, down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if down {
		m.down[edgeID] = true
	} else {
		delete(m.down, edgeID)
	}
}

// OpenTS, bir edge'in SRS'inin bildirdiği yeni .ts izlemesini kaydeder.
func (m *Manager) OpenTS(ctx context.Context, edgeID, viewerID, channelID int64, clientID, ip string) error {
	evicted, err := m.store.OpenTSSession(ctx, edgeID, viewerID, channelID, clientID, ip, m.idle)
	if err != nil {
		return err
	}
	m.kickEvicted(ctx, evicted)
	return nil
}

// TouchHLS, bir edge'e gelen HLS isteğini oturumuna işler; gerekiyorsa oturumu açar.
func (m *Manager) TouchHLS(ctx context.Context, edgeID, viewerID, channelID int64, key, ip string) error {
	evicted, err := m.store.TouchHLSSession(ctx, edgeID, viewerID, channelID, key, ip, m.idle)
	if err != nil {
		return err
	}
	m.kickEvicted(ctx, evicted)
	return nil
}

func (m *Manager) CloseTS(ctx context.Context, edgeID int64, clientID string) error {
	return m.store.CloseTSSession(ctx, edgeID, clientID)
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
		if m.isDown(e.EdgeID) {
			continue // izleme isteği ulaşılamayan edge'i beklemez; bağlantıyı denetim döngüsü keser
		}
		kickCtx, cancel := context.WithTimeout(ctx, kickTimeout)
		edge, err := m.store.EdgeByID(kickCtx, e.EdgeID, 0)
		if err == nil {
			err = m.srsFor(edge).Kick(kickCtx, e.Key)
		}
		if err != nil {
			log.Printf("session: yerinden edilen bağlantı kesilemedi, sonra yeniden denenecek: %v", err)
		}
		cancel()
	}
}

// Flush, kesilmeyi bekleyen bağlantıları hemen kesmeyi dener. Bir kanal veya izleyici silindikten,
// yayın anahtarı ya da izleyici şifresi yenilendikten sonra çağrılır; böylece etkisi bir sonraki
// döngü geçişini beklemez. İsteğin iptalinden etkilenmez; başarısız olanları ve ulaşılamadığı
// bilinen edge'lerdekileri Enforce döngüsü yeniden dener.
func (m *Manager) Flush(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
	defer cancel()
	if err := m.pass(ctx, false); err != nil {
		log.Printf("session: bekleyen bağlantılar kesilemedi, yeniden denenecek: %v", err)
	}
}

// Run, EnforceOnce'ı bağlam iptal edilene kadar düzenli olarak çalıştırır. Süren bir arıza
// (ör. ulaşılamayan bir edge) her geçişte değil, yalnızca başladığında ve bittiğinde loga yazılır.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	last := ""
	for {
		err := m.EnforceOnce(ctx)
		if ctx.Err() == nil {
			current := failureKey(err)
			switch {
			case current != "" && current != last:
				log.Printf("session: %v", err)
			case current == "" && last != "":
				log.Printf("session: denetim yeniden sorunsuz çalışıyor")
			}
			last = current
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// EnforceOnce, origin ve her edge için:
//  1. kesilmeyi bekleyen bağlantıları (silinen kanal ve izleyiciler, yenilenen yayın anahtarları),
//     yerinden edilmiş veya izleme hakkını yitirmiş .ts bağlantılarını ve askıdaki yayıncıların
//     süren yayınlarını keser,
//  2. edge'in SRS'inde artık olmayan .ts oturumlarını siler ve yanıt veren edge'in sağlık
//     sinyalini kaydeder,
//
// ardından saklama süresi dolan HLS oturum kayıtlarını, eskimiş kesme kayıtlarını ve uzun süredir
// ulaşılamayan edge'lerin .ts oturumlarını siler.
// Geçişin tamamı passTimeout ile sınırlıdır.
func (m *Manager) EnforceOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()
	return errors.Join(m.pass(ctx, true), m.expire(ctx))
}

// pass, origin'i ve her edge'i birbirinden bağımsız denetler: biri yanıt vermese de diğerlerinin
// işi gecikmez ve geçişin SRS'lerle geçen kısmı edgeTimeout'tan uzun sürmez. reconcile kapalıysa yalnızca kesmeler yapılır
// ve ulaşılamadığı bilinen edge'ler atlanır.
func (m *Manager) pass(ctx context.Context, reconcile bool) error {
	edges, listErr := m.store.Edges(ctx, 0)
	errs := make([]error, len(edges)+2)
	errs[len(edges)+1] = listErr
	if listErr == nil {
		m.forgetMissing(edges)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(ctx, edgeTimeout)
		defer cancel()
		errs[len(edges)] = errors.Join(
			m.kickAll(ctx, m.origin, m.store.PendingOriginKicks, func(id string) error { return m.store.ResolveOriginKick(ctx, id) }),
			m.kickAll(ctx, m.origin, m.store.SuspendedLivePublishers, nil),
		)
	}()
	for i, e := range edges {
		if !reconcile && m.isDown(e.ID) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.enforceEdge(ctx, e, reconcile); err != nil {
				errs[i] = fmt.Errorf("edge %q: %w", e.Name, err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// enforceEdge, bir edge'e edgeTimeout kadar süre ayırır. Önce bağlantı listesi alınır: bu, edge'in
// sağlık sinyalidir ve kesmelerin ne kadar sürdüğünden etkilenmemelidir. Liste alınamasa da kesmeler
// denenir (SRS listeyi veremeyip kesmeleri yanıtlayabilir); yalnızca sağlık sinyali ve eşitleme
// atlanır. Süreye sığmayan kesmeler sonraki geçişe kalır.
func (m *Manager) enforceEdge(ctx context.Context, e store.Edge, reconcile bool) error {
	srs := m.srsFor(e)
	srsCtx, cancel := context.WithTimeout(ctx, edgeTimeout)
	defer cancel()

	var clients []string
	var listed time.Time
	var listErr error
	if reconcile {
		clients, listErr = srs.ClientIDs(srsCtx)
		listed = time.Now()
		m.setDown(e.ID, listErr != nil)
		if listErr == nil {
			listErr = m.store.MarkEdgeSeen(ctx, e.ID)
		}
	}

	pending := func(ctx context.Context) ([]string, error) { return m.store.PendingEdgeKicks(ctx, e.ID) }
	resolve := func(id string) error { return m.store.ResolveEdgeKick(ctx, e.ID, id) }
	unentitled := func(ctx context.Context) ([]string, error) { return m.store.TSSessionsToKick(ctx, e.ID) }
	err := errors.Join(
		listErr,
		m.kickAll(srsCtx, srs, pending, resolve),
		m.kickAll(srsCtx, srs, unentitled, nil),
	)
	if !reconcile || listErr != nil {
		return err
	}
	// Liste kesmelerden önce alındı: o andan sonra açılan oturumlar listede yoktur, bu yüzden
	// tanınan süreye aradan geçen zaman eklenir. Az önce kesilen bağlantıların kayıtları bir
	// sonraki geçişte silinir.
	_, recErr := m.store.ReconcileTSSessions(ctx, e.ID, clients, reconcileGrace+time.Since(listed))
	return errors.Join(err, recErr)
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
			// done yoksa kayıt bir oturumdur; zaten kopmuş bağlantının kaydını eşitleme siler.
			if done != nil {
				errs = append(errs, done(id))
			}
		default:
			return errors.Join(append(errs, err)...)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) expire(ctx context.Context) error {
	_, hlsErr := m.store.DeleteExpiredHLSSessions(ctx, m.retention)
	_, kickErr := m.store.DeleteStaleKicks(ctx, staleKickAge)
	_, edgeErr := m.store.DeleteTSSessionsOfUnreachableEdges(ctx, deadEdgeAfter)
	return errors.Join(hlsErr, kickErr, edgeErr)
}
