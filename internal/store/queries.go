package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateTenant(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	return id, err
}

func (s *Store) SetTenantStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tenants SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s *Store) CreateChannel(ctx context.Context, tenantID int64, name, secret string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO channels (tenant_id, name, stream_secret) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, name, secret).Scan(&id)
	return id, err
}

func (s *Store) ChannelByID(ctx context.Context, id int64) (Channel, error) {
	var c Channel
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.tenant_id, c.name, c.stream_secret, c.live, t.status
		FROM channels c JOIN tenants t ON t.id = c.tenant_id
		WHERE c.id = $1`, id).
		Scan(&c.ID, &c.TenantID, &c.Name, &c.StreamSecret, &c.Live, &c.TenantStatus)
	return c, notFound(err)
}

func (s *Store) CreateViewer(ctx context.Context, tenantID int64, username, password string, maxConnections int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO viewers (tenant_id, username, password, max_connections) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, username, password, maxConnections).Scan(&id)
	return id, err
}

const viewerSelect = `
	SELECT v.id, v.tenant_id, v.username, v.password, v.status, v.expires_at, v.max_connections, t.status
	FROM viewers v JOIN tenants t ON t.id = v.tenant_id `

func (s *Store) ViewerByUsername(ctx context.Context, username string) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.username = $1`, username))
}

func (s *Store) ViewerByID(ctx context.Context, id int64) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.id = $1`, id))
}

func scanViewer(row pgx.Row) (Viewer, error) {
	var v Viewer
	err := row.Scan(&v.ID, &v.TenantID, &v.Username, &v.Password, &v.Status, &v.ExpiresAt, &v.MaxConnections, &v.TenantStatus)
	return v, notFound(err)
}

func (s *Store) SetViewerStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE viewers SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s *Store) SetViewerExpiry(ctx context.Context, id int64, at *time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE viewers SET expires_at = $2 WHERE id = $1`, id, at)
	return err
}

// MarkLive, kanal çevrimdışıysa yayında olarak işaretler. Kanal zaten yayındaysa false döner.
func (s *Store) MarkLive(ctx context.Context, channelID int64, clientID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE channels SET live = true, publisher_client_id = $2, last_publish_at = now()
		WHERE id = $1 AND NOT live`, channelID, clientID)
	return tag.RowsAffected() == 1, err
}

// MarkOffline, yalnızca yayını açan bağlantının bildirimiyle kanalı çevrimdışı yapar.
func (s *Store) MarkOffline(ctx context.Context, channelID int64, clientID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE channels SET live = false, publisher_client_id = NULL
		WHERE id = $1 AND publisher_client_id = $2`, channelID, clientID)
	return err
}

// ReconcileLive, yayında görünen ama activeIDs içinde olmayan kanalları çevrimdışı yapar.
// grace süresinden daha yeni başlamış yayınlara dokunmaz.
func (s *Store) ReconcileLive(ctx context.Context, activeIDs []int64, grace time.Duration) (int64, error) {
	if activeIDs == nil {
		activeIDs = []int64{}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE channels SET live = false, publisher_client_id = NULL
		WHERE live
		  AND last_publish_at <= now() - make_interval(secs => $2)
		  AND NOT (id = ANY($1))`, activeIDs, grace.Seconds())
	return tag.RowsAffected(), err
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
