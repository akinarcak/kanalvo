import { type FormEvent, useEffect, useState } from "react";
import { type Edge, type Enrollment, api } from "./api";
import { Badge, ErrorNote, Icon, Loading, Panel, formatDate, since, useAction, useFeedback, useLoad } from "./ui";

function EdgeStatus({ edge }: { edge: Edge }) {
  if (!edge.enabled) return <Badge tone="off">Devre dışı</Badge>;
  if (edge.healthy) return <Badge tone="ok">Sağlıklı</Badge>;
  return <Badge tone="warn">{edge.last_seen_at ? "Ulaşılamıyor" : "Sinyal bekleniyor"}</Badge>;
}

// lastSeen, son sinyalin ne kadar önce geldiğini yazar; yoklama aralığı içindeki sinyal "şimdi"dir.
function lastSeen(iso: string): string {
  const text = since(iso);
  return text === "az önce" ? "şimdi" : `${text} önce`;
}

export function AdminEdges() {
  const edges = useLoad(() => api.get<Edge[]>("/api/admin/edges"), 5000);
  const [selected, setSelected] = useState<number | null>(null);
  // enrollment, açık "Sunucu ekle" penceresinin kurulum kodudur.
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const action = useAction();
  const current = edges.data?.find((e) => e.id === selected) ?? null;
  const refresh = () => void edges.reload();
  const startEnrollment = () =>
    void action.run(async () => {
      setEnrollment(await api.post<Enrollment>("/api/admin/edge-enrollments"));
    });

  return (
    <>
      <p className="tip">
        İzleyiciler, sağlıklı ve etkin sunucular arasında ağırlıklarıyla orantılı olarak dağıtılır. Her sunucu 5 saniyede bir
        yoklanır; 15 saniye yanıt vermeyen sunucuya yeni izleyici gönderilmez.
      </p>

      <ErrorNote message={action.error} />
      <Panel
        title="Sunucular"
        aside={
          <button type="button" disabled={action.busy} onClick={startEnrollment}>
            Sunucu ekle
          </button>
        }
      >
        {!edges.data ? (
          <Loading error={edges.error} />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Ad</th>
                  <th>İzleyici adresi</th>
                  <th>Durum</th>
                  <th className="num">Ağırlık</th>
                  <th className="num">İzleme</th>
                  <th>Son sinyal</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {edges.data.map((e) => (
                  <tr key={e.id} className={e.id === selected ? "selected" : ""}>
                    <td>
                      {e.name} {e.builtin && <span className="muted small">(bu sunucu)</span>}
                    </td>
                    <td>
                      <code>{e.base_url}</code>
                    </td>
                    <td>
                      <EdgeStatus edge={e} />
                    </td>
                    <td className="num">{e.weight}</td>
                    <td className="num">{e.active_sessions}</td>
                    <td title={e.last_seen_at ? formatDate(e.last_seen_at) : undefined}>
                      {e.last_seen_at ? lastSeen(e.last_seen_at) : <span className="muted">Hiç</span>}
                    </td>
                    <td className="actions">
                      <button type="button" className="ghost" onClick={() => setSelected(e.id)}>
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

      <details className="manual">
        <summary>Elle ekle (gelişmiş)</summary>
        <CreateEdge
          onCreated={(e) => {
            setSelected(e.id);
            refresh();
          }}
        />
      </details>

      {enrollment && (
        <EnrollDrawer
          key={enrollment.id}
          enrollment={enrollment}
          onChanged={refresh}
          onRestart={startEnrollment}
          onClose={() => {
            setEnrollment(null);
            refresh();
          }}
        />
      )}

      {current && (
        <EdgeDetail
          key={current.id}
          edge={current}
          onChanged={refresh}
          onClose={() => setSelected(null)}
          onDeleted={() => {
            setSelected(null);
            refresh();
          }}
        />
      )}
    </>
  );
}

// EnrollDrawer, yeni sunucuyu tek komutla ekler: önce kurulum komutunu gösterir ve cihazın
// bağlanmasını bekler; cihaz kaydolunca adını ve izleyici adresini onaylatıp sunucuyu etkinleştirir.
function EnrollDrawer({
  enrollment,
  onChanged,
  onRestart,
  onClose,
}: {
  enrollment: Enrollment;
  onChanged: () => void;
  onRestart: () => void;
  onClose: () => void;
}) {
  const state = useLoad(() => api.get<Enrollment>(`/api/admin/edge-enrollments/${enrollment.id}`), 2000);
  const status = state.data?.status ?? "waiting";
  const edge = state.data?.edge;
  const [copied, setCopied] = useState(false);
  const minutesLeft = Math.max(0, Math.ceil((new Date(enrollment.expires_at).getTime() - Date.now()) / 60000));

  // Cihaz kaydolduğunda arkadaki liste de yenilenir.
  useEffect(() => {
    if (status === "enrolled") onChanged();
  }, [status]);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(enrollment.command ?? "");
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // pano kullanılamıyorsa komut zaten görünür; elle kopyalanabilir
    }
  };

  return (
    <Panel title="Sunucu ekle" onClose={onClose}>
      <ol className="enroll-steps">
        <li className={status === "waiting" ? "current" : "done"}>
          <span className="step-mark">{status !== "waiting" && <Icon name="check" size={14} />}</span>
          <div>
            <strong>Yeni sunucuda bu komutu çalıştırın</strong>
            {status === "waiting" && (
              <>
                <pre className="command">{enrollment.command}</pre>
                <button type="button" className={`ghost${copied ? " done" : ""}`} onClick={() => void copy()}>
                  {copied ? "Kopyalandı" : "Komutu kopyala"}
                </button>
                <p className="muted small">
                  Linux sunucuda yönetici yetkisiyle çalıştırın. Docker yoksa kurulur, servisler başlatılır ve sunucu kendini
                  kaydeder. İzleyicilere 80 numaralı port açılır; başka bir port için komutun sonunu{" "}
                  <code>| sudo EDGE_PORT=8080 sh</code> yapın.
                </p>
                <p className="muted small">Kod tek kullanımlıktır ve yaklaşık {minutesLeft} dakika geçerlidir.</p>
              </>
            )}
          </div>
        </li>
        <li className={status === "waiting" ? "" : edge ? "current" : ""}>
          <span className="step-mark" />
          <div>
            <strong>Bilgileri onaylayın</strong>
            {status === "waiting" && (
              <p className="waiting">
                <span className="pulse" />
                Sunucunun bağlanması bekleniyor…
              </p>
            )}
            {status === "enrolled" && edge && <ConfirmEdge key={edge.id} edge={edge} onDone={onClose} />}
            {status === "enrolled" && !edge && <p className="muted small">Bu kodla kaydolan sunucu silinmiş.</p>}
            {status === "expired" && (
              <>
                <p className="note error">Kodun süresi doldu; sunucu bağlanmadı.</p>
                <button type="button" onClick={onRestart}>
                  Yeni kod al
                </button>
              </>
            )}
          </div>
        </li>
      </ol>
      <ErrorNote message={state.error} />
    </Panel>
  );
}

// ConfirmEdge, kaydolan sunucunun adını ve izleyici adresini onaylatır; onaylanınca sunucu izleyici almaya başlar.
function ConfirmEdge({ edge, onDone }: { edge: Edge; onDone: () => void }) {
  const [name, setName] = useState(edge.name);
  const [baseURL, setBaseURL] = useState(edge.base_url);
  const [weight, setWeight] = useState(edge.weight);
  const action = useAction();
  const { toast } = useFeedback();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      await api.patch(`/api/admin/edges/${edge.id}`, { name, base_url: baseURL, weight, enabled: true });
      toast("Sunucu eklendi; izleyici almaya başladı.");
      onDone();
    });
  };

  return (
    <form className="stack" onSubmit={submit}>
      <p className="drawer-status">
        <EdgeStatus edge={{ ...edge, enabled: true }} />
        <span className="muted small">
          <code>{edge.pull_ip}</code> adresinden bağlandı
        </span>
      </p>
      <label>
        Ad
        <input required autoFocus maxLength={100} placeholder="Frankfurt" value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label>
        İzleyici adresi
        <input required value={baseURL} onChange={(e) => setBaseURL(e.target.value)} />
      </label>
      <p className="muted small">
        İzleyiciler bu adrese yönlendirilir. Sunucunun bir alan adı varsa ya da HTTPS arkasındaysa burada değiştirin.
      </p>
      <label>
        Ağırlık (1-1000)
        <input type="number" min={1} max={1000} required value={weight} onChange={(e) => setWeight(Number(e.target.value))} />
      </label>
      <ErrorNote message={action.error} />
      <div className="drawer-actions">
        <button type="submit" disabled={action.busy}>
          Sunucuyu ekle
        </button>
      </div>
      <p className="muted small">Onaylamadan kapatırsanız sunucu listede "Devre dışı" kalır; sonradan "Yönet"ten etkinleştirebilirsiniz.</p>
    </form>
  );
}

function CreateEdge({ onCreated }: { onCreated: (e: Edge) => void }) {
  const [name, setName] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [pullIP, setPullIP] = useState("");
  const action = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      const created = await api.post<Edge>("/api/admin/edges", { name, base_url: baseURL, pull_ip: pullIP });
      setName("");
      setBaseURL("");
      setPullIP("");
      onCreated(created);
    });
  };

  return (
    <Panel title="Yeni sunucu">
      <form className="row" onSubmit={submit}>
        <label>
          Ad
          <input required maxLength={100} placeholder="Frankfurt" value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          İzleyici adresi
          <input required placeholder="http://edge1.example.com" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} />
        </label>
        <label>
          Sunucunun IP adresi
          <input required placeholder="203.0.113.20" value={pullIP} onChange={(e) => setPullIP(e.target.value)} />
        </label>
        <button type="submit" disabled={action.busy}>
          Sunucu ekle
        </button>
      </form>
      <p className="muted small">
        IP adresi, sunucunun yayını ana sunucudan çekerken göründüğü adrestir; yalnızca bu adresten gelen çekme isteği kabul edilir.
      </p>
      <ErrorNote message={action.error} />
    </Panel>
  );
}

function EdgeDetail({
  edge,
  onChanged,
  onClose,
  onDeleted,
}: {
  edge: Edge;
  onChanged: () => void;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const [name, setName] = useState(edge.name);
  const [baseURL, setBaseURL] = useState(edge.base_url);
  const [controlURL, setControlURL] = useState(edge.control_url);
  const [pullIP, setPullIP] = useState(edge.pull_ip);
  const [weight, setWeight] = useState(edge.weight);
  const [copied, setCopied] = useState(false);
  const action = useAction();
  const { confirm, toast } = useFeedback();
  const path = `/api/admin/edges/${edge.id}`;

  const save = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      // Yerel sunucunun adresleri sunucu ayarlarından gelir; yalnızca adı ve ağırlığı değişir.
      const body = edge.builtin ? { name, weight } : { name, weight, base_url: baseURL, control_url: controlURL, pull_ip: pullIP };
      await api.patch(path, body);
      toast("Değişiklikler kaydedildi.");
      onChanged();
    });
  };

  const setEnabled = async (enabled: boolean) => {
    const sure = await confirm(
      enabled
        ? { title: `"${edge.name}" yeniden etkinleştirilsin mi?`, confirmLabel: "Etkinleştir" }
        : {
            title: `"${edge.name}" devre dışı bırakılsın mı?`,
            body: "Yeni izleyici gönderilmez; süren izlemeler devam eder.",
            confirmLabel: "Devre dışı bırak",
          },
    );
    if (!sure) return;
    void action.run(async () => {
      await api.patch(path, { enabled });
      toast(enabled ? "Sunucu etkinleştirildi." : "Sunucu devre dışı bırakıldı.");
      onChanged();
    });
  };

  const remove = async () => {
    const sure = await confirm({
      title: `"${edge.name}" silinsin mi?`,
      body: "Bu sunucuda süren izlemeler kesilmez; önce devre dışı bırakıp boşalmasını bekleyin.",
      confirmLabel: "Sil",
      danger: true,
    });
    if (!sure) return;
    void action.run(async () => {
      await api.del(path);
      toast("Sunucu silindi.");
      onDeleted();
    });
  };

  const copySetup = async () => {
    try {
      await navigator.clipboard.writeText(edge.setup ?? "");
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // pano kullanılamıyorsa metin zaten görünür; elle kopyalanabilir
    }
  };

  return (
    <Panel title={`Sunucu: ${edge.name}`} onClose={onClose}>
      <p className="drawer-status">
        <EdgeStatus edge={edge} />
        <span className="muted small">{edge.active_sessions} izleme</span>
      </p>
      <form className="stack" onSubmit={save}>
        <div className="row">
          <label>
            Ad
            <input required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label>
            Ağırlık (1-1000)
            <input type="number" min={1} max={1000} required value={weight} onChange={(e) => setWeight(Number(e.target.value))} />
          </label>
        </div>
        {edge.builtin ? (
          <p className="muted small">
            Bu, panelin çalıştığı sunucudur. Adresleri sunucu ayarlarından gelir: MPEG-TS <code>{edge.base_url}</code>, HLS{" "}
            <code>{edge.hls_base_url}</code>.
          </p>
        ) : (
          <div className="row">
            <label>
              İzleyici adresi
              <input required value={baseURL} onChange={(e) => setBaseURL(e.target.value)} />
            </label>
            <label>
              Yönetim adresi
              <input required value={controlURL} onChange={(e) => setControlURL(e.target.value)} />
            </label>
            <label>
              Sunucunun IP adresi
              <input required value={pullIP} onChange={(e) => setPullIP(e.target.value)} />
            </label>
          </div>
        )}
        <ErrorNote message={action.error} />
        <div className="drawer-actions">
          <button type="submit" disabled={action.busy}>
            Kaydet
          </button>
          {edge.enabled ? (
            <button type="button" className="ghost" disabled={action.busy} onClick={() => void setEnabled(false)}>
              Devre dışı bırak
            </button>
          ) : (
            <button type="button" className="ghost" disabled={action.busy} onClick={() => void setEnabled(true)}>
              Yeniden etkinleştir
            </button>
          )}
          {!edge.builtin && (
            <button type="button" className="ghost danger-text" disabled={action.busy} onClick={() => void remove()}>
              Sil
            </button>
          )}
        </div>
      </form>

      {edge.setup && (
        <div className="setup">
          <h3>Kurulum</h3>
          <p className="muted small">
            Yeni sunucuya depodaki <code>deploy/edge</code> klasörünü kopyalayın, aşağıdaki satırları o klasörde <code>.env</code>{" "}
            dosyasına yazın ve <code>docker compose up -d</code> çalıştırın. Bu satırlar sunucunun anahtarını içerir; gizli tutun.
          </p>
          <pre>{edge.setup}</pre>
          <button type="button" className="ghost" onClick={() => void copySetup()}>
            {copied ? "Kopyalandı" : "Kopyala"}
          </button>
        </div>
      )}
    </Panel>
  );
}
