-- Kurulum kodu: yöneticinin panelden aldığı, yeni bir sunucunun kendini kaydetmesini sağlayan tek
-- kullanımlık koddur. Kodun kendisi saklanmaz, yalnızca özeti (token_hash) saklanır.
--   used_at : kodun kullanıldığı an; kullanılan kod bir daha geçmez
--   edge_id : kodla kaydolan sunucu (sunucu silinirse boşalır)
CREATE TABLE edge_enrollments (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    edge_id    BIGINT REFERENCES edges (id) ON DELETE SET NULL
);
