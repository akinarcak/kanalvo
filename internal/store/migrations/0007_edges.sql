-- Edge, izleyicilerin yayını aldığı sunucudur. Kontrol sunucusundaki dağıtım (.ts dağıtıcısı ve
-- HLS geçidi) "yerel" edge'dir: bu göçle oluşur, silinemez ve adresleri ayarlardan gelir.
--   control_url : uzak edge'in yönetim adresi (bağlantı listesi ve bağlantı kesme)
--   edge_key    : uzak edge'in kimliği; edge'den gelen ve edge'e giden isteklerde taşınır
--   pull_ip     : uzak edge'in origin'den yayın çekerken göründüğü adres
--   last_seen_at: yönetim API'sinin en son yanıt verdiği an (sağlık sinyali)
CREATE TABLE edges (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    builtin      BOOLEAN NOT NULL DEFAULT false,
    ts_base_url  TEXT NOT NULL DEFAULT '',
    hls_base_url TEXT NOT NULL DEFAULT '',
    control_url  TEXT NOT NULL DEFAULT '',
    edge_key     TEXT NOT NULL DEFAULT '',
    pull_ip      TEXT NOT NULL DEFAULT '',
    weight       INT NOT NULL DEFAULT 100 CHECK (weight BETWEEN 1 AND 1000),
    enabled      BOOLEAN NOT NULL DEFAULT true,
    last_seen_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (builtin OR edge_key <> '')
);
CREATE UNIQUE INDEX edges_builtin_idx ON edges (builtin) WHERE builtin;
CREATE UNIQUE INDEX edges_key_idx ON edges (edge_key) WHERE edge_key <> '';

INSERT INTO edges (name, builtin) VALUES ('Yerel', true);

-- Var olan oturumlar ve bekleyen kesmeler yerel edge'e aittir. Silinmezler: süren .ts izlemeleri
-- izlenmeye devam etmeli, sonlandırılmış HLS adresleri kapalı kalmalı, bekleyen kesmeler kaybolmamalıdır.
--
-- SRS bağlantı kimlikleri yalnızca kendi SRS'inde tekildir: .ts oturumunun kimliği edge ile
-- birlikte bağlantı kimliğidir. HLS oturumunun kimliği yine yalnızca imzadaki anahtardır.
ALTER TABLE sessions ADD COLUMN edge_id BIGINT REFERENCES edges (id) ON DELETE CASCADE;
UPDATE sessions SET edge_id = (SELECT id FROM edges WHERE builtin);
ALTER TABLE sessions ALTER COLUMN edge_id SET NOT NULL;
DROP INDEX sessions_identity_idx;
CREATE UNIQUE INDEX sessions_hls_identity_idx ON sessions (session_key) WHERE kind = 'hls';
CREATE UNIQUE INDEX sessions_ts_identity_idx ON sessions (edge_id, session_key) WHERE kind = 'ts';

-- 'ts' kesmeleri bir edge'e aittir; 'origin' kesmelerinin edge'i yoktur.
ALTER TABLE pending_kicks
    ADD COLUMN edge_id BIGINT REFERENCES edges (id) ON DELETE CASCADE,
    DROP CONSTRAINT pending_kicks_target_client_id_key;
UPDATE pending_kicks SET edge_id = (SELECT id FROM edges WHERE builtin) WHERE target = 'ts';
ALTER TABLE pending_kicks ADD CHECK ((target = 'ts') = (edge_id IS NOT NULL));
CREATE UNIQUE INDEX pending_kicks_identity_idx ON pending_kicks (target, coalesce(edge_id, 0), client_id);
