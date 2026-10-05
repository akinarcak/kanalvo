-- Kesilmesi gereken SRS bağlantıları. Bir kanal veya izleyici silinirken bağlantı kimlikleri
-- (oturum kayıtları ve yayıncı kimliği) kayıtla birlikte yok olur; kesme isteği o an başarısız
-- olursa yeniden denenebilmesi için buraya aynı işlem içinde yazılır.
--   target 'origin': yayıncı bağlantısı; 'ts': .ts dağıtıcısındaki izleyici bağlantısı.
CREATE TABLE pending_kicks (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    target     TEXT NOT NULL CHECK (target IN ('origin', 'ts')),
    client_id  TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (target, client_id)
);
