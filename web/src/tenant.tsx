import { type FormEvent, useEffect, useState } from "react";
import { type Category, type Channel, type Overview, type Session, type Viewer, api } from "./api";
import {
  Avatar,
  Badge,
  CopyField,
  EmptyState,
  ErrorNote,
  Icon,
  LiveBadge,
  Loading,
  Pager,
  Panel,
  SearchBox,
  Stat,
  formatDate,
  fromLocalInput,
  navigate,
  pageSize,
  since,
  toLocalInput,
  useAction,
  useFeedback,
  useLoad,
} from "./ui";

// --- Genel bakış ---

export function OverviewPage() {
  const overview = useLoad(() => api.get<Overview>("/api/tenant/overview"), 10000);
  const channels = useLoad(() => api.get<Channel[]>("/api/tenant/channels"), 10000);
  if (!overview.data) return <Loading error={overview.error} />;
  const o = overview.data;
  const live = channels.data?.filter((c) => c.live) ?? [];
  const steps = [
    { done: o.usage.channels > 0, title: "Bir kanal oluşturun", hint: "Her kanalın kendi yayın anahtarı olur.", to: "/kanallar", action: "Kanallara git" },
    { done: live.length > 0, title: "OBS ile yayına başlayın", hint: 'Kanallar sayfasındaki "OBS ayarları"nı OBS\'e yapıştırın.', to: "/kanallar", action: "OBS ayarları" },
    { done: o.usage.viewers > 0, title: "İzleyici hesabı açın", hint: "İzleyici, verdiğiniz kullanıcı adı ve şifreyle oynatıcısından izler.", to: "/izleyiciler", action: "İzleyicilere git" },
  ];
  return (
    <>
      <div className="stats">
        <Stat icon="broadcast" label="Yayındaki kanal" value={live.length} live />
        <Stat icon="channels" label="Kanal" value={o.usage.channels} limit={o.quotas.max_channels} />
        <Stat icon="viewers" label="İzleyici hesabı" value={o.usage.viewers} limit={o.quotas.max_viewers} />
        <Stat icon="sessions" label="Süren izleme" value={o.usage.connections} limit={o.quotas.max_connections} />
      </div>
      <p className="muted small">Kotaları platform yöneticisi belirler.</p>

      {(o.usage.channels === 0 || o.usage.viewers === 0) && (
        <Panel title="Başlarken">
          <ol className="steps">
            {steps.map((s) => (
              <li key={s.title} className={s.done ? "done" : ""}>
                <span className="step-mark">{s.done && <Icon name="check" size={14} />}</span>
                <div>
                  <strong>{s.title}</strong>
                  <div className="muted small">{s.hint}</div>
                </div>
                {!s.done && (
                  <button type="button" className="ghost" onClick={() => navigate(s.to)}>
                    {s.action}
                  </button>
                )}
              </li>
            ))}
          </ol>
        </Panel>
      )}

      {live.length > 0 && (
        <Panel title="Şu an yayında">
          <ul className="live-list">
            {live.map((c) => (
              <li key={c.id}>
                <Avatar name={c.name} src={c.logo_url} />
                <span>{c.name}</span>
                <LiveBadge />
              </li>
            ))}
          </ul>
        </Panel>
      )}

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
  const { confirm, ask, toast } = useFeedback();

  const add = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      await api.post("/api/tenant/categories", { name });
      setName("");
      await list.reload();
      toast("Kategori eklendi.");
    });
  };
  const rename = async (c: Category) => {
    const next = await ask({
      title: "Kategoriyi yeniden adlandır",
      input: { label: "Yeni ad", initial: c.name, maxLength: 100 },
      confirmLabel: "Kaydet",
    });
    if (!next || next === c.name) return;
    void action.run(async () => {
      await api.patch(`/api/tenant/categories/${c.id}`, { name: next });
      await list.reload();
      toast("Kategori adı değişti.");
    });
  };
  const remove = async (c: Category) => {
    const sure = await confirm({
      title: `"${c.name}" silinsin mi?`,
      body: 'Bu kategorideki kanallar oynatıcıda "Genel" altında görünür.',
      confirmLabel: "Sil",
      danger: true,
    });
    if (!sure) return;
    void action.run(async () => {
      await api.del(`/api/tenant/categories/${c.id}`);
      await list.reload();
      toast("Kategori silindi.");
    });
  };

  return (
    <>
      <Panel title="Yeni kategori">
        <form className="row" onSubmit={add}>
          <label>
            Ad
            <input required maxLength={100} placeholder="Spor" value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <button type="submit" disabled={action.busy}>
            Kategori ekle
          </button>
        </form>
        <ErrorNote message={action.error} />
      </Panel>
      <Panel title="Kategoriler" aside={list.data && list.data.length > 0 ? <span className="count">{list.data.length}</span> : null}>
        {!list.data ? (
          <Loading error={list.error} />
        ) : list.data.length === 0 ? (
          <EmptyState icon="categories" title="Henüz kategori yok">
            Kategorisiz kanallar oynatıcıda "Genel" altında görünür.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table>
              <tbody>
                {list.data.map((c) => (
                  <tr key={c.id}>
                    <td>{c.name}</td>
                    <td className="actions">
                      <button type="button" className="ghost" disabled={action.busy} onClick={() => void rename(c)}>
                        Yeniden adlandır
                      </button>
                      <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => void remove(c)}>
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
  const { confirm, toast } = useFeedback();
  const cats = categories.data ?? [];
  const current = channels.data?.find((c) => c.id === opened?.id) ?? null;
  const liveCount = channels.data?.filter((c) => c.live).length ?? 0;

  const remove = async (c: Channel) => {
    const sure = await confirm({
      title: `"${c.name}" silinsin mi?`,
      body: c.live ? "Kanal şu an yayında; yayın ve izlemeler kesilir." : "Kanal ve yayın anahtarı kalıcı olarak silinir.",
      confirmLabel: "Sil",
      danger: true,
    });
    if (!sure) return;
    void action.run(async () => {
      await api.del(`/api/tenant/channels/${c.id}`);
      setOpened(null);
      await channels.reload();
      toast("Kanal silindi.");
    });
  };

  return (
    <>
      <ChannelForm
        title="Yeni kanal"
        categories={cats}
        submitLabel="Kanal ekle"
        onSaved={(c) => {
          void channels.reload();
          // Yeni kanalın ilk işi OBS'e bağlanmaktır; ayarları hemen gösterilir.
          if (c) setOpened({ id: c.id, mode: "obs" });
        }}
      />

      <Panel
        title="Kanallar"
        aside={
          channels.data && channels.data.length > 0 ? (
            <span className="count">
              {channels.data.length} kanal{liveCount > 0 && ` · ${liveCount} yayında`}
            </span>
          ) : null
        }
      >
        <ErrorNote message={action.error} />
        {!channels.data ? (
          <Loading error={channels.error} />
        ) : channels.data.length === 0 ? (
          <EmptyState icon="channels" title="Henüz kanal yok">
            Yukarıdaki formla bir kanal ekleyin; OBS ayarları hemen açılır.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Kanal</th>
                  <th>Kategori</th>
                  <th>Durum</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {channels.data.map((c) => (
                  <tr key={c.id} className={c.id === opened?.id ? "selected" : ""}>
                    <td>
                      <span className="with-avatar">
                        <Avatar name={c.name} src={c.logo_url} />
                        <span>
                          {c.name}
                          <span className="sub">No {c.id}</span>
                        </span>
                      </span>
                    </td>
                    <td>{cats.find((k) => k.id === c.category_id)?.name ?? <span className="muted">Genel</span>}</td>
                    <td>{c.live ? <LiveBadge /> : <Badge tone="off">Çevrimdışı</Badge>}</td>
                    <td className="actions">
                      <button type="button" className="ghost" onClick={() => setOpened({ id: c.id, mode: "obs" })}>
                        OBS ayarları
                      </button>
                      <button type="button" className="ghost" onClick={() => setOpened({ id: c.id, mode: "edit" })}>
                        Düzenle
                      </button>
                      <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => void remove(c)}>
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
  // created, yeni eklenen kanaldır; düzenlemede verilmez.
  onSaved: (created?: Channel) => void;
  onClose?: () => void;
}) {
  const [name, setName] = useState(channel?.name ?? "");
  const [categoryId, setCategoryId] = useState<number | null>(channel?.category_id ?? null);
  const [logoUrl, setLogoUrl] = useState(channel?.logo_url ?? "");
  const action = useAction();
  const { toast } = useFeedback();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      const body = { name, category_id: categoryId, logo_url: logoUrl.trim() };
      if (channel) {
        await api.patch(`/api/tenant/channels/${channel.id}`, body);
        toast("Kanal kaydedildi.");
        onSaved();
        return;
      }
      const created = await api.post<Channel>("/api/tenant/channels", body);
      setName("");
      setLogoUrl("");
      toast("Kanal eklendi.");
      onSaved(created);
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
  const { confirm, toast } = useFeedback();
  const regenerate = async () => {
    const sure = await confirm({
      title: "Yayın anahtarı yenilensin mi?",
      body: `OBS'teki eski anahtar artık çalışmaz.${channel.live ? " Kanal şu an yayında; yayın kesilir." : ""}`,
      confirmLabel: "Anahtarı yenile",
      danger: true,
    });
    if (!sure) return;
    void action.run(async () => {
      await api.post(`/api/tenant/channels/${channel.id}/regenerate-secret`);
      onChanged();
      toast("Yayın anahtarı yenilendi.");
    });
  };
  return (
    <Panel title={`OBS ayarları: ${channel.name}`} onClose={onClose}>
      <p className="drawer-status">{channel.live ? <LiveBadge /> : <Badge tone="off">Çevrimdışı</Badge>}</p>
      <p className="muted small">OBS → Ayarlar → Yayın → Hizmet: "Özel". Aşağıdaki iki değeri yapıştırın.</p>
      <CopyField label="Sunucu" value={channel.ingest_url} />
      <CopyField label="Yayın anahtarı" value={channel.stream_key} secret />
      <p className="muted small">Yayın anahtarı gizlidir: ele geçiren kişi bu kanala yayın açabilir.</p>
      <p className="tip">
        <strong>Gecikmeyi düşürmek için:</strong> OBS → Ayarlar → Çıkış → Çıkış kipi "Gelişmiş" → Yayın sekmesinde "Anahtar kare aralığı" 2
        sn. Varsayılan ayarda bu aralık 8 saniyeyi bulur; kanalı açan izleyici o kadar geriden başlar ve kanal geç açılır.
      </p>
      <ErrorNote message={action.error} />
      <button type="button" className="ghost" disabled={action.busy} onClick={() => void regenerate()}>
        Anahtarı yenile
      </button>
    </Panel>
  );
}

// --- İzleyiciler ---

export function Viewers() {
  // Liste dilim dilim gelir (en yeni önce); arama kullanıcı adında yapılır.
  // search kutudaki metindir; query, yazma durunca sunucuya gönderilen halidir.
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const viewers = useLoad(() => api.page<Viewer>("/api/tenant/viewers", { q: query, limit: pageSize, offset }), undefined, [query, offset]);
  const overview = useLoad(() => api.get<Overview>("/api/tenant/overview"));
  const [opened, setOpened] = useState<{ id: number; mode: "info" | "edit" } | null>(null);
  const action = useAction();
  const { confirm, toast } = useFeedback();
  const current = viewers.data?.items.find((v) => v.id === opened?.id) ?? null;
  const reload = () => void viewers.reload();
  useEffect(() => {
    // Yazarken her tuşta istek gitmesin diye kısa bir bekleme.
    // Sayfa aramayla birlikte sıfırlanır; ayrı sıfırlansaydı eski aramanın ilk sayfası boşuna istenirdi.
    const timer = window.setTimeout(() => (setQuery(search.trim()), setOffset(0)), 250);
    return () => window.clearTimeout(timer);
  }, [search]);
  // Liste değişince (arama, sayfa) açık bilgi ya da düzenleme penceresi kapanır.
  const showPage = (next: number) => (setOpened(null), setOffset(next));
  // Silme sonrası boşalan son sayfada kalınmaz.
  useEffect(() => {
    if (viewers.data && viewers.data.items.length === 0 && offset > 0) setOffset(Math.max(0, offset - pageSize));
  }, [viewers.data]);

  const toggle = async (v: Viewer) => {
    const status = v.status === "active" ? "suspended" : "active";
    if (status === "suspended") {
      const sure = await confirm({
        title: `"${v.username}" askıya alınsın mı?`,
        body: "Süren izlemesi birkaç saniye içinde kesilir. Hesabı istediğiniz zaman yeniden etkinleştirebilirsiniz.",
        confirmLabel: "Askıya al",
        danger: true,
      });
      if (!sure) return;
    }
    void action.run(async () => {
      await api.patch(`/api/tenant/viewers/${v.id}`, { status });
      await viewers.reload();
      toast(status === "suspended" ? "İzleyici askıya alındı." : "İzleyici etkinleştirildi.");
    });
  };
  const remove = async (v: Viewer) => {
    const sure = await confirm({
      title: `"${v.username}" silinsin mi?`,
      body: "Süren izlemesi kesilir ve hesap geri getirilemez.",
      confirmLabel: "Sil",
      danger: true,
    });
    if (!sure) return;
    void action.run(async () => {
      await api.del(`/api/tenant/viewers/${v.id}`);
      setOpened(null);
      await viewers.reload();
      toast("İzleyici silindi.");
    });
  };

  return (
    <>
      <CreateViewer
        onCreated={(v) => {
          // Yeni izleyici listenin başındadır; görünmesi için arama ve sayfa sıfırlanır.
          setOpened({ id: v.id, mode: "info" });
          setSearch("");
          setQuery("");
          setOffset(0);
          reload();
        }}
      />

      <Panel title="İzleyiciler" aside={<SearchBox value={search} placeholder="Kullanıcı adında ara" onChange={(v) => (setOpened(null), setSearch(v))} />}>
        <ErrorNote message={action.error} />
        <ErrorNote message={viewers.data ? viewers.error : null} />
        {!viewers.data ? (
          <Loading error={viewers.error} />
        ) : viewers.data.items.length === 0 ? (
          search.trim() ? (
            <EmptyState icon="search" title="Aramayla eşleşen izleyici yok">
              Kullanıcı adının bir parçasını yazmanız yeterli.
            </EmptyState>
          ) : (
            <EmptyState icon="viewers" title="Henüz izleyici yok">
              Yukarıdaki formla bir izleyici hesabı oluşturun; giriş bilgileri hemen gösterilir.
            </EmptyState>
          )
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
                      <td>
                        <span className="with-avatar">
                          <Avatar name={v.username} />
                          {v.username}
                        </span>
                      </td>
                      <td>
                        {v.status === "suspended" ? (
                          <Badge tone="warn">Askıda</Badge>
                        ) : expired ? (
                          <Badge tone="warn">Süresi doldu</Badge>
                        ) : (
                          <Badge tone="ok">Etkin</Badge>
                        )}
                      </td>
                      <td>{v.expires_at ? formatDate(v.expires_at) : <span className="muted">Süresiz</span>}</td>
                      <td className="num">{v.max_connections}</td>
                      <td className="actions">
                        <button type="button" className="ghost" onClick={() => setOpened({ id: v.id, mode: "info" })}>
                          Giriş bilgileri
                        </button>
                        <button type="button" className="ghost" onClick={() => setOpened({ id: v.id, mode: "edit" })}>
                          Düzenle
                        </button>
                        <button type="button" className="ghost" disabled={action.busy} onClick={() => void toggle(v)}>
                          {v.status === "active" ? "Askıya al" : "Etkinleştir"}
                        </button>
                        <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => void remove(v)}>
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
        {viewers.data && <Pager offset={offset} shown={viewers.data.items.length} total={viewers.data.total} onMove={showPage} />}
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
  const { toast } = useFeedback();

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
      toast("İzleyici eklendi.");
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
  const { confirm, toast } = useFeedback();
  const regenerate = async () => {
    const sure = await confirm({
      title: `"${viewer.username}" için yeni şifre üretilsin mi?`,
      body: "Süren izlemesi kesilir ve oynatıcısına yeni şifreyi girmesi gerekir.",
      confirmLabel: "Şifreyi yenile",
      danger: true,
    });
    if (!sure) return;
    void action.run(async () => {
      await api.post(`/api/tenant/viewers/${viewer.id}/regenerate-password`);
      onChanged();
      toast("Yeni şifre üretildi.");
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
      <button type="button" className="ghost" disabled={action.busy} onClick={() => void regenerate()}>
        Şifreyi yenile
      </button>
    </Panel>
  );
}

function EditViewer({ viewer, onClose, onSaved }: { viewer: Viewer; onClose: () => void; onSaved: () => void }) {
  const [maxConnections, setMaxConnections] = useState(viewer.max_connections);
  const [expires, setExpires] = useState(toLocalInput(viewer.expires_at));
  const action = useAction();
  const { toast } = useFeedback();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      await api.patch(`/api/tenant/viewers/${viewer.id}`, {
        max_connections: maxConnections,
        expires_at: fromLocalInput(expires),
      });
      toast("İzleyici kaydedildi.");
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
  const sessions = useLoad(() => api.page<Session>("/api/tenant/sessions", { limit: pageSize, offset }), 5000, [offset]);
  // İzlemeler azalınca boşalan son sayfada kalınmaz.
  useEffect(() => {
    if (sessions.data && sessions.data.items.length === 0 && offset > 0) setOffset(Math.max(0, offset - pageSize));
  }, [sessions.data]);
  return (
    <Panel
      title="Süren izlemeler"
      aside={
        <span className="count refreshing" title="Liste 5 saniyede bir yenilenir">
          <span className="pulse" />
          {sessions.data ? `${sessions.data.total} izleme` : "canlı"}
        </span>
      }
    >
      <ErrorNote message={sessions.data ? sessions.error : null} />
      {!sessions.data ? (
        <Loading error={sessions.error} />
      ) : sessions.data.items.length === 0 ? (
        <EmptyState icon="sessions" title="Şu an izleyen yok">
          Bir izleyici kanal açtığında burada görünür. Liste 5 saniyede bir yenilenir.
        </EmptyState>
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
                <th>Süre</th>
              </tr>
            </thead>
            <tbody>
              {sessions.data.items.map((s) => (
                <tr key={s.id}>
                  <td>
                    <span className="with-avatar">
                      <Avatar name={s.viewer} />
                      {s.viewer}
                    </span>
                  </td>
                  <td>{s.channel}</td>
                  <td>
                    <Badge tone="off">{s.kind === "hls" ? "HLS" : "MPEG-TS"}</Badge>
                  </td>
                  <td>
                    <code>{s.ip}</code>
                  </td>
                  <td>{s.edge}</td>
                  <td title={`Başlangıç: ${formatDate(s.started_at)}`}>{since(s.started_at)}</td>
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
