-- Bir HLS oturumunun kimliği yalnızca oturum anahtarıdır; istemci adresi oturumun o anki
-- konumudur. Böylece yerinden edilen bir adres hiçbir ağdan geri dönemez ve ağ değiştiren
-- izleyici aynı oturumla devam eder. Oturumlar geçicidir; eski kayıtlar silinir.
DELETE FROM sessions;

DROP INDEX sessions_identity_idx;
CREATE UNIQUE INDEX sessions_identity_idx ON sessions (kind, session_key);

-- ip_changed_at: oturumun başka bir ağa en son taşındığı an; hiç taşınmadıysa boştur. Bir taşınmadan
-- sonra kısa süre içinde yeniden taşınamaz; aksi halde aynı adresi paylaşan iki kişi sırayla izleyebilirdi.
ALTER TABLE sessions ADD COLUMN ip_changed_at TIMESTAMPTZ;

-- Limit denetimleri yalnızca sonlandırılmamış oturumlara bakar; sonlandırılmış kayıtlar imza
-- ömrü boyunca saklandığı için tablo onlarla dolabilir.
DROP INDEX sessions_viewer_idx;
DROP INDEX sessions_tenant_idx;
CREATE INDEX sessions_viewer_active_idx ON sessions (viewer_id) WHERE NOT revoked;
CREATE INDEX sessions_tenant_active_idx ON sessions (tenant_id) WHERE NOT revoked;
