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

// CreateChannel, yayıncının kanal kotası doluysa ErrQuotaExceeded döner.
func (s *Store) CreateChannel(ctx context.Context, tenantID int64, name, secret string) (int64, error) {
	return s.insertWithinQuota(ctx, tenantID, "channels", "max_channels",
		`INSERT INTO channels (tenant_id, name, stream_secret) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, name, secret)
}

const channelSelect = `
	SELECT c.id, c.tenant_id, c.name, c.stream_secret, c.live, c.category_id, c.logo_url, c.created_at, t.status
	FROM channels c JOIN tenants t ON t.id = c.tenant_id `

func scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.StreamSecret, &c.Live, &c.CategoryID, &c.LogoURL, &c.CreatedAt, &c.TenantStatus)
	return c, notFound(err)
}

func (s *Store) ChannelByID(ctx context.Context, id int64) (Channel, error) {
	return scanChannel(s.pool.QueryRow(ctx, channelSelect+`WHERE c.id = $1`, id))
}

// ChannelsByTenant, yayıncının kanallarını numara sırasıyla döner.
func (s *Store) ChannelsByTenant(ctx context.Context, tenantID int64) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, channelSelect+`WHERE c.tenant_id = $1 ORDER BY c.id`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Channel, error) { return scanChannel(row) })
}

func (s *Store) CreateCategory(ctx context.Context, tenantID int64, name string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO categories (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenantID, name).Scan(&id)
	return id, err
}

// CategoriesByTenant, yayıncının kategorilerini sıra numarasına göre döner.
func (s *Store) CategoriesByTenant(ctx context.Context, tenantID int64) ([]Category, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, name, position FROM categories WHERE tenant_id = $1 ORDER BY position, id`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Category])
}

// DeleteCategory, yayıncının kategorisini siler; o kategorideki kanallar kategorisiz kalır.
func (s *Store) DeleteCategory(ctx context.Context, tenantID, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM categories WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetChannelCategory, kanalın kategorisini değiştirir; nil kategoriyi kaldırır.
// Kategori kanalla aynı yayıncıya ait değilse ErrNotFound döner.
func (s *Store) SetChannelCategory(ctx context.Context, channelID int64, categoryID *int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE channels c SET category_id = $2
		WHERE c.id = $1
		  AND ($2::bigint IS NULL OR EXISTS (
		        SELECT 1 FROM categories k WHERE k.id = $2 AND k.tenant_id = c.tenant_id))`,
		channelID, categoryID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// CreateViewer, yayıncının izleyici kotası doluysa ErrQuotaExceeded döner.
func (s *Store) CreateViewer(ctx context.Context, tenantID int64, username, password string, maxConnections int) (int64, error) {
	id, err := s.insertWithinQuota(ctx, tenantID, "viewers", "max_viewers",
		`INSERT INTO viewers (tenant_id, username, password, max_connections) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, username, password, maxConnections)
	return id, conflict(err) // kullanıcı adı platform genelinde tekildir
}

const viewerSelect = `
	SELECT v.id, v.tenant_id, v.username, v.password, v.status, v.expires_at, v.max_connections, v.created_at, t.status
	FROM viewers v JOIN tenants t ON t.id = v.tenant_id `

func (s *Store) ViewerByUsername(ctx context.Context, username string) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.username = $1`, username))
}

func (s *Store) ViewerByID(ctx context.Context, id int64) (Viewer, error) {
	return scanViewer(s.pool.QueryRow(ctx, viewerSelect+`WHERE v.id = $1`, id))
}

func scanViewer(row pgx.Row) (Viewer, error) {
	var v Viewer
	err := row.Scan(&v.ID, &v.TenantID, &v.Username, &v.Password, &v.Status, &v.ExpiresAt, &v.MaxConnections, &v.CreatedAt, &v.TenantStatus)
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
