import { type FormEvent, type ReactNode, createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { ApiError } from "./api";

// --- simgeler ---

const iconPaths = {
  tenants: "M3 21h18M5 21V5a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v16M15 9h2a2 2 0 0 1 2 2v10M8 7h4M8 11h4M8 15h4",
  servers: "M4 5h16v6H4zM4 13h16v6H4zM7.5 8h.01M7.5 16h.01",
  overview: "M4 4h7v9H4zM13 4h7v5h-7zM13 11h7v9h-7zM4 15h7v5H4z",
  channels: "M3 7h18v12H3zM8 3l4 4 4-4",
  categories: "M4 6h7v5H4zM13 6h7v5h-7zM4 13h7v5H4zM13 13h7v5h-7z",
  viewers: "M16 20v-1a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v1M9.5 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM21 20v-1a4 4 0 0 0-3-3.85M16 4.15a3.5 3.5 0 0 1 0 6.7",
  sessions: "M3 12h4l3-8 4 16 3-8h4",
  close: "M6 6l12 12M18 6L6 18",
  key: "M15 7a4 4 0 1 1-3.87 5H8v3H5v3H2v-3l7.13-7.13A4 4 0 0 1 15 7zM16.5 8.5h.01",
  logout: "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9",
  sun: "M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 2v2M12 20v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M2 12h2M20 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4",
  moon: "M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z",
  auto: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 3v18M12 7l5.5 5.5M12 12l4 4",
  check: "M5 12.5l4.5 4.5L19 7.5",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-3.5-3.5",
  empty: "M3 8l3-4h12l3 4v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2zM3 8h5l1.5 3h5L16 8h5",
  broadcast: "M12 14a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM7.8 16.2a6 6 0 0 1 0-8.4M16.2 7.8a6 6 0 0 1 0 8.4M4.9 19.1a10 10 0 0 1 0-14.2M19.1 4.9a10 10 0 0 1 0 14.2",
} as const;

export type IconName = keyof typeof iconPaths;

export function Icon({ name, size = 18 }: { name: IconName; size?: number }) {
  return (
    <svg
      className="icon"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={iconPaths[name]} />
    </svg>
  );
}

export function Logo({ size = 28 }: { size?: number }) {
  return (
    <svg className="logo" width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <rect width="32" height="32" rx="9" fill="var(--brand)" />
      <path d="M13.5 10.5v11l8.5-5.5z" fill="#fff" />
      <path d="M8.5 11.5a8 8 0 0 0 0 9" fill="none" stroke="#fff" strokeOpacity=".6" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

// --- gezinme ---

// navigate, sayfayı yeniden yüklemeden panelin başka bir sayfasına geçer.
export function navigate(to: string) {
  window.history.pushState(null, "", to);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

// --- tema ---

export type Theme = "auto" | "light" | "dark";
const themeKey = "kanalvo-tema";

function storedTheme(): Theme {
  try {
    const v = window.localStorage.getItem(themeKey);
    if (v === "light" || v === "dark") return v;
  } catch {
    // depolama kapalıysa sistem teması kullanılır
  }
  return "auto";
}

export function applyStoredTheme() {
  document.documentElement.dataset.theme = storedTheme();
}

export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(storedTheme);
  const cycle = () => {
    const next: Theme = theme === "auto" ? "light" : theme === "light" ? "dark" : "auto";
    try {
      if (next === "auto") window.localStorage.removeItem(themeKey);
      else window.localStorage.setItem(themeKey, next);
    } catch {
      // seçim yalnızca bu oturumda geçerli olur
    }
    document.documentElement.dataset.theme = next;
    setTheme(next);
  };
  return [theme, cycle];
}

// --- veri yükleme ---

// useLoad, bir API çağrısının sonucunu tutar. refreshMs verilirse sonucu o aralıkla yeniler;
// deps'teki bir değer (ör. arama metni, sayfa) değişince yeniden yükler. Yanıtlar sırasız
// gelebilir: yalnızca en son başlatılan isteğin sonucu gösterilir.
export function useLoad<T>(load: () => Promise<T>, refreshMs?: number, deps: readonly unknown[] = []) {
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const loader = useRef(load);
  loader.current = load;
  const latest = useRef(0);
  const pending = useRef(0);

  const reload = useCallback(async () => {
    const mine = ++latest.current;
    pending.current++;
    try {
      const result = await loader.current();
      if (mine !== latest.current) return;
      setData(result);
      setError(null);
    } catch (e) {
      if (mine === latest.current) setError(messageOf(e));
    } finally {
      pending.current--;
    }
  }, []);

  useEffect(() => {
    void reload();
    if (!refreshMs) return;
    // Süren bir istek varken yenisi başlatılmaz: yavaş bir bağlantıda her yenileme bir öncekinin
    // yanıtını geçersiz kılar ve liste hiç güncellenmezdi.
    const timer = window.setInterval(() => {
      if (pending.current === 0) void reload();
    }, refreshMs);
    return () => window.clearInterval(timer);
  }, [reload, refreshMs, ...deps]);

  return { data, error, reload };
}

export function messageOf(e: unknown): string {
  return e instanceof ApiError ? e.message : "Beklenmeyen bir hata oluştu.";
}

// useAction, bir işlemi çalıştırırken "meşgul" ve hata durumunu tutar.
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (e) {
      setError(messageOf(e));
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, run, clearError: () => setError(null) };
}

// --- pencereler, onay ve bildirimler ---

// Modal, sayfanın üstünde açılan bir penceredir: "dialog" ortada, "drawer" sağdan açılır.
// Esc ve pencerenin dışına tıklamak kapatır; odak pencerenin içinde kalır.
export function Modal({
  title,
  children,
  onClose,
  variant = "dialog",
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  variant?: "dialog" | "drawer";
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
    return () => d?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className={`modal ${variant}`}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onMouseDown={(e) => {
        if (e.target === ref.current) onClose();
      }}
    >
      <div className="modal-body">
        <header>
          <h2>{title}</h2>
          <button type="button" className="icon-button" aria-label="Kapat" onClick={onClose}>
            <Icon name="close" />
          </button>
        </header>
        {children}
      </div>
    </dialog>
  );
}

export interface ConfirmOptions {
  title: string;
  body?: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  // input verilirse pencere bir metin sorar; confirm yerine ask kullanılır.
  input?: { label: string; initial: string; maxLength?: number };
}

interface Feedback {
  // confirm, kullanıcı onaylarsa true döner.
  confirm: (o: ConfirmOptions) => Promise<boolean>;
  // ask, kullanıcının yazdığı metni döner; vazgeçerse null.
  ask: (o: ConfirmOptions) => Promise<string | null>;
  // toast, kısa süre görünen bir "işlem tamam" bildirimi gösterir.
  toast: (text: string) => void;
}

const FeedbackContext = createContext<Feedback | null>(null);

export function useFeedback(): Feedback {
  const f = useContext(FeedbackContext);
  if (!f) throw new Error("FeedbackProvider eksik");
  return f;
}

export function FeedbackProvider({ children }: { children: ReactNode }) {
  const [question, setQuestion] = useState<(ConfirmOptions & { resolve: (v: string | null) => void }) | null>(null);
  const [toasts, setToasts] = useState<{ id: number; text: string }[]>([]);
  const nextId = useRef(0);
  const toastBox = useRef<HTMLDivElement>(null);

  const open = useCallback((o: ConfirmOptions) => new Promise<string | null>((resolve) => setQuestion({ ...o, resolve })), []);
  const feedback = useRef<Feedback>({
    confirm: async (o) => (await open(o)) !== null,
    ask: (o) => open(o),
    toast: (text) => {
      const id = ++nextId.current;
      setToasts((t) => [...t, { id, text }]);
      window.setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 2600);
    },
  });

  // Bildirimler açık bir pencerenin de üstünde görünmelidir; kutu her bildirimde en üst katmana alınır.
  useEffect(() => {
    const box = toastBox.current;
    if (!box) return;
    try {
      if (box.matches(":popover-open")) box.hidePopover();
      if (toasts.length > 0) box.showPopover();
    } catch {
      // popover desteklenmiyorsa kutu normal katmanda kalır
    }
  }, [toasts]);

  const answer = (v: string | null) => {
    question?.resolve(v);
    setQuestion(null);
  };

  return (
    <FeedbackContext.Provider value={feedback.current}>
      {children}
      {question && <Question question={question} onAnswer={answer} />}
      <div ref={toastBox} className="toasts" popover="manual" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className="toast">
            <Icon name="check" size={16} />
            {t.text}
          </div>
        ))}
      </div>
    </FeedbackContext.Provider>
  );
}

function Question({ question, onAnswer }: { question: ConfirmOptions; onAnswer: (v: string | null) => void }) {
  const [text, setText] = useState(question.input?.initial ?? "");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onAnswer(question.input ? text.trim() : "");
  };
  return (
    <Modal title={question.title} onClose={() => onAnswer(null)}>
      <form className="stack" onSubmit={submit}>
        {question.body && <div className="question-body">{question.body}</div>}
        {question.input && (
          <label>
            {question.input.label}
            <input required autoFocus maxLength={question.input.maxLength} value={text} onChange={(e) => setText(e.target.value)} />
          </label>
        )}
        <div className="modal-actions">
          <button type="button" className="ghost" onClick={() => onAnswer(null)}>
            Vazgeç
          </button>
          <button type="submit" className={question.danger ? "danger" : ""} autoFocus={!question.input}>
            {question.confirmLabel ?? "Onayla"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

// --- bileşenler ---

export function ErrorNote({ message }: { message: string | null | undefined }) {
  if (!message) return null;
  return (
    <p className="note error" role="alert">
      {message}
    </p>
  );
}

export function Loading({ error }: { error: string | null }) {
  if (error) return <ErrorNote message={error} />;
  return (
    <div className="skeleton" aria-label="Yükleniyor" role="status">
      <span />
      <span />
      <span />
    </div>
  );
}

export function Badge({ tone, children }: { tone: "ok" | "off" | "warn"; children: ReactNode }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}

export function LiveBadge() {
  return (
    <span className="badge live">
      <span className="pulse" />
      Yayında
    </span>
  );
}

// CopyField, kopyalanabilir bir değeri gösterir. secret ise değer varsayılan olarak gizlidir.
export function CopyField({ label, value, secret }: { label: string; value: string; secret?: boolean }) {
  const [shown, setShown] = useState(!secret);
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setShown(true); // pano kullanılamıyorsa değer elle kopyalanabilsin
    }
  };
  return (
    <div className="copy-field">
      <span className="copy-label">{label}</span>
      <div className="copy-value">
        <code>{shown ? value : "•".repeat(Math.min(value.length, 24))}</code>
        {secret && (
          <button type="button" className="ghost" onClick={() => setShown(!shown)}>
            {shown ? "Gizle" : "Göster"}
          </button>
        )}
        <button type="button" className={`ghost${copied ? " done" : ""}`} onClick={() => void copy()}>
          {copied ? "Kopyalandı" : "Kopyala"}
        </button>
      </div>
    </div>
  );
}

// Panel, sayfadaki bir bölümdür. onClose verilirse bölüm sayfada değil, sağdan açılan bir çekmecede
// gösterilir: uzun bir listenin altında gözden kaçmaz.
export function Panel({ title, children, onClose, aside }: { title: string; children: ReactNode; onClose?: () => void; aside?: ReactNode }) {
  if (onClose) {
    return (
      <Modal title={title} onClose={onClose} variant="drawer">
        {children}
      </Modal>
    );
  }
  return (
    <section className="panel">
      <header>
        <h2>{title}</h2>
        {aside}
      </header>
      {children}
    </section>
  );
}

export function EmptyState({ icon = "empty", title, children }: { icon?: IconName; title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <span className="empty-icon">
        <Icon name={icon} size={22} />
      </span>
      <strong>{title}</strong>
      {children && <p className="muted">{children}</p>}
    </div>
  );
}

// Stat, tek bir sayıyı gösteren karttır; limit verilirse doluluk çubuğu da çizilir.
export function Stat({ icon, label, value, limit, live }: { icon: IconName; label: string; value: number; limit?: number; live?: boolean }) {
  const ratio = limit === undefined ? 0 : limit > 0 ? Math.min(value / limit, 1) : value > 0 ? 1 : 0;
  return (
    <div className={`stat${live && value > 0 ? " live" : ""}`}>
      <span className="stat-icon">
        <Icon name={icon} size={20} />
      </span>
      <div className="stat-text">
        <div className="stat-value">
          {value}
          {limit !== undefined && <span className="stat-limit"> / {limit}</span>}
        </div>
        <div className="muted small">{label}</div>
      </div>
      {limit !== undefined && (
        <div className="meter-track" role="progressbar" aria-valuemin={0} aria-valuemax={limit} aria-valuenow={value} aria-label={label}>
          <div className={`meter-fill${ratio >= 1 ? " full" : ratio >= 0.8 ? " high" : ""}`} style={{ width: `${ratio * 100}%` }} />
        </div>
      )}
    </div>
  );
}

// Avatar, bir kaydın logosunu ya da adının baş harfini gösterir.
export function Avatar({ name, src }: { name: string; src?: string }) {
  const [broken, setBroken] = useState(false);
  let hue = 0;
  for (const ch of name) hue = (hue * 131 + ch.codePointAt(0)! * 17) % 3599;
  if (src && !broken) {
    return <img className="avatar" src={src} alt="" loading="lazy" referrerPolicy="no-referrer" onError={() => setBroken(true)} />;
  }
  return (
    <span className="avatar" style={{ background: `hsl(${hue % 360} 52% 44%)` }} aria-hidden="true">
      {(name.trim()[0] ?? "?").toLocaleUpperCase("tr-TR")}
    </span>
  );
}

// Usage, tablo hücresinde "kullanılan / kota" değerini ince bir doluluk çubuğuyla gösterir.
export function Usage({ used, limit }: { used: number; limit: number }) {
  const ratio = limit > 0 ? Math.min(used / limit, 1) : used > 0 ? 1 : 0;
  return (
    <span className="usage">
      <span>
        {used} <span className="muted">/ {limit}</span>
      </span>
      <span className="meter-track" aria-hidden="true">
        <span className={`meter-fill${ratio >= 1 ? " full" : ratio >= 0.8 ? " high" : ""}`} style={{ width: `${ratio * 100}%` }} />
      </span>
    </span>
  );
}

export function SearchBox({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  return (
    <div className="list-search">
      <Icon name="search" size={16} />
      <input type="search" placeholder={placeholder} aria-label={placeholder} value={value} maxLength={64} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}

export function formatDate(iso: string | null): string {
  if (!iso) return "Süresiz";
  return new Date(iso).toLocaleString("tr-TR", { dateStyle: "medium", timeStyle: "short" });
}

// since, bir andan bu yana geçen süreyi kısa biçimde yazar ("az önce", "12 dk", "2 sa 5 dk", "3 gün").
export function since(iso: string, now = Date.now()): string {
  const minutes = Math.floor((now - new Date(iso).getTime()) / 60000);
  if (minutes < 1) return "az önce";
  if (minutes < 60) return `${minutes} dk`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return minutes % 60 ? `${hours} sa ${minutes % 60} dk` : `${hours} sa`;
  return `${Math.floor(hours / 24)} gün`;
}

// toLocalInput / fromLocalInput: <input type="datetime-local"> ile ISO tarih arasında çevirir.
export function toLocalInput(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function fromLocalInput(value: string): string | null {
  return value ? new Date(value).toISOString().replace(/\.\d{3}Z$/, "Z") : null;
}

export const pageSize = 50;

// Pager, dilimlenen bir listenin altında hangi kayıtların göründüğünü ve sayfa düğmelerini gösterir.
export function Pager({ offset, shown, total, onMove }: { offset: number; shown: number; total: number; onMove: (offset: number) => void }) {
  if (total <= pageSize && offset === 0) return null;
  return (
    <div className="pager">
      <span className="muted">
        {shown === 0 ? `${total} kayıt` : `${total} kayıttan ${offset + 1}–${offset + shown}`}
      </span>
      <button type="button" className="ghost" disabled={offset === 0} onClick={() => onMove(Math.max(0, offset - pageSize))}>
        Önceki
      </button>
      <button type="button" className="ghost" disabled={offset + shown >= total} onClick={() => onMove(offset + pageSize)}>
        Sonraki
      </button>
    </div>
  );
}
