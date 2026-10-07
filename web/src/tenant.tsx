import { type FormEvent, useEffect, useState } from "react";
import { type Category, type Channel, type Overview, type Session, type Viewer, api } from "./api";
import { Badge, CopyField, ErrorNote, Loading, Meter, Pager, Panel, formatDate, fromLocalInput, pageSize, toLocalInput, useAction, useLoad } from "./ui";

// --- Genel bakış ---

export function OverviewPage() {
  const overview = useLoad(() => api.get<Overview>("/api/tenant/overview"), 10000);
  if (!overview.data) return <Loading error={overview.error} />;
  const o = overview.data;
  return (
    <>
      <Panel title="Kullanım">
        <div className="meters">
          <Meter label="Kanal" used={o.usage.channels} limit={o.quotas.max_channels} />
          <Meter label="İzleyici hesabı" used={o.usage.viewers} limit={o.quotas.max_viewers} />
          <Meter label="Süren izleme" used={o.usage.connections} limit={o.quotas.max_connections} />
        </div>
        <p className="muted small">Kotaları platform yöneticisi belirler.</p>
      </Panel>
      <Panel title="Bağlantı bilgileri">
        <CopyField label="OBS yayın sunucusu" value={o.ingest_url} />
        <CopyField label="İzleyicilerin oynatıcıya yazacağı sunucu" value={o.xtream_url} />
        <p className="muted small">
          Yayın anahtarı her kanal için ayrıdır; Kanallar sayfasında "OBS ayarları" ile görebilirsiniz. İzleyiciler, IPTV oynatıcısında
          "Xtream Codes" girişini seçip bu sunucu adresini ve kendi kullanıcı adı ile şifrelerini yazar.
        </p>
      </Panel>
    </>
  );
}

// --- Kategoriler ---

export function Categories() {
  const list = useLoad(() => api.get<Category[]>("/api/tenant/categories"));
  const [name, setName] = useState("");
  const action = useAction();

  const add = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      await api.post("/api/tenant/categories", { name });
      setName("");
      await list.reload();
    });
  };
  const rename = (c: Category) => {
    const next = window.prompt("Kategorinin yeni adı:", c.name);
    if (!next || next === c.name) return;
    void action.run(async () => {
      await api.patch(`/api/tenant/categories/${c.id}`, { name: next });
      await list.reload();
    });
  };
  const remove = (c: Category) => {
    if (!window.confirm(`"${c.name}" silinsin mi? Bu kategorideki kanallar "Genel" altında görünür.`)) return;
    void action.run(async () => {
      await api.del(`/api/tenant/categories/${c.id}`);
      await list.reload();
    });
  };

  return (
    <>
      <Panel title="Yeni kategori">
        <form className="row" onSubmit={add}>
          <label>
            Ad
            <input required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <button type="submit" disabled={action.busy}>
            Kategori ekle
          </button>
        </form>
        <ErrorNote message={action.error} />
      </Panel>
      <Panel title="Kategoriler">
        {!list.data ? (
          <Loading error={list.error} />
        ) : list.data.length === 0 ? (
          <p className="muted">Henüz kategori yok. Kategorisiz kanallar oynatıcıda "Genel" altında görünür.</p>
        ) : (
          <div className="table-wrap">
            <table>
              <tbody>
                {list.data.map((c) => (
                  <tr key={c.id}>
                    <td>{c.name}</td>
                    <td className="actions">
                      <button type="button" className="ghost" disabled={action.busy} onClick={() => rename(c)}>
                        Yeniden adlandır
                      </button>
                      <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => remove(c)}>
                        Sil
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </>
  );
}

// --- Kanallar ---

export function Channels() {
  const channels = useLoad(() => api.get<Channel[]>("/api/tenant/channels"), 5000);
  const categories = useLoad(() => api.get<Category[]>("/api/tenant/categories"));
  const [opened, setOpened] = useState<{ id: number; mode: "obs" | "edit" } | null>(null);
  const action = useAction();
  const cats = categories.data ?? [];
  const current = channels.data?.find((c) => c.id === opened?.id) ?? null;

  const remove = (c: Channel) => {
    const warning = c.live ? " Kanal şu an yayında; yayın ve izlemeler kesilir." : "";
    if (!window.confirm(`"${c.name}" silinsin mi?${warning}`)) return;
    void action.run(async () => {
      await api.del(`/api/tenant/channels/${c.id}`);
      setOpened(null);
      await channels.reload();
    });
  };

  return (
    <>
      <ChannelForm title="Yeni kanal" categories={cats} submitLabel="Kanal ekle" onSaved={() => void channels.reload()} />

      <Panel title="Kanallar">
        <ErrorNote message={action.error} />
        {!channels.data ? (
          <Loading error={channels.error} />
        ) : channels.data.length === 0 ? (
          <p className="muted">Henüz kanal yok. Yukarıdaki formla bir kanal ekleyin, sonra OBS ayarlarını kopyalayın.</p>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>No</th>
                  <th>Ad</th>
                  <th>Kategori</th>
                  <th>Durum</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {channels.data.map((c) => (
                  <tr key={c.id} className={c.id === opened?.id ? "selected" : ""}>
                    <td className="muted">{c.id}</td>
                    <td>{c.name}</td>
                    <td>{cats.find((k) => k.id === c.category_id)?.name ?? <span className="muted">Genel</span>}</td>
                    <td>{c.live ? <Badge tone="ok">Yayında</Badge> : <Badge tone="off">Çevrimdışı</Badge>}</td>
                    <td className="actions">
                      <button type="button" className="ghost" onClick={() => setOpened({ id: c.id, mode: "obs" })}>
                        OBS ayarları
                      </button>
                      <button type="button" className="ghost" onClick={() => setOpened({ id: c.id, mode: "edit" })}>
                        Düzenle
                      </button>
                      <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => remove(c)}>
                        Sil
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      {current && opened?.mode === "obs" && (
        <ObsSettings channel={current} onClose={() => setOpened(null)} onChanged={() => void channels.reload()} />
      )}
      {current && opened?.mode === "edit" && (
        <ChannelForm
          key={current.id}
          title={`Kanalı düzenle: ${current.name}`}
          channel={current}
          categories={cats}
          submitLabel="Kaydet"
          onClose={() => setOpened(null)}
          onSaved={() => {
            setOpened(null);
            void channels.reload();
          }}
        />
      )}
    </>
  );
}

function ChannelForm({
  title,
  channel,
  categories,
  submitLabel,
  onSaved,
  onClose,
}: {
  title: string;
  channel?: Channel;
  categories: Category[];
  submitLabel: string;
  onSaved: () => void;
  onClose?: () => void;
}) {
  const [name, setName] = useState(channel?.name ?? "");
  const [categoryId, setCategoryId] = useState<number | null>(channel?.category_id ?? null);
  const [logoUrl, setLogoUrl] = useState(channel?.logo_url ?? "");
  const action = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      const body = { name, category_id: categoryId, logo_url: logoUrl.trim() };
      if (channel) await api.patch(`/api/tenant/channels/${channel.id}`, body);
      else await api.post("/api/tenant/channels", body);
      if (!channel) {
        setName("");
        setLogoUrl("");
      }
      onSaved();
    });
  };

  return (
    <Panel title={title} onClose={onClose}>
      <form className="row" onSubmit={submit}>
        <label>
          Ad
          <input required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          Kategori
          <select value={categoryId ?? ""} onChange={(e) => setCategoryId(e.target.value ? Number(e.target.value) : null)}>
            <option value="">Genel (kategorisiz)</option>
            {categories.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          Logo adresi (isteğe bağlı)
          <input type="url" maxLength={500} placeholder="https://…" value={logoUrl} onChange={(e) => setLogoUrl(e.target.value)} />
        </label>
        <button type="submit" disabled={action.busy}>
          {submitLabel}
        </button>
      </form>
      <ErrorNote message={action.error} />
    </Panel>
  );
}

function ObsSettings({ channel, onClose, onChanged }: { channel: Channel; onClose: () => void; onChanged: () => void }) {
  const action = useAction();
  const regenerate = () => {
    const warning = channel.live ? " Kanal şu an yayında; yayın kesilir." : "";
    if (!window.confirm(`Yayın anahtarı yenilensin mi? OBS'teki eski anahtar artık çalışmaz.${warning}`)) return;
    void action.run(async () => {
      await api.post(`/api/tenant/channels/${channel.id}/regenerate-secret`);
      onChanged();
    });
  };
  return (
    <Panel title={`OBS ayarları: ${channel.name}`} onClose={onClose}>
      <p className="muted small">OBS → Ayarlar → Yayın → Hizmet: "Özel". Aşağıdaki iki değeri yapıştırın.</p>
      <CopyField label="Sunucu" value={channel.ingest_url} />
      <CopyField label="Yayın anahtarı" value={channel.stream_key} secret />
      <p className="muted small">Yayın anahtarı gizlidir: ele geçiren kişi bu kanala yayın açabilir.</p>
      <p className="muted small">
        Gecikmeyi düşürmek için: OBS → Ayarlar → Çıkış → Çıkış kipi "Gelişmiş" → Yayın sekmesinde "Anahtar kare aralığı" 2 sn. Varsayılan
        ayarda bu aralık 8 saniyeyi bulur; kanalı açan izleyici o kadar geriden başlar ve kanal geç açılır.
      </p>
      <ErrorNote message={action.error} />
      <button type="button" className="ghost" disabled={action.busy} onClick={regenerate}>
        Anahtarı yenile
      </button>
    </Panel>
  );
}

// --- İzleyiciler ---

export function Viewers() {
  // Liste dilim dilim gelir (en yeni önce); arama kullanıcı adında yapılır.
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);
  const viewers = useLoad(() => api.page<Viewer>("/api/tenant/viewers", { q: search.trim(), limit: pageSize, offset }));
  const overview = useLoad(() => api.get<Overview>("/api/tenant/overview"));
  const [opened, setOpened] = useState<{ id: number; mode: "info" | "edit" } | null>(null);
  const action = useAction();
  const current = viewers.data?.items.find((v) => v.id === opened?.id) ?? null;
  const reload = () => void viewers.reload();
  useEffect(() => {
    // Yazarken her tuşta istek gitmesin diye kısa bir bekleme.
    const timer = window.setTimeout(() => void viewers.reload(), 250);
    return () => window.clearTimeout(timer);
  }, [search, offset]);
  // Silme sonrası boşalan son sayfada kalınmaz.
  useEffect(() => {
    if (viewers.data && viewers.data.items.length === 0 && offset > 0) setOffset(Math.max(0, offset - pageSize));
  }, [viewers.data]);

  const toggle = (v: Viewer) => {
    const status = v.status === "active" ? "suspended" : "active";
    if (status === "suspended" && !window.confirm(`"${v.username}" askıya alınsın mı? Süren izlemesi birkaç saniye içinde kesilir.`)) return;
    void action.run(async () => {
      await api.patch(`/api/tenant/viewers/${v.id}`, { status });
      await viewers.reload();
    });
  };
  const remove = (v: Viewer) => {
    if (!window.confirm(`"${v.username}" silinsin mi? Süren izlemesi kesilir ve hesap geri getirilemez.`)) return;
    void action.run(async () => {
      await api.del(`/api/tenant/viewers/${v.id}`);
      setOpened(null);
      await viewers.reload();
    });
  };

  return (
    <>
      <CreateViewer
        onCreated={(v) => {
          // Yeni izleyici listenin başındadır; görünmesi için arama ve sayfa sıfırlanır.
          setOpened({ id: v.id, mode: "info" });
          setSearch("");
          setOffset(0);
          reload();
        }}
      />

      <Panel title="İzleyiciler">
        <ErrorNote message={action.error} />
        <div className="list-search">
          <input
            type="search"
            placeholder="Kullanıcı adında ara"
            aria-label="Kullanıcı adında ara"
            value={search}
            maxLength={64}
            onChange={(e) => (setSearch(e.target.value), setOffset(0))}
          />
        </div>
        {!viewers.data ? (
          <Loading error={viewers.error} />
        ) : viewers.data.items.length === 0 ? (
          <p className="muted">
            {search.trim() ? "Aramayla eşleşen izleyici yok." : "Henüz izleyici yok. Yukarıdaki formla bir izleyici hesabı oluşturun."}
          </p>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Kullanıcı adı</th>
                  <th>Durum</th>
                  <th>Bitiş</th>
                  <th className="num">Bağlantı limiti</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {viewers.data.items.map((v) => {
                  const expired = v.expires_at !== null && new Date(v.expires_at) <= new Date();
                  return (
                    <tr key={v.id} className={v.id === opened?.id ? "selected" : ""}>
                      <td>{v.username}</td>
                      <td>
                        {v.status === "suspended" ? (
                          <Badge tone="warn">Askıda</Badge>
                        ) : expired ? (
                          <Badge tone="warn">Süresi doldu</Badge>
                        ) : (
                          <Badge tone="ok">Etkin</Badge>
                        )}
                      </td>
                      <td>{formatDate(v.expires_at)}</td>
                      <td className="num">{v.max_connections}</td>
                      <td className="actions">
                        <button type="button" className="ghost" onClick={() => setOpened({ id: v.id, mode: "info" })}>
                          Giriş bilgileri
                        </button>
                        <button type="button" className="ghost" onClick={() => setOpened({ id: v.id, mode: "edit" })}>
                          Düzenle
                        </button>
                        <button type="button" className="ghost" disabled={action.busy} onClick={() => toggle(v)}>
                          {v.status === "active" ? "Askıya al" : "Etkinleştir"}
                        </button>
                        <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => remove(v)}>
                          Sil
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {viewers.data && <Pager offset={offset} shown={viewers.data.items.length} total={viewers.data.total} onMove={setOffset} />}
      </Panel>

      {current && opened?.mode === "info" && (
        <ViewerInfo viewer={current} server={overview.data?.xtream_url ?? ""} onClose={() => setOpened(null)} onChanged={reload} />
      )}
      {current && opened?.mode === "edit" && (
        <EditViewer
          key={current.id}
          viewer={current}
          onClose={() => setOpened(null)}
          onSaved={() => {
            setOpened(null);
            reload();
          }}
        />
      )}
    </>
  );
}

function CreateViewer({ onCreated }: { onCreated: (v: Viewer) => void }) {
  const [username, setUsername] = useState("");
  const [maxConnections, setMaxConnections] = useState(1);
  const [expires, setExpires] = useState("");
  const action = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      const v = await api.post<Viewer>("/api/tenant/viewers", {
        username,
        max_connections: maxConnections,
        expires_at: fromLocalInput(expires),
      });
      setUsername("");
      setExpires("");
      onCreated(v);
    });
  };

  return (
    <Panel title="Yeni izleyici">
      <form className="row" onSubmit={submit}>
        <label>
          Kullanıcı adı
          <input
            required
            minLength={3}
            maxLength={32}
            pattern="[A-Za-z0-9._\-]+"
            title="Harf, rakam, nokta, alt çizgi ve tire"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </label>
        <label>
          Bağlantı limiti
          <input type="number" min={1} max={100} required value={maxConnections} onChange={(e) => setMaxConnections(Number(e.target.value))} />
        </label>
        <label>
          Bitiş tarihi (boşsa süresiz)
          <input type="datetime-local" value={expires} onChange={(e) => setExpires(e.target.value)} />
        </label>
        <button type="submit" disabled={action.busy}>
          İzleyici ekle
        </button>
      </form>
      <p className="muted small">Şifreyi sistem üretir. Bağlantı limiti, aynı anda kaç cihazdan izlenebileceğidir.</p>
      <ErrorNote message={action.error} />
    </Panel>
  );
}

function ViewerInfo({ viewer, server, onClose, onChanged }: { viewer: Viewer; server: string; onClose: () => void; onChanged: () => void }) {
  const action = useAction();
  const regenerate = () => {
    if (!window.confirm(`"${viewer.username}" için yeni şifre üretilsin mi? Süren izlemesi kesilir ve oynatıcısına yeni şifreyi girmesi gerekir.`)) return;
    void action.run(async () => {
      await api.post(`/api/tenant/viewers/${viewer.id}/regenerate-password`);
      onChanged();
    });
  };
  return (
    <Panel title={`Giriş bilgileri: ${viewer.username}`} onClose={onClose}>
      <p className="muted small">İzleyici, IPTV oynatıcısında "Xtream Codes" girişini seçip bu üç değeri yazar.</p>
      {server && <CopyField label="Sunucu" value={server} />}
      <CopyField label="Kullanıcı adı" value={viewer.username} />
      <CopyField label="Şifre" value={viewer.password} secret />
      <CopyField label="M3U listesi (Xtream desteklemeyen oynatıcılar için)" value={viewer.playlist_url} secret />
      <ErrorNote message={action.error} />
      <button type="button" className="ghost" disabled={action.busy} onClick={regenerate}>
        Şifreyi yenile
      </button>
    </Panel>
  );
}

function EditViewer({ viewer, onClose, onSaved }: { viewer: Viewer; onClose: () => void; onSaved: () => void }) {
  const [maxConnections, setMaxConnections] = useState(viewer.max_connections);
  const [expires, setExpires] = useState(toLocalInput(viewer.expires_at));
  const action = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      await api.patch(`/api/tenant/viewers/${viewer.id}`, {
        max_connections: maxConnections,
        expires_at: fromLocalInput(expires),
      });
      onSaved();
    });
  };

  return (
    <Panel title={`İzleyiciyi düzenle: ${viewer.username}`} onClose={onClose}>
      <form className="row" onSubmit={submit}>
        <label>
          Bağlantı limiti
          <input type="number" min={1} max={100} required value={maxConnections} onChange={(e) => setMaxConnections(Number(e.target.value))} />
        </label>
        <label>
          Bitiş tarihi (boşsa süresiz)
          <input type="datetime-local" value={expires} onChange={(e) => setExpires(e.target.value)} />
        </label>
        <button type="submit" disabled={action.busy}>
          Kaydet
        </button>
      </form>
      <ErrorNote message={action.error} />
    </Panel>
  );
}

// --- Oturumlar ---

export function Sessions() {
  const [offset, setOffset] = useState(0);
  const sessions = useLoad(() => api.page<Session>("/api/tenant/sessions", { limit: pageSize, offset }), 5000);
  useEffect(() => void sessions.reload(), [offset]);
  // İzlemeler azalınca boşalan son sayfada kalınmaz.
  useEffect(() => {
    if (sessions.data && sessions.data.items.length === 0 && offset > 0) setOffset(Math.max(0, offset - pageSize));
  }, [sessions.data]);
  return (
    <Panel title="Süren izlemeler">
      {!sessions.data ? (
        <Loading error={sessions.error} />
      ) : sessions.data.items.length === 0 ? (
        <p className="muted">Şu an izleyen yok. Liste 5 saniyede bir yenilenir.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>İzleyici</th>
                <th>Kanal</th>
                <th>Biçim</th>
                <th>Ağ adresi</th>
                <th>Sunucu</th>
                <th>Başlangıç</th>
              </tr>
            </thead>
            <tbody>
              {sessions.data.items.map((s) => (
                <tr key={s.id}>
                  <td>{s.viewer}</td>
                  <td>{s.channel}</td>
                  <td>{s.kind === "hls" ? "HLS" : "MPEG-TS"}</td>
                  <td>
                    <code>{s.ip}</code>
                  </td>
                  <td>{s.edge}</td>
                  <td>{formatDate(s.started_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {sessions.data && <Pager offset={offset} shown={sessions.data.items.length} total={sessions.data.total} onMove={setOffset} />}
    </Panel>
  );
}
