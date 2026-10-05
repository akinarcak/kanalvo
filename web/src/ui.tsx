import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "./api";

// useLoad, bir API çağrısının sonucunu tutar. refreshMs verilirse sonucu o aralıkla yeniler.
export function useLoad<T>(load: () => Promise<T>, refreshMs?: number) {
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const loader = useRef(load);
  loader.current = load;

  const reload = useCallback(async () => {
    try {
      setData(await loader.current());
      setError(null);
    } catch (e) {
      setError(messageOf(e));
    }
  }, []);

  useEffect(() => {
    void reload();
    if (!refreshMs) return;
    const timer = window.setInterval(() => void reload(), refreshMs);
    return () => window.clearInterval(timer);
  }, [reload, refreshMs]);

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

export function ErrorNote({ message }: { message: string | null | undefined }) {
  if (!message) return null;
  return (
    <p className="note error" role="alert">
      {message}
    </p>
  );
}

export function Loading({ error }: { error: string | null }) {
  return error ? <ErrorNote message={error} /> : <p className="muted">Yükleniyor…</p>;
}

export function Badge({ tone, children }: { tone: "ok" | "off" | "warn"; children: ReactNode }) {
  return <span className={`badge ${tone}`}>{children}</span>;
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
      <code>{shown ? value : "•".repeat(Math.min(value.length, 24))}</code>
      {secret && (
        <button type="button" className="ghost" onClick={() => setShown(!shown)}>
          {shown ? "Gizle" : "Göster"}
        </button>
      )}
      <button type="button" className="ghost" onClick={() => void copy()}>
        {copied ? "Kopyalandı" : "Kopyala"}
      </button>
    </div>
  );
}

export function Panel({ title, children, onClose }: { title: string; children: ReactNode; onClose?: () => void }) {
  return (
    <section className="panel">
      <header>
        <h2>{title}</h2>
        {onClose && (
          <button type="button" className="ghost" onClick={onClose}>
            Kapat
          </button>
        )}
      </header>
      {children}
    </section>
  );
}

export function Meter({ label, used, limit }: { label: string; used: number; limit: number }) {
  const ratio = limit > 0 ? Math.min(used / limit, 1) : used > 0 ? 1 : 0;
  return (
    <div className="meter">
      <div className="meter-head">
        <span>{label}</span>
        <strong>
          {used} / {limit}
        </strong>
      </div>
      <div className="meter-track" role="progressbar" aria-valuemin={0} aria-valuemax={limit} aria-valuenow={used} aria-label={label}>
        <div className={`meter-fill${ratio >= 1 ? " full" : ""}`} style={{ width: `${ratio * 100}%` }} />
      </div>
    </div>
  );
}

export function formatDate(iso: string | null): string {
  if (!iso) return "Süresiz";
  return new Date(iso).toLocaleString("tr-TR", { dateStyle: "medium", timeStyle: "short" });
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
