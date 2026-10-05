package store

import (
	"context"
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

func (s *Store) SetChannelSecret(ctx context.Context, tenantID, id int64, secret string) error {
	return s.one(ctx, `UPDATE channels SET stream_secret = $3 WHERE id = $2 AND tenant_id = $1`, tenantID, id, secret)
}

// DeleteChannel, kanalı ve oturum kayıtlarını siler. Süren yayın ve izlemeler ayrıca kesilmelidir
// (bkz. session.Manager.EndChannel).
func (s *Store) DeleteChannel(ctx context.Context, tenantID, id int64) error {
	return s.one(ctx, `DELETE FROM channels WHERE id = $2 AND tenant_id = $1`, tenantID, id)
}

func (s *Store) ViewersByTenant(ctx context.Context, tenantID int64) ([]Viewer, error) {
	rows, err := s.pool.Query(ctx, viewerSelect+`WHERE v.tenant_id = $1 ORDER BY v.id`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Viewer, error) { return scanViewer(row) })
}

func (s *Store) ViewerOfTenant(ctx context.Context, tenantID, id int64) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.id = $2 AND v.tenant_id = $1`, tenantID, id))
}

func (s *Store) UpdateViewer(ctx context.Context, tenantID, id int64, status string, expiresAt *time.Time, maxConnections int) error {
	return s.one(ctx, `
		UPDATE viewers SET status = $3, expires_at = $4, max_connections = $5
		WHERE id = $2 AND tenant_id = $1`, tenantID, id, status, expiresAt, maxConnections)
}

func (s *Store) SetViewerPassword(ctx context.Context, tenantID, id int64, password string) error {
	return s.one(ctx, `UPDATE viewers SET password = $3 WHERE id = $2 AND tenant_id = $1`, tenantID, id, password)
}

// DeleteViewer, izleyiciyi ve oturum kayıtlarını siler. Süren izlemeler ayrıca kesilmelidir
// (bkz. session.Manager.EndViewer).
func (s *Store) DeleteViewer(ctx context.Context, tenantID, id int64) error {
	return s.one(ctx, `DELETE FROM viewers WHERE id = $2 AND tenant_id = $1`, tenantID, id)
}

// SessionInfo, yayıncı panelinde gösterilen süren bir izlemedir.
type SessionInfo struct {
	ID        int64
	Viewer    string
	Channel   string
	Kind      string
	IP        string
	StartedAt time.Time
}

func (s *Store) ActiveSessionsByTenant(ctx context.Context, tenantID int64, idle time.Duration) ([]SessionInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT x.id, v.username, c.name, x.kind, x.ip, x.started_at
		FROM sessions x
		JOIN viewers v ON v.id = x.viewer_id
		JOIN channels c ON c.id = x.channel_id
		WHERE x.tenant_id = $2 AND NOT x.revoked
		  AND (x.kind = 'ts' OR x.last_seen_at > now() - make_interval(secs => $1))
		ORDER BY x.started_at, x.id`, idle.Seconds(), tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SessionInfo])
}

// ChannelConnections, bir kanalın SRS'teki bağlantılarını döner: yayıncının origin'deki bağlantı
// kimliği (yayında değilse boş) ve .ts izleyicilerinin dağıtıcıdaki bağlantı kimlikleri.
func (s *Store) ChannelConnections(ctx context.Context, channelID int64) (publisher string, tsViewers []string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT CASE WHEN live THEN coalesce(publisher_client_id, '') ELSE '' END
		FROM channels WHERE id = $1`, channelID).Scan(&publisher)
	if err != nil {
		return "", nil, notFound(err)
	}
	tsViewers, err = s.strings(ctx, `SELECT session_key FROM sessions WHERE kind = 'ts' AND channel_id = $1 ORDER BY session_key`, channelID)
	return publisher, tsViewers, err
}

// ViewerTSConnections, bir izleyicinin .ts dağıtıcısındaki bağlantı kimliklerini döner.
func (s *Store) ViewerTSConnections(ctx context.Context, viewerID int64) ([]string, error) {
	return s.strings(ctx, `SELECT session_key FROM sessions WHERE kind = 'ts' AND viewer_id = $1 ORDER BY session_key`, viewerID)
}
