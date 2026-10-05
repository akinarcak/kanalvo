-- Panel hesapları: platform yöneticileri ve yayıncılar e-posta ve şifreyle giriş yapar.
CREATE TABLE admins (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admins_email_idx ON admins (lower(email));

-- Paneli olmayan yayıncılar (ör. seed-dev ile oluşturulanlar) için boş kalır.
ALTER TABLE tenants
    ADD COLUMN email         TEXT,
    ADD COLUMN password_hash TEXT;
CREATE UNIQUE INDEX tenants_email_idx ON tenants (lower(email)) WHERE email IS NOT NULL;

-- Panel oturumu: tarayıcıdaki çerezin SHA-256 özeti saklanır, çerezin kendisi saklanmaz.
CREATE TABLE panel_sessions (
    token_hash BYTEA PRIMARY KEY,
    role       TEXT NOT NULL CHECK (role IN ('admin', 'tenant')),
    admin_id   BIGINT REFERENCES admins (id) ON DELETE CASCADE,
    tenant_id  BIGINT REFERENCES tenants (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    CHECK ((role = 'admin') = (admin_id IS NOT NULL)),
    CHECK ((role = 'tenant') = (tenant_id IS NOT NULL))
);
CREATE INDEX panel_sessions_expires_idx ON panel_sessions (expires_at);

-- Kanal silinince kategori atamaları ve oturumlar zaten temizleniyor; izleyici silinince oturumları da.
-- Kanal veya izleyici bir yayıncıya aitken yayıncı silinemez (yayıncılar silinmez, askıya alınır).
