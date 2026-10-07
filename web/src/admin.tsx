import { type FormEvent, useState } from "react";
import { type Quotas, type Stats, type Tenant, api } from "./api";
import { Avatar, Badge, CopyField, EmptyState, ErrorNote, Loading, Modal, Panel, SearchBox, Stat, Usage, formatDate, useAction, useFeedback, useLoad } from "./ui";

// Yeni oluşturulan veya şifresi sıfırlanan yayıncının şifresi yalnızca bir kez gösterilir.
interface IssuedPassword {
  email: string;
  password: string;
}

export function AdminTenants() {
  const stats = useLoad(() => api.get<Stats>("/api/admin/stats"), 10000);
  const tenants = useLoad(() => api.get<Tenant[]>("/api/admin/tenants"), 10000);
  const [selected, setSelected] = useState<number | null>(null);
  const [issued, setIssued] = useState<IssuedPassword | null>(null);
  const [search, setSearch] = useState("");

  const refresh = () => {
    void stats.reload();
    void tenants.reload();
  };
  const current = tenants.data?.find((t) => t.id === selected) ?? null;
  const needle = search.trim().toLocaleLowerCase("tr-TR");
  const shown = tenants.data?.filter((t) => !needle || `${t.name} ${t.email}`.toLocaleLowerCase("tr-TR").includes(needle)) ?? [];

  return (
    <>
      {stats.data && (
        <div className="stats">
          <Stat icon="tenants" label="Yayıncı" value={stats.data.tenants} />
          <Stat icon="channels" label="Kanal" value={stats.data.channels} />
          <Stat icon="broadcast" label="Yayındaki kanal" value={stats.data.live_channels} live />
          <Stat icon="viewers" label="İzleyici hesabı" value={stats.data.viewers} />
          <Stat icon="sessions" label="Süren izleme" value={stats.data.active_sessions} />
        </div>
      )}

      {issued && (
        <Modal title="Panel şifresi" onClose={() => setIssued(null)}>
          <p className="question-body">
            Bu şifre yalnızca şimdi gösteriliyor. <strong>{issued.email}</strong> adresinin sahibine güvenli bir yoldan iletin; ilk
            girişten sonra değiştirmesini isteyin.
          </p>
          <CopyField label="Şifre" value={issued.password} />
          <div className="modal-actions">
            <button type="button" onClick={() => setIssued(null)}>
              Tamam
            </button>
          </div>
        </Modal>
      )}

      <CreateTenant
        onCreated={(t, password) => {
          setIssued({ email: t.email, password });
          refresh();
        }}
      />

      <Panel
        title="Yayıncılar"
        aside={tenants.data && tenants.data.length > 5 ? <SearchBox value={search} placeholder="Ad ya da e-postada ara" onChange={setSearch} /> : null}
      >
        {!tenants.data ? (
          <Loading error={tenants.error} />
        ) : tenants.data.length === 0 ? (
          <EmptyState icon="tenants" title="Henüz yayıncı yok">
            Yukarıdaki formla ilk yayıncıyı ekleyin.
          </EmptyState>
        ) : shown.length === 0 ? (
          <EmptyState icon="search" title="Aramayla eşleşen yayıncı yok" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Ad</th>
                  <th>E-posta</th>
                  <th>Durum</th>
                  <th className="num">Kanal</th>
                  <th className="num">İzleyici</th>
                  <th className="num">Yayında</th>
                  <th className="num">İzleme</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {shown.map((t) => (
                  <tr key={t.id} className={t.id === selected ? "selected" : ""}>
                    <td>
                      <span className="with-avatar">
                        <Avatar name={t.name} />
                        {t.name}
                      </span>
                    </td>
                    <td>{t.email || <span className="muted">panel hesabı yok</span>}</td>
                    <td>{t.status === "active" ? <Badge tone="ok">Etkin</Badge> : <Badge tone="warn">Askıda</Badge>}</td>
                    <td className="num">
                      <Usage used={t.channels} limit={t.quotas.max_channels} />
                    </td>
                    <td className="num">
                      <Usage used={t.viewers} limit={t.quotas.max_viewers} />
                    </td>
                    <td className="num">{t.live_channels > 0 ? <strong className="live-text">{t.live_channels}</strong> : <span className="muted">0</span>}</td>
                    <td className="num">
                      <Usage used={t.active_sessions} limit={t.quotas.max_connections} />
                    </td>
                    <td className="actions">
                      <button type="button" className="ghost" onClick={() => setSelected(t.id)}>
                        Yönet
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      {current && (
        <TenantDetail
          key={current.id}
          tenant={current}
          onChanged={refresh}
          onClose={() => setSelected(null)}
          onPassword={(password) => {
            setSelected(null);
            setIssued({ email: current.email, password });
          }}
        />
      )}
    </>
  );
}

function CreateTenant({ onCreated }: { onCreated: (t: Tenant, password: string) => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const action = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      const res = await api.post<{ tenant: Tenant; password: string }>("/api/admin/tenants", { name, email });
      setName("");
      setEmail("");
      onCreated(res.tenant, res.password);
    });
  };

  return (
    <Panel title="Yeni yayıncı">
      <form className="row" onSubmit={submit}>
        <label>
          Ad
          <input required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          E-posta (panele girişte kullanılır)
          <input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <button type="submit" disabled={action.busy}>
          Yayıncı ekle
        </button>
      </form>
      <ErrorNote message={action.error} />
    </Panel>
  );
}

function TenantDetail({
  tenant,
  onChanged,
  onClose,
  onPassword,
}: {
  tenant: Tenant;
  onChanged: () => void;
  onClose: () => void;
  onPassword: (password: string) => void;
}) {
  const [name, setName] = useState(tenant.name);
  const [email, setEmail] = useState(tenant.email);
  const [quotas, setQuotas] = useState<Quotas>(tenant.quotas);
  const action = useAction();
  const { confirm, toast } = useFeedback();
  const path = `/api/admin/tenants/${tenant.id}`;

  const save = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      await api.patch(path, { name, email, quotas });
      toast("Değişiklikler kaydedildi.");
      onChanged();
    });
  };

  const setStatus = async (status: Tenant["status"]) => {
    const sure = await confirm(
      status === "suspended"
        ? {
            title: `"${tenant.name}" askıya alınsın mı?`,
            body: "Süren yayınları kesilir, izleyicileri izleyemez ve panele giremez.",
            confirmLabel: "Askıya al",
            danger: true,
          }
        : { title: `"${tenant.name}" yeniden etkinleştirilsin mi?`, confirmLabel: "Etkinleştir" },
    );
    if (!sure) return;
    void action.run(async () => {
      await api.patch(path, { status });
      toast(status === "suspended" ? "Yayıncı askıya alındı." : "Yayıncı etkinleştirildi.");
      onChanged();
    });
  };

  const resetPassword = async () => {
    const sure = await confirm({
      title: "Yeni panel şifresi üretilsin mi?",
      body: `"${tenant.name}" için eski şifre ve açık oturumlar geçersiz olur.`,
      confirmLabel: "Şifreyi sıfırla",
      danger: true,
    });
    if (!sure) return;
    void action.run(async () => {
      const res = await api.post<{ password: string }>(`${path}/reset-password`);
      onPassword(res.password);
    });
  };

  const quota = (key: keyof Quotas, label: string) => (
    <label>
      {label}
      <input
        type="number"
        min={0}
        max={100000}
        required
        value={quotas[key]}
        onChange={(e) => setQuotas({ ...quotas, [key]: Number(e.target.value) })}
      />
    </label>
  );

  return (
    <Panel title={`Yayıncı: ${tenant.name}`} onClose={onClose}>
      <p className="drawer-status">
        {tenant.status === "active" ? <Badge tone="ok">Etkin</Badge> : <Badge tone="warn">Askıda</Badge>}
        <span className="muted small">Oluşturulma: {formatDate(tenant.created_at)}</span>
      </p>
      <form className="stack" onSubmit={save}>
        <div className="row">
          <label>
            Ad
            <input required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label>
            E-posta
            <input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
          </label>
        </div>
        <div className="row">
          {quota("max_channels", "Kanal kotası")}
          {quota("max_viewers", "İzleyici kotası")}
          {quota("max_connections", "Eşzamanlı izleme kotası")}
        </div>
        <ErrorNote message={action.error} />
        <div className="drawer-actions">
          <button type="submit" disabled={action.busy}>
            Kaydet
          </button>
          <button type="button" className="ghost" disabled={action.busy || !tenant.email} onClick={() => void resetPassword()}>
            Panel şifresini sıfırla
          </button>
          {tenant.status === "active" ? (
            <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => void setStatus("suspended")}>
              Askıya al
            </button>
          ) : (
            <button type="button" className="ghost" disabled={action.busy} onClick={() => void setStatus("active")}>
              Yeniden etkinleştir
            </button>
          )}
        </div>
      </form>
    </Panel>
  );
}
