package store

import "time"

type Category struct {
	ID       int64
	TenantID int64
	Name     string
	Position int
}

type Channel struct {
	ID           int64
	TenantID     int64
	Name         string
	StreamSecret string
	Live         bool
	CategoryID   *int64
	LogoURL      string
	CreatedAt    time.Time
	TenantStatus string
}

type Viewer struct {
	ID             int64
	TenantID       int64
	Username       string
	Password       string
	Status         string
	ExpiresAt      *time.Time
	MaxConnections int
	CreatedAt      time.Time
	TenantStatus   string
}

// Usable, izleyicinin şu an yayın izleyip izleyemeyeceğini söyler.
func (v Viewer) Usable(now time.Time) bool {
	if v.Status != "active" || v.TenantStatus != "active" {
		return false
	}
	return v.ExpiresAt == nil || now.Before(*v.ExpiresAt)
}
