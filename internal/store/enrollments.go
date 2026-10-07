package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// EdgeEnrollment, bir kurulum kodunun durumudur. EdgeID, kodla kaydolan sunucudur; kod henüz
// kullanılmadıysa (ya da sunucu sonradan silindiyse) nil'dir.
type EdgeEnrollment struct {
	ID        int64
	ExpiresAt time.Time
	UsedAt    *time.Time
	EdgeID    *int64
	// Expired: kod kullanılmadan süresi dolmuş.
	Expired bool
}

// enrollmentRetention: kullanılmış ya da süresi dolmuş kodların kaydı bu süre sonra silinir.
const enrollmentRetention = 24 * time.Hour

func enrollmentHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateEdgeEnrollment, ttl süresince geçerli bir kurulum kodu kaydeder. Eski kayıtları da temizler.
func (s *Store) CreateEdgeEnrollment(ctx context.Context, token string, ttl time.Duration) (int64, error) {
	if _, err := s.pool.Exec(ctx, `DELETE FROM edge_enrollments WHERE expires_at < now() - make_interval(secs => $1)`,
		enrollmentRetention.Seconds()); err != nil {
		return 0, err
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO edge_enrollments (token_hash, expires_at) VALUES ($1, now() + make_interval(secs => $2)) RETURNING id`,
		enrollmentHash(token), ttl.Seconds()).Scan(&id)
	return id, err
}

func (s *Store) EdgeEnrollmentByID(ctx context.Context, id int64) (EdgeEnrollment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, expires_at, used_at, edge_id, used_at IS NULL AND expires_at <= now()
		FROM edge_enrollments WHERE id = $1`, id)
	if err != nil {
		return EdgeEnrollment{}, err
	}
	e, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[EdgeEnrollment])
	return e, notFound(err)
}

// EdgeEnrollmentOpen: kod geçerli, kullanılmamış ve süresi dolmamışsa true döner.
func (s *Store) EdgeEnrollmentOpen(ctx context.Context, token string) (bool, error) {
	var open bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM edge_enrollments WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now())`,
		enrollmentHash(token)).Scan(&open)
	return open, err
}

// RedeemEdgeEnrollment, kodu kullanır ve e sunucusunu devre dışı olarak kaydeder: sunucu, yönetici
// bilgilerini onaylayıp etkinleştirene kadar izleyici almaz. Kod geçersiz, kullanılmış ya da süresi
// dolmuşsa ErrNotFound döner. e.Name kullanılıyorsa sonuna kayıt numarası eklenir.
func (s *Store) RedeemEdgeEnrollment(ctx context.Context, token string, e NewEdge) (int64, error) {
	var edgeID int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var enrollmentID int64
		err := tx.QueryRow(ctx, `
			SELECT id FROM edge_enrollments
			WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now() FOR UPDATE`, enrollmentHash(token)).Scan(&enrollmentID)
		if err != nil {
			return notFound(err)
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM edges WHERE name = $1)`, e.Name).Scan(&taken); err != nil {
			return err
		}
		name := e.Name
		if taken {
			name = fmt.Sprintf("%s (%d)", e.Name, enrollmentID)
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO edges (name, ts_base_url, hls_base_url, control_url, edge_key, pull_ip, weight, enabled)
			VALUES ($1, $2, $2, $3, $4, $5, $6, false) RETURNING id`,
			name, e.BaseURL, e.ControlURL, e.Key, e.PullIP, e.Weight).Scan(&edgeID)
		if err != nil {
			return conflict(err)
		}
		_, err = tx.Exec(ctx, `UPDATE edge_enrollments SET used_at = now(), edge_id = $2 WHERE id = $1`, enrollmentID, edgeID)
		return err
	})
	return edgeID, err
}
