package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Bu dosyadaki işlemler yayıncı panelinden çağrılır. Her biri yayıncı kimliğini alır ve yalnızca
// o yayıncıya ait kayıtlara dokunur; başka yayıncının kaydı "yok" sayılır (ErrNotFound).

func (s *Store) RenameCategory(ctx context.Context, tenantID, id int64, name string) error {
	return s.one(ctx, `UPDATE categories SET name = $3 WHERE id = $2 AND tenant_id = $1`, tenantID, id, name)
}

func (s *Store) ChannelOfTenant(ctx context.Context, tenantID, id int64) (Channel, error) {
	return scanChannel(s.pool.QueryRow(ctx, channelSelect+`WHERE c.id = $2 AND c.tenant_id = $1`, tenantID, id))
}

// UpdateChannel: kategori verilmişse aynı yayıncıya ait olmalıdır; nil kategoriyi kaldırır.
func (s *Store) UpdateChannel(ctx context.Context, tenantID, id int64, name, logoURL string, categoryID *int64) error {
	return s.one(ctx, `
		UPDATE channels SET name = $3, logo_url = $4, category_id = $5
		WHERE id = $2 AND tenant_id = $1
		  AND ($5::bigint IS NULL OR EXISTS (SELECT 1 FROM categories k WHERE k.id = $5 AND k.tenant_id = $1))`,
		tenantID, id, name, logoURL, categoryID)
}

// SetChannelSecret, yayın anahtarını değiştirir. Kanal yayındaysa eski anahtarla süren yayın
// kesilmek üzere kuyruğa alınır.
func (s *Store) SetChannelSecret(ctx context.Context, tenantID, id int64, secret string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE channels SET stream_secret = $3 WHERE id = $2 AND tenant_id = $1`, tenantID, id, secret)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return queuePublisher(ctx, tx, id)
	})
}

// DeleteChannel, kanalı ve oturum kayıtlarını siler; süren yayını ve .ts izlemelerini aynı işlem
// içinde kesilmek üzere kuyruğa alır (bkz. PendingKicks). Kayıt önce silindiği için araya giren
// yeni bir bağlantı yetkilendirilemez.
func (s *Store) DeleteChannel(ctx context.Context, tenantID, id int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT true FROM channels WHERE id = $2 AND tenant_id = $1 FOR UPDATE`, tenantID, id).Scan(&exists)
		if err != nil {
			return notFound(err)
		}
		if err := queuePublisher(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pending_kicks (target, edge_id, client_id)
			SELECT 'ts', edge_id, session_key FROM sessions WHERE kind = 'ts' AND channel_id = $1
			ON CONFLICT DO NOTHING`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM channels WHERE id = $1`, id)
		return err
	})
}

// queuePublisher, kanal yayındaysa yayıncı bağlantısını kesilmek üzere kuyruğa alır.
func queuePublisher(ctx context.Context, tx pgx.Tx, channelID int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO pending_kicks (target, client_id)
		SELECT 'origin', publisher_client_id FROM channels
		WHERE id = $1 AND live AND publisher_client_id IS NOT NULL
		ON CONFLICT DO NOTHING`, channelID)
	return err
}

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return conflict(err)
	}
	return tx.Commit(ctx)
}

// Page, bir listenin istenen dilimidir.
type Page struct {
	Limit  int
	Offset int
}

// likeEscaper, arama metnindeki LIKE özel karakterlerini düz karaktere çevirir.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ViewersByTenant, yayıncının izleyicilerinin istenen dilimini (en yeni önce) ve toplam sayısını
// döner. search verilmişse yalnızca kullanıcı adında o metin geçenler (büyük/küçük harf ayırmadan).
func (s *Store) ViewersByTenant(ctx context.Context, tenantID int64, search string, p Page) ([]Viewer, int, error) {
	const where = `WHERE v.tenant_id = $1 AND v.username ILIKE '%' || $2 || '%' `
	pattern := likeEscaper.Replace(search)
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM viewers v `+where, tenantID, pattern).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, viewerSelect+where+`ORDER BY v.id DESC LIMIT $3 OFFSET $4`, tenantID, pattern, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Viewer, error) { return scanViewer(row) })
	return list, total, err
}

func (s *Store) ViewerOfTenant(ctx context.Context, tenantID, id int64) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.id = $2 AND v.tenant_id = $1`, tenantID, id))
}

// ViewerUpdate, bir izleyicide değiştirilecek alanlardır; nil alanlara dokunulmaz.
// Bitiş tarihi nil "süresiz" anlamına geldiği için ayrıca SetExpiry ile işaretlenir.
type ViewerUpdate struct {
	Status         *string
	MaxConnections *int
	SetExpiry      bool
	ExpiresAt      *time.Time
}

func (s *Store) UpdateViewer(ctx context.Context, tenantID, id int64, u ViewerUpdate) error {
	return s.one(ctx, `
		UPDATE viewers SET
			status          = coalesce($3, status),
			max_connections = coalesce($4, max_connections),
			expires_at      = CASE WHEN $5 THEN $6 ELSE expires_at END
		WHERE id = $2 AND tenant_id = $1`, tenantID, id, u.Status, u.MaxConnections, u.SetExpiry, u.ExpiresAt)
}

// SetViewerPassword, şifreyi değiştirir ve izleyicinin süren tüm izlemelerini sonlandırır; böylece
// sızan eski şifreyle başlamış izlemeler de kesilir (.ts bağlantılarını uygulama döngüsü keser,
// HLS geçidi sonlandırılmış oturumu reddeder).
func (s *Store) SetViewerPassword(ctx context.Context, tenantID, id int64, password string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE viewers SET password = $3 WHERE id = $2 AND tenant_id = $1`, tenantID, id, password)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `UPDATE sessions SET revoked = true WHERE viewer_id = $1 AND NOT revoked`, id)
		return err
	})
}

// DeleteViewer, izleyiciyi ve oturum kayıtlarını siler; süren .ts izlemelerini aynı işlem içinde
// kesilmek üzere kuyruğa alır.
func (s *Store) DeleteViewer(ctx context.Context, tenantID, id int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT true FROM viewers WHERE id = $2 AND tenant_id = $1 FOR UPDATE`, tenantID, id).Scan(&exists)
		if err != nil {
			return notFound(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pending_kicks (target, edge_id, client_id)
			SELECT 'ts', edge_id, session_key FROM sessions WHERE kind = 'ts' AND viewer_id = $1
			ON CONFLICT DO NOTHING`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM viewers WHERE id = $1`, id)
		return err
	})
}

// SessionInfo, yayıncı panelinde gösterilen süren bir izlemedir.
type SessionInfo struct {
	ID        int64
	Viewer    string
	Channel   string
	Kind      string
	IP        string
	StartedAt time.Time
	Edge      string
}

// ActiveSessionsByTenant, yayıncının süren izlemelerinin istenen dilimini (en eski önce) ve
// toplam sayısını döner.
func (s *Store) ActiveSessionsByTenant(ctx context.Context, tenantID int64, idle time.Duration, p Page) ([]SessionInfo, int, error) {
	var total int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE tenant_id = $2 AND `+activeSession, idle.Seconds(), tenantID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT x.id, v.username, c.name, x.kind, x.ip, x.started_at, e.name
		FROM sessions x
		JOIN viewers v ON v.id = x.viewer_id
		JOIN channels c ON c.id = x.channel_id
		JOIN edges e ON e.id = x.edge_id
		WHERE x.tenant_id = $2 AND NOT x.revoked
		  AND (x.kind = 'ts' OR x.last_seen_at > now() - make_interval(secs => $1))
		ORDER BY x.started_at, x.id LIMIT $3 OFFSET $4`, idle.Seconds(), tenantID, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SessionInfo])
	return list, total, err
}

// --- kesilmeyi bekleyen bağlantılar ---

// PendingOriginKicks, origin'de kesilmeyi bekleyen yayıncı bağlantılarının kimliklerini döner.
func (s *Store) PendingOriginKicks(ctx context.Context) ([]string, error) {
	return s.strings(ctx, `SELECT client_id FROM pending_kicks WHERE target = 'origin' ORDER BY client_id`)
}

// PendingEdgeKicks, bir edge'in SRS'inde kesilmeyi bekleyen izleyici bağlantılarının kimliklerini döner.
func (s *Store) PendingEdgeKicks(ctx context.Context, edgeID int64) ([]string, error) {
	return s.strings(ctx, `SELECT client_id FROM pending_kicks WHERE target = 'ts' AND edge_id = $1 ORDER BY client_id`, edgeID)
}

// ResolveOriginKick, kesilen (veya origin'de artık bulunmayan) yayıncı bağlantısını kuyruktan çıkarır.
func (s *Store) ResolveOriginKick(ctx context.Context, clientID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pending_kicks WHERE target = 'origin' AND client_id = $1`, clientID)
	return err
}

// ResolveEdgeKick, kesilen (veya edge'de artık bulunmayan) izleyici bağlantısını kuyruktan çıkarır.
func (s *Store) ResolveEdgeKick(ctx context.Context, edgeID int64, clientID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pending_kicks WHERE target = 'ts' AND edge_id = $1 AND client_id = $2`, edgeID, clientID)
	return err
}

// DeleteStaleKicks, olderThan süresinden eski kayıtları siler; o kadar süredir kesilemeyen bir
// bağlantı büyük olasılıkla SRS yeniden başladığı için zaten yoktur.
func (s *Store) DeleteStaleKicks(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM pending_kicks WHERE created_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	return tag.RowsAffected(), err
}
