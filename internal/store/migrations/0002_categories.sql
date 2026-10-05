CREATE TABLE categories (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  BIGINT NOT NULL REFERENCES tenants (id),
    name       TEXT NOT NULL,
    position   INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX categories_tenant_idx ON categories (tenant_id);

ALTER TABLE channels
    ADD COLUMN category_id BIGINT REFERENCES categories (id) ON DELETE SET NULL,
    ADD COLUMN logo_url    TEXT NOT NULL DEFAULT '';
