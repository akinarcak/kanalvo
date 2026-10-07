package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrSessionRevoked: oturum, bağlantı limiti yüzünden yerini daha yeni bir oturuma bırakmış.
	ErrSessionRevoked = errors.New("store: oturum sonlandırılmış")
	// ErrSessionMoved: HLS oturumu kısa süre önce başka bir ağa taşınmış; bu ağdan şimdilik kullanılamaz.
	ErrSessionMoved = errors.New("store: oturum başka bir ağdan kullanılıyor")
	// ErrTenantConnectionLimit: yayıncının toplam eşzamanlı bağlantı kotası dolu.
	ErrTenantConnectionLimit = errors.New("store: yayıncının bağlantı kotası dolu")
)

const (
	// hlsMoveCooldown: bir HLS oturumu başka bir ağa taşındıktan sonra bu süre dolmadan yeniden
	// taşınamaz. Ağ değiştiren izleyici kesintisiz devam eder; aynı adresi paylaşan iki kişi ise
	// sırayla izleyemez.
	hlsMoveCooldown = time.Minute
	// hlsTouchInterval: süren bir HLS oturumunun son görülme zamanı en sık bu aralıkla yazılır.
	// HLS istekleri saniyede bir gelir; her birinde yazmak gereksiz yük olurdu. Oturumun boşta
	// sayılma süresinden (idle) belirgin biçimde kısa olmalıdır.
	hlsTouchInterval = 10 * time.Second
)

// Evicted, yeni bir oturuma yer açmak için sonlandırılan oturumdur.
// Kind "ts" ise Key, EdgeID numaralı edge'in SRS'inde kesilmesi gereken bağlantının kimliğidir.
type Evicted struct {
	Kind   string
	Key    string
	EdgeID int64
}

// activeSession, bir oturumun bağlantı limitinde sayılıp sayılmadığını belirleyen koşuldur:
// .ts oturumları SRS bitti diyene kadar, HLS oturumları son istekten idle süresi geçene kadar etkindir.
// $1 her kullanımda idle süresidir (saniye).
const activeSession = `NOT revoked AND (kind = 'ts' OR last_seen_at > now() - make_interval(secs => $1))`

// TouchHLSSession, bir HLS isteğini oturumuna işler: süren oturumun son görülme zamanını
// günceller; oturum yoksa, boşta kalmışsa veya başka bir ağa taşınıyorsa bağlantı limitini
// uygulayarak kabul eder. Oturumun kimliği anahtarıdır; ip, o an kullanıldığı ağdır; edgeID,
// isteğin geldiği edge'dir.
func (s *Store) TouchHLSSession(ctx context.Context, edgeID, viewerID, channelID int64, key, ip string, idle time.Duration) ([]Evicted, error) {
	// Sık yol: kilit almadan tek okuma. Sonlandırılmış bir adresin yeniden denemeleri de burada
	// reddedilir, böylece yayıncı satırının kilidini meşgul etmez.
	var dead, sameIP, sameEdge, active, fresh, movable bool
	err := s.pool.QueryRow(ctx, `
		SELECT revoked OR viewer_id <> $3,
		       ip = $2,
		       edge_id = $7,
		       last_seen_at > now() - make_interval(secs => $4),
		       last_seen_at > now() - make_interval(secs => $5),
		       (ip_changed_at IS NULL OR ip_changed_at <= now() - make_interval(secs => $6))
		FROM sessions WHERE kind = 'hls' AND session_key = $1`,
		key, ip, viewerID, idle.Seconds(), hlsTouchInterval.Seconds(), hlsMoveCooldown.Seconds(), edgeID).
		Scan(&dead, &sameIP, &sameEdge, &active, &fresh, &movable)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, err
	case dead:
		return nil, ErrSessionRevoked
	case sameIP && active:
		if !fresh || !sameEdge {
			_, err = s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now(), edge_id = $2 WHERE kind = 'hls' AND session_key = $1 AND NOT revoked`, key, edgeID)
		}
		return nil, err
	case !sameIP && !movable:
		return nil, ErrSessionMoved
	}
	return s.admit(ctx, "hls", edgeID, viewerID, channelID, key, ip, idle)
}

// OpenTSSession, bir edge'in SRS'inin bildirdiği yeni .ts izlemesini bağlantı limitini uygulayarak
// kaydeder. clientID yalnızca o edge'de tekildir.
func (s *Store) OpenTSSession(ctx context.Context, edgeID, viewerID, channelID int64, clientID, ip string, idle time.Duration) ([]Evicted, error) {
	return s.admit(ctx, "ts", edgeID, viewerID, channelID, clientID, ip, idle)
}

// admit, bir oturumu kabul eder. İzleyici limitine ulaşılmışsa en eski oturumları sonlandırır
// (kanal değiştiren izleyici takılmasın diye); yayıncı kotası doluysa reddeder.
func (s *Store) admit(ctx context.Context, kind string, edgeID, viewerID, channelID int64, key, ip string, idle time.Duration) ([]Evicted, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Yayıncı satırını kilitlemek, aynı yayıncıya ait oturum açılışlarını sıraya sokar; limit
	// denetimi ile kayıt arasına başka bir açılış giremez. NO KEY: yayıncıya bağlı başka
	// kayıtların (kanal, izleyici) eklenmesini engellemez.
	var viewerMax, tenantMax int
	var tenantID int64
	err = tx.QueryRow(ctx, `
		SELECT v.max_connections, t.id, t.max_connections
		FROM viewers v JOIN tenants t ON t.id = v.tenant_id
		WHERE v.id = $1 FOR NO KEY UPDATE OF t`, viewerID).Scan(&viewerMax, &tenantID, &tenantMax)
	if err != nil {
		return nil, notFound(err)
	}

	var rowID, owner int64
	var revoked, movable bool
	var rowIP string
	err = tx.QueryRow(ctx, `
		SELECT id, viewer_id, revoked, ip, (ip_changed_at IS NULL OR ip_changed_at <= now() - make_interval(secs => $3))
		FROM sessions WHERE kind = $1 AND session_key = $2 AND (kind = 'hls' OR edge_id = $4)`,
		kind, key, hlsMoveCooldown.Seconds(), edgeID).Scan(&rowID, &owner, &revoked, &rowIP, &movable)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if exists && (revoked || owner != viewerID) {
		return nil, ErrSessionRevoked
	}
	if exists && kind == "hls" && rowIP != ip && !movable {
		return nil, ErrSessionMoved
	}

	rows, err := tx.Query(ctx, `
		SELECT id, kind, session_key, edge_id FROM sessions
		WHERE viewer_id = $2 AND id <> $3 AND `+activeSession+`
		ORDER BY started_at, id`, idle.Seconds(), viewerID, rowID)
	if err != nil {
		return nil, err
	}
	type active struct {
		id int64
		Evicted
	}
	others, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (active, error) {
		var a active
		err := row.Scan(&a.id, &a.Kind, &a.Key, &a.EdgeID)
		return a, err
	})
	if err != nil {
		return nil, err
	}
	var evicted []Evicted
	for len(others) > 0 && len(others) >= viewerMax {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked = true WHERE id = $1`, others[0].id); err != nil {
			return nil, err
		}
		evicted = append(evicted, others[0].Evicted)
		others = others[1:]
	}

	var tenantActive int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE tenant_id = $2 AND id <> $3 AND `+activeSession,
		idle.Seconds(), tenantID, rowID).Scan(&tenantActive)
	if err != nil {
		return nil, err
	}
	if tenantActive >= tenantMax {
		return nil, ErrTenantConnectionLimit
	}

	if exists {
		_, err = tx.Exec(ctx, `
			UPDATE sessions
			SET channel_id = $2, edge_id = $4, started_at = now(), last_seen_at = now(),
			    ip_changed_at = CASE WHEN ip <> $3 THEN now() ELSE ip_changed_at END,
			    ip = $3
			WHERE id = $1`, rowID, channelID, ip, edgeID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO sessions (tenant_id, viewer_id, channel_id, edge_id, kind, session_key, ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, tenantID, viewerID, channelID, edgeID, kind, key, ip)
	}
	if err != nil {
		return nil, err
	}
	return evicted, tx.Commit(ctx)
}

// CloseTSSession, edge'in SRS'i "izleme bitti" dediğinde .ts oturumunu siler.
func (s *Store) CloseTSSession(ctx context.Context, edgeID int64, clientID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE kind = 'ts' AND edge_id = $1 AND session_key = $2`, edgeID, clientID)
	return err
}

// ActiveSessionCount, izleyicinin bağlantı limitinde sayılan oturumlarının sayısıdır.
func (s *Store) ActiveSessionCount(ctx context.Context, viewerID int64, idle time.Duration) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE viewer_id = $2 AND `+activeSession,
		idle.Seconds(), viewerID).Scan(&n)
	return n, err
}

// TSSessionsToKick, bir edge'in SRS'inde kesilmesi gereken .ts bağlantılarının kimliklerini döner:
// yerinden edilmiş oturumlar ve artık izleme hakkı olmayan (askıda, süresi dolmuş) izleyicilerin oturumları.
func (s *Store) TSSessionsToKick(ctx context.Context, edgeID int64) ([]string, error) {
	return s.strings(ctx, `
		SELECT x.session_key
		FROM sessions x
		JOIN viewers v ON v.id = x.viewer_id
		JOIN tenants t ON t.id = x.tenant_id
		WHERE x.kind = 'ts' AND x.edge_id = $1
		  AND (x.revoked OR v.status <> 'active' OR t.status <> 'active'
		       OR (v.expires_at IS NOT NULL AND v.expires_at <= now()))
		ORDER BY x.session_key`, edgeID)
}

// ReconcileTSSessions, bir edge'in SRS bağlantı listesinde olmayan .ts oturumlarını siler (SRS
// çökerse "izleme bitti" bildirimi gelmez). grace süresinden yeni oturumlara ve başka edge'lerin
// oturumlarına dokunmaz.
func (s *Store) ReconcileTSSessions(ctx context.Context, edgeID int64, activeClientIDs []string, grace time.Duration) (int64, error) {
	if activeClientIDs == nil {
		activeClientIDs = []string{}
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sessions
		WHERE kind = 'ts' AND edge_id = $3
		  AND started_at <= now() - make_interval(secs => $2)
		  AND NOT (session_key = ANY($1))`, activeClientIDs, grace.Seconds(), edgeID)
	return tag.RowsAffected(), err
}

// DeleteExpiredHLSSessions, son isteği retention süresinden eski HLS oturumlarını siler.
// retention, HLS imzasının ömründen kısa olmamalıdır; aksi halde sonlandırılmış bir oturumun
// adresi yeniden kullanılabilir hale gelir.
func (s *Store) DeleteExpiredHLSSessions(ctx context.Context, retention time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sessions WHERE kind = 'hls' AND last_seen_at < now() - make_interval(secs => $1)`,
		retention.Seconds())
	return tag.RowsAffected(), err
}

// SuspendedLivePublishers, askıdaki yayıncıların süren yayınlarının SRS bağlantı kimliklerini döner.
func (s *Store) SuspendedLivePublishers(ctx context.Context) ([]string, error) {
	return s.strings(ctx, `
		SELECT c.publisher_client_id
		FROM channels c JOIN tenants t ON t.id = c.tenant_id
		WHERE c.live AND c.publisher_client_id IS NOT NULL AND t.status <> 'active'
		ORDER BY c.publisher_client_id`)
}

func (s *Store) strings(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
