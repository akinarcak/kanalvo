ALTER TABLE tenants
    ADD COLUMN max_channels    INT NOT NULL DEFAULT 10 CHECK (max_channels >= 0),
    ADD COLUMN max_viewers     INT NOT NULL DEFAULT 100 CHECK (max_viewers >= 0),
    ADD COLUMN max_connections INT NOT NULL DEFAULT 100 CHECK (max_connections >= 0);

-- Bir oturum, bir izleyicinin süren tek bir izlemesidir.
--   ts : session_key SRS'in bağlantı kimliğidir; SRS "izleme bitti" deyince silinir.
--   hls: session_key imzadaki oturum anahtarıdır; istek geldikçe last_seen_at güncellenir.
-- revoked: bağlantı limiti yüzünden yerini daha yeni bir oturuma bırakmış oturum. Satır,
-- aynı adresle yeniden izlenememesi için silinmez.
CREATE TABLE sessions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    BIGINT NOT NULL REFERENCES tenants (id),
    viewer_id    BIGINT NOT NULL REFERENCES viewers (id) ON DELETE CASCADE,
    channel_id   BIGINT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('ts', 'hls')),
    session_key  TEXT NOT NULL,
    ip           TEXT NOT NULL,
    revoked      BOOLEAN NOT NULL DEFAULT false,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX sessions_identity_idx ON sessions (kind, session_key, ip);
CREATE INDEX sessions_viewer_idx ON sessions (viewer_id);
CREATE INDEX sessions_tenant_idx ON sessions (tenant_id);
