import { type FormEvent, useState } from "react";
import { type Quotas, type Stats, type Tenant, api } from "./api";
import { Badge, CopyField, ErrorNote, Loading, Panel, formatDate, useAction, useLoad } from "./ui";

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

  const refresh = () => {
    void stats.reload();
    void tenants.reload();
  };
  const current = tenants.data?.find((t) => t.id === selected) ?? null;

  return (
    <>
      {stats.data && (
        <div className="cards">
          <Card label="Yayıncı" value={stats.data.tenants} />
          <Card label="Kanal" value={stats.data.channels} />
          <Card label="Yayındaki kanal" value={stats.data.live_channels} />
          <Card label="İzleyici hesabı" value={stats.data.viewers} />
          <Card label="Süren izleme" value={stats.data.active_sessions} />
        </div>
      )}

      {issued && (
        <Panel title="Panel şifresi" onClose={() => setIssued(null)}>
          <p>
            Bu şifre yalnızca şimdi gösteriliyor. <strong>{issued.email}</strong> adresinin sahibine güvenli bir yoldan iletin; ilk
            girişten sonra değiştirmesini isteyin.
          </p>
          <CopyField label="Şifre" value={issued.password} />
        </Panel>
      )}

      <CreateTenant
        onCreated={(t, password) => {
          setIssued({ email: t.email, password });
          setSelected(t.id);
          refresh();
        }}
      />

      <Panel title="Yayıncılar">
        {!tenants.data ? (
          <Loading error={tenants.error} />
        ) : tenants.data.length === 0 ? (
          <p className="muted">Henüz yayıncı yok. Yukarıdaki formla ilk yayıncıyı ekleyin.</p>
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
                {tenants.data.map((t) => (
                  <tr key={t.id} className={t.id === selected ? "selected" : ""}>
                    <td>{t.name}</td>
                    <td>{t.email || <span className="muted">panel hesabı yok</span>}</td>
                    <td>{t.status === "active" ? <Badge tone="ok">Etkin</Badge> : <Badge tone="warn">Askıda</Badge>}</td>
                    <td className="num">
                      {t.channels} / {t.quotas.max_channels}
                    </td>
                    <td className="num">
                      {t.viewers} / {t.quotas.max_viewers}
                    </td>
                    <td className="num">{t.live_channels}</td>
                    <td className="num">
                      {t.active_sessions} / {t.quotas.max_connections}
                    </td>
                    <td className="actions">
                      <button type="button" className="ghost" onClick={() => setSelected(t.id === selected ? null : t.id)}>
                        {t.id === selected ? "Kapat" : "Yönet"}
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
          onPassword={(password) => setIssued({ email: current.email, password })}
        />
      )}
    </>
  );
}

function Card({ label, value }: { label: string; value: number }) {
  return (
    <div className="card">
      <div className="card-value">{value}</div>
      <div className="muted small">{label}</div>
    </div>
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
  const [saved, setSaved] = useState(false);
  const action = useAction();
  const path = `/api/admin/tenants/${tenant.id}`;

  const save = (e: FormEvent) => {
    e.preventDefault();
    setSaved(false);
    void action.run(async () => {
      await api.patch(path, { name, email, quotas });
      setSaved(true);
      onChanged();
    });
  };

  const setStatus = (status: Tenant["status"]) => {
    const question =
      status === "suspended"
        ? `"${tenant.name}" askıya alınsın mı? Süren yayınları kesilir ve izleyicileri izleyemez.`
        : `"${tenant.name}" yeniden etkinleştirilsin mi?`;
    if (!window.confirm(question)) return;
    void action.run(async () => {
      await api.patch(path, { status });
      onChanged();
    });
  };

  const resetPassword = () => {
    if (!window.confirm(`"${tenant.name}" için yeni bir panel şifresi üretilsin mi? Eski şifre ve açık oturumlar geçersiz olur.`)) return;
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
      <p className="muted small">Oluşturulma: {formatDate(tenant.created_at)}</p>
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
        {saved && <p className="note ok">Değişiklikler kaydedildi.</p>}
        <div className="row">
          <button type="submit" disabled={action.busy}>
            Kaydet
          </button>
          <button type="button" className="ghost" disabled={action.busy || !tenant.email} onClick={resetPassword}>
            Panel şifresini sıfırla
          </button>
          {tenant.status === "active" ? (
            <button type="button" className="danger" disabled={action.busy} onClick={() => setStatus("suspended")}>
              Askıya al
            </button>
          ) : (
            <button type="button" className="ghost" disabled={action.busy} onClick={() => setStatus("active")}>
              Yeniden etkinleştir
            </button>
          )}
        </div>
      </form>
    </Panel>
  );
}
