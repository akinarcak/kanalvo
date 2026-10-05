CREATE TABLE tenants (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE channels (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id           BIGINT NOT NULL REFERENCES tenants (id),
    name                TEXT NOT NULL,
    stream_secret       TEXT NOT NULL,
    live                BOOLEAN NOT NULL DEFAULT false,
    publisher_client_id TEXT,
    last_publish_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX channels_tenant_idx ON channels (tenant_id);

CREATE TABLE viewers (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       BIGINT NOT NULL REFERENCES tenants (id),
    username        TEXT NOT NULL UNIQUE,
    password        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    expires_at      TIMESTAMPTZ,
    max_connections INT NOT NULL DEFAULT 1 CHECK (max_connections > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX viewers_tenant_idx ON viewers (tenant_id);
