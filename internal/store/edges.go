package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Edge, izleyicilerin yayını aldığı sunucudur. Builtin, kontrol sunucusundaki dağıtımdır
// (.ts dağıtıcısı ve HLS geçidi); diğerleri yönetici tarafından kaydedilen uzak sunuculardır.
type Edge struct {
	ID      int64
	Name    string
	Builtin bool
	// TSBaseURL ve HLSBaseURL, izleyicinin yönlendirildiği dış adreslerdir.
	TSBaseURL  string
	HLSBaseURL string
	// ControlURL, uzak edge'in yönetim adresidir; Key her iki yöndeki isteklerde edge'in kimliğidir.
	ControlURL string
	Key        string
	// PullIP, uzak edge'in origin'den yayın çekerken göründüğü adrestir.
	PullIP     string
	Weight     int
	Enabled    bool
	LastSeenAt *time.Time
	CreatedAt  time.Time
	// Healthy: yönetim API'si sağlık penceresi içinde yanıt vermiş.
	Healthy bool
}

// $1 her kullanımda sağlık penceresidir (saniye).
const edgeSelect = `
	SELECT id, name, builtin, ts_base_url, hls_base_url, control_url, edge_key, pull_ip, weight, enabled,
	       last_seen_at, created_at, coalesce(last_seen_at > now() - make_interval(secs => $1), false)
	FROM edges `

// Edges, tüm edge'leri döner. healthWindow içinde sinyal vermiş olanlar sağlıklı sayılır.
func (s *Store) Edges(ctx context.Context, healthWindow time.Duration) ([]Edge, error) {
	rows, err := s.pool.Query(ctx, edgeSelect+`ORDER BY id`, healthWindow.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Edge])
}

func (s *Store) EdgeByID(ctx context.Context, id int64, healthWindow time.Duration) (Edge, error) {
	rows, err := s.pool.Query(ctx, edgeSelect+`WHERE id = $2`, healthWindow.Seconds(), id)
	if err != nil {
		return Edge{}, err
	}
	e, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Edge])
	return e, notFound(err)
}

// SyncLocalEdge, yerel edge'in izleyici adreslerini ayarlardaki değerlerle eşitler ve numarasını döner.
func (s *Store) SyncLocalEdge(ctx context.Context, tsBaseURL, hlsBaseURL string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `UPDATE edges SET ts_base_url = $1, hls_base_url = $2 WHERE builtin RETURNING id`,
		tsBaseURL, hlsBaseURL).Scan(&id)
	return id, notFound(err)
}

// NewEdge, kaydedilecek uzak edge'dir. BaseURL hem .ts hem HLS için izleyici adresidir.
type NewEdge struct {
	Name       string
	BaseURL    string
	ControlURL string
	Key        string
	PullIP     string
	Weight     int
}

// CreateEdge: ad kullanılıyorsa ErrConflict döner.
func (s *Store) CreateEdge(ctx context.Context, e NewEdge) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO edges (name, ts_base_url, hls_base_url, control_url, edge_key, pull_ip, weight)
		VALUES ($1, $2, $2, $3, $4, $5, $6) RETURNING id`,
		e.Name, e.BaseURL, e.ControlURL, e.Key, e.PullIP, e.Weight).Scan(&id)
	return id, conflict(err)
}

// EdgeUpdate, bir edge'de değiştirilecek alanlardır; nil alanlara dokunulmaz. Yerel edge'in
// adresleri ayarlardan geldiği için BaseURL, ControlURL ve PullIP yerel edge'de yok sayılır.
type EdgeUpdate struct {
	Name       *string
	BaseURL    *string
	ControlURL *string
	PullIP     *string
	Weight     *int
	Enabled    *bool
}

func (s *Store) UpdateEdge(ctx context.Context, id int64, u EdgeUpdate) error {
	return s.one(ctx, `
		UPDATE edges SET
			name         = coalesce($2, name),
			ts_base_url  = CASE WHEN builtin THEN ts_base_url ELSE coalesce($3, ts_base_url) END,
			hls_base_url = CASE WHEN builtin THEN hls_base_url ELSE coalesce($3, hls_base_url) END,
			control_url  = CASE WHEN builtin THEN control_url ELSE coalesce($4, control_url) END,
			pull_ip      = CASE WHEN builtin THEN pull_ip ELSE coalesce($5, pull_ip) END,
			weight       = coalesce($6, weight),
			enabled      = coalesce($7, enabled)
		WHERE id = $1`, id, u.Name, u.BaseURL, u.ControlURL, u.PullIP, u.Weight, u.Enabled)
}

// DeleteEdge, uzak bir edge'i oturum kayıtlarıyla birlikte siler. Yerel edge silinemez (ErrConflict).
func (s *Store) DeleteEdge(ctx context.Context, id int64) error {
	var builtin bool
	err := s.pool.QueryRow(ctx, `SELECT builtin FROM edges WHERE id = $1`, id).Scan(&builtin)
	if err != nil {
		return notFound(err)
	}
	if builtin {
		return ErrConflict
	}
	return s.one(ctx, `DELETE FROM edges WHERE id = $1 AND NOT builtin`, id)
}

// MarkEdgeSeen, edge'in yönetim API'sinin şimdi yanıt verdiğini kaydeder.
func (s *Store) MarkEdgeSeen(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE edges SET last_seen_at = now() WHERE id = $1`, id)
	return err
}

// EdgeSessionCounts, edge başına bağlantı limitinde sayılan oturum sayısını döner.
func (s *Store) EdgeSessionCounts(ctx context.Context, idle time.Duration) (map[int64]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT edge_id, count(*) FROM sessions WHERE `+activeSession+` GROUP BY edge_id`, idle.Seconds())
	if err != nil {
		return nil, err
	}
	counts := map[int64]int{}
	var id int64
	var n int
	_, err = pgx.ForEachRow(rows, []any{&id, &n}, func() error {
		counts[id] = n
		return nil
	})
	return counts, err
}
