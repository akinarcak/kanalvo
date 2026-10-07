package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrConflict: benzersiz olması gereken bir değer (e-posta, kullanıcı adı) zaten kullanılıyor.
var ErrConflict = errors.New("store: değer zaten kullanılıyor")

// conflict, PostgreSQL'in benzersizlik ihlalini ErrConflict'e çevirir.
func conflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}

// --- yöneticiler ---

type Admin struct {
	ID           int64
	Email        string
	PasswordHash string
}

// CreateAdmin: e-posta başka bir yönetici veya yayıncı tarafından kullanılıyorsa ErrConflict döner.
func (s *Store) CreateAdmin(ctx context.Context, email, passwordHash string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO admins (email, password_hash)
		SELECT $1, $2 WHERE NOT EXISTS (SELECT 1 FROM tenants WHERE lower(email) = lower($1))
		RETURNING id`, email, passwordHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	}
	return id, conflict(err)
}

func (s *Store) AdminByEmail(ctx context.Context, email string) (Admin, error) {
	return scanAdmin(s.pool.QueryRow(ctx, `SELECT id, email, password_hash FROM admins WHERE lower(email) = lower($1)`, email))
}

func (s *Store) AdminByID(ctx context.Context, id int64) (Admin, error) {
	return scanAdmin(s.pool.QueryRow(ctx, `SELECT id, email, password_hash FROM admins WHERE id = $1`, id))
}

func scanAdmin(row pgx.Row) (Admin, error) {
	var a Admin
	err := row.Scan(&a.ID, &a.Email, &a.PasswordHash)
	return a, notFound(err)
}

func (s *Store) SetAdminPassword(ctx context.Context, id int64, passwordHash string) error {
	return s.one(ctx, `UPDATE admins SET password_hash = $2 WHERE id = $1`, id, passwordHash)
}

// --- yayıncı hesapları ---

type Tenant struct {
	ID           int64
	Name         string
	Email        string // paneli olmayan yayıncıda boş
	PasswordHash string
	Status       string
	Quotas       Quotas
	CreatedAt    time.Time
}

const tenantSelect = `
	SELECT t.id, t.name, coalesce(t.email, ''), coalesce(t.password_hash, ''), t.status,
	       t.max_channels, t.max_viewers, t.max_connections, t.created_at
	FROM tenants t `

func scanTenant(row pgx.Row) (Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.Name, &t.Email, &t.PasswordHash, &t.Status,
		&t.Quotas.MaxChannels, &t.Quotas.MaxViewers, &t.Quotas.MaxConnections, &t.CreatedAt)
	return t, notFound(err)
}

// CreateTenantAccount, panele giriş yapabilen bir yayıncı oluşturur. E-posta başka bir yayıncı
// veya yönetici tarafından kullanılıyorsa ErrConflict döner.
func (s *Store) CreateTenantAccount(ctx context.Context, name, email, passwordHash string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tenants (name, email, password_hash)
		SELECT $1, $2, $3 WHERE NOT EXISTS (SELECT 1 FROM admins WHERE lower(email) = lower($2))
		RETURNING id`, name, email, passwordHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	}
	return id, conflict(err)
}

func (s *Store) TenantByEmail(ctx context.Context, email string) (Tenant, error) {
	if email == "" {
		return Tenant{}, ErrNotFound
	}
	return scanTenant(s.pool.QueryRow(ctx, tenantSelect+`WHERE lower(t.email) = lower($1)`, email))
}

func (s *Store) TenantByID(ctx context.Context, id int64) (Tenant, error) {
	return scanTenant(s.pool.QueryRow(ctx, tenantSelect+`WHERE t.id = $1`, id))
}

// TenantProfile, yayıncının adı ve panel e-postasıdır.
type TenantProfile struct {
	Name  string
	Email string
}

// TenantUpdate, bir yayıncıda değiştirilecek alanlardır; nil olanlara dokunulmaz.
type TenantUpdate struct {
	Profile *TenantProfile
	Quotas  *Quotas
	Status  *string
}

// UpdateTenant, verilen alanları tek işlemde değiştirir: biri uygulanamazsa hiçbiri uygulanmaz.
// E-posta başka bir hesapta kullanılıyorsa ErrConflict döner. Askıya alınan yayıncının panel
// oturumları da aynı işlemde kapanır.
func (s *Store) UpdateTenant(ctx context.Context, id int64, u TenantUpdate) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		// Satırı kilitlemek varlığını da doğrular.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM tenants WHERE id = $1 FOR UPDATE`, id).Scan(&exists); err != nil {
			return notFound(err)
		}
		if u.Profile != nil {
			tag, err := tx.Exec(ctx, `
				UPDATE tenants SET name = $2, email = $3
				WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM admins WHERE lower(email) = lower($3))`, id, u.Profile.Name, u.Profile.Email)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrConflict // e-posta bir yöneticiye ait
			}
		}
		if u.Quotas != nil {
			if _, err := tx.Exec(ctx, `UPDATE tenants SET max_channels = $2, max_viewers = $3, max_connections = $4 WHERE id = $1`,
				id, u.Quotas.MaxChannels, u.Quotas.MaxViewers, u.Quotas.MaxConnections); err != nil {
				return err
			}
		}
		if u.Status != nil {
			if _, err := tx.Exec(ctx, `UPDATE tenants SET status = $2 WHERE id = $1`, id, *u.Status); err != nil {
				return err
			}
			if *u.Status != "active" {
				if _, err := tx.Exec(ctx, `DELETE FROM panel_sessions WHERE role = 'tenant' AND tenant_id = $1`, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) SetTenantPassword(ctx context.Context, id int64, passwordHash string) error {
	return s.one(ctx, `UPDATE tenants SET password_hash = $2 WHERE id = $1`, id, passwordHash)
}

// TenantSummary, yönetici listesindeki bir yayıncı satırıdır. Şifre özeti taşımaz.
type TenantSummary struct {
	Tenant
	Channels       int
	Viewers        int
	LiveChannels   int
	ActiveSessions int
}

func (s *Store) ListTenants(ctx context.Context, idle time.Duration) ([]TenantSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.name, coalesce(t.email, ''), t.status, t.max_channels, t.max_viewers, t.max_connections, t.created_at,
		       (SELECT count(*) FROM channels c WHERE c.tenant_id = t.id),
		       (SELECT count(*) FROM viewers v WHERE v.tenant_id = t.id),
		       (SELECT count(*) FROM channels c WHERE c.tenant_id = t.id AND c.live),
		       (SELECT count(*) FROM sessions WHERE tenant_id = t.id AND `+activeSession+`)
		FROM tenants t ORDER BY t.id`, idle.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TenantSummary, error) {
		var t TenantSummary
		err := row.Scan(&t.ID, &t.Name, &t.Email, &t.Status, &t.Quotas.MaxChannels, &t.Quotas.MaxViewers, &t.Quotas.MaxConnections,
			&t.CreatedAt, &t.Channels, &t.Viewers, &t.LiveChannels, &t.ActiveSessions)
		return t, err
	})
}

type PlatformStats struct {
	Tenants        int
	Channels       int
	LiveChannels   int
	Viewers        int
	ActiveSessions int
}

func (s *Store) PlatformStats(ctx context.Context, idle time.Duration) (PlatformStats, error) {
	var p PlatformStats
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM tenants),
		       (SELECT count(*) FROM channels),
		       (SELECT count(*) FROM channels WHERE live),
		       (SELECT count(*) FROM viewers),
		       (SELECT count(*) FROM sessions WHERE `+activeSession+`)`, idle.Seconds()).
		Scan(&p.Tenants, &p.Channels, &p.LiveChannels, &p.Viewers, &p.ActiveSessions)
	return p, err
}

// Usage, bir yayıncının kotalarına karşılık gelen şimdiki kullanımıdır.
type Usage struct {
	Channels    int
	Viewers     int
	Connections int
}

func (s *Store) TenantUsage(ctx context.Context, tenantID int64, idle time.Duration) (Usage, error) {
	var u Usage
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM channels WHERE tenant_id = $2),
		       (SELECT count(*) FROM viewers WHERE tenant_id = $2),
		       (SELECT count(*) FROM sessions WHERE tenant_id = $2 AND `+activeSession+`)`, idle.Seconds(), tenantID).
		Scan(&u.Channels, &u.Viewers, &u.Connections)
	return u, err
}

// --- panel oturumları ---

type PanelSession struct {
	Role      string // "admin" veya "tenant"
	SubjectID int64
}

func (s *Store) CreatePanelSession(ctx context.Context, tokenHash []byte, role string, subjectID int64, ttl time.Duration) error {
	var adminID, tenantID *int64
	if role == "admin" {
		adminID = &subjectID
	} else {
		tenantID = &subjectID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO panel_sessions (token_hash, role, admin_id, tenant_id, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))`, tokenHash, role, adminID, tenantID, ttl.Seconds())
	return err
}

// PanelSessionByHash, süresi dolmamış oturumu döner; yoksa ErrNotFound.
func (s *Store) PanelSessionByHash(ctx context.Context, tokenHash []byte) (PanelSession, error) {
	var p PanelSession
	err := s.pool.QueryRow(ctx, `
		SELECT role, coalesce(admin_id, tenant_id) FROM panel_sessions
		WHERE token_hash = $1 AND expires_at > now()`, tokenHash).Scan(&p.Role, &p.SubjectID)
	return p, notFound(err)
}

func (s *Store) DeletePanelSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM panel_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeletePanelSessionsOf, bir hesabın except dışındaki tüm oturumlarını kapatır (şifre değişince).
func (s *Store) DeletePanelSessionsOf(ctx context.Context, role string, subjectID int64, except []byte) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM panel_sessions
		WHERE role = $1 AND coalesce(admin_id, tenant_id) = $2 AND token_hash IS DISTINCT FROM $3`, role, subjectID, except)
	return err
}

func (s *Store) DeleteExpiredPanelSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM panel_sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}

// one, tam bir satırı etkilemesi gereken bir güncellemeyi çalıştırır; satır yoksa ErrNotFound döner.
func (s *Store) one(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return conflict(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
