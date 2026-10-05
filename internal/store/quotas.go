package store

import (
	"context"
	"errors"
)

// ErrQuotaExceeded: yayıncı, kotasının izin verdiğinden fazla kanal veya izleyici oluşturmaya çalıştı.
var ErrQuotaExceeded = errors.New("store: kota aşıldı")

// Quotas, bir yayıncının sınırlarıdır.
type Quotas struct {
	MaxChannels    int
	MaxViewers     int
	MaxConnections int // tüm izleyicilerinin toplam eşzamanlı bağlantısı
}

func (s *Store) TenantQuotas(ctx context.Context, tenantID int64) (Quotas, error) {
	var q Quotas
	err := s.pool.QueryRow(ctx, `SELECT max_channels, max_viewers, max_connections FROM tenants WHERE id = $1`, tenantID).
		Scan(&q.MaxChannels, &q.MaxViewers, &q.MaxConnections)
	return q, notFound(err)
}

func (s *Store) SetTenantQuotas(ctx context.Context, tenantID int64, q Quotas) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tenants SET max_channels = $2, max_viewers = $3, max_connections = $4 WHERE id = $1`,
		tenantID, q.MaxChannels, q.MaxViewers, q.MaxConnections)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// insertWithinQuota, yayıncı satırını kilitleyip mevcut kayıt sayısını kotayla karşılaştırır ve
// yer varsa insert sorgusunu çalıştırır. table ve column yalnızca bu paketteki sabitlerden gelir.
func (s *Store) insertWithinQuota(ctx context.Context, tenantID int64, table, column, insert string, args ...any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var limit, count int
	if err := tx.QueryRow(ctx, `SELECT `+column+` FROM tenants WHERE id = $1 FOR UPDATE`, tenantID).Scan(&limit); err != nil {
		return 0, notFound(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, tenantID).Scan(&count); err != nil {
		return 0, err
	}
	if count >= limit {
		return 0, ErrQuotaExceeded
	}
	var id int64
	if err := tx.QueryRow(ctx, insert, args...).Scan(&id); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
