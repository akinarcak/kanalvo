import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { type Account, ApiError, api, sessionEndedEvent } from "./api";
import { AdminTenants } from "./admin";
import { Categories, Channels, OverviewPage, Sessions, Viewers } from "./tenant";
import { ErrorNote, Panel, useAction } from "./ui";

interface Page {
  path: string;
  title: string;
  render: () => ReactNode;
}

const adminPages: Page[] = [{ path: "/yayincilar", title: "Yayıncılar", render: () => <AdminTenants /> }];

const tenantPages: Page[] = [
  { path: "/genel", title: "Genel bakış", render: () => <OverviewPage /> },
  { path: "/kanallar", title: "Kanallar", render: () => <Channels /> },
  { path: "/kategoriler", title: "Kategoriler", render: () => <Categories /> },
  { path: "/izleyiciler", title: "İzleyiciler", render: () => <Viewers /> },
  { path: "/oturumlar", title: "Oturumlar", render: () => <Sessions /> },
];

const passwordPage = "/sifre";

function usePath(): [string, (to: string) => void] {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname);
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  const go = (to: string) => {
    window.history.pushState(null, "", to);
    setPath(to);
  };
  return [path, go];
}

export function App() {
  // undefined: oturum denetleniyor; null: giriş yapılmamış.
  const [account, setAccount] = useState<Account | null | undefined>(undefined);
  const [bootError, setBootError] = useState<string | null>(null);

  useEffect(() => {
    api
      .get<Account>("/api/me")
      .then(setAccount)
      .catch((e: unknown) => {
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) setAccount(null);
        else setBootError(e instanceof ApiError ? e.message : "Panel yüklenemedi.");
      });
  }, []);

  useEffect(() => {
    const onEnded = () => setAccount(null);
    window.addEventListener(sessionEndedEvent, onEnded);
    return () => window.removeEventListener(sessionEndedEvent, onEnded);
  }, []);

  if (bootError) {
    return (
      <main className="center">
        <ErrorNote message={bootError} />
      </main>
    );
  }
  if (account === undefined) return <main className="center muted">Yükleniyor…</main>;
  if (account === null) return <Login onLogin={setAccount} />;
  return <Shell account={account} onLogout={() => setAccount(null)} />;
}

function Login({ onLogin }: { onLogin: (a: Account) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const action = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      onLogin(await api.post<Account>("/api/login", { email, password }));
    });
  };

  return (
    <main className="center">
      <form className="login" onSubmit={submit}>
        <h1>StreamHub</h1>
        <p className="muted">Yönetim paneline giriş yapın.</p>
        <label>
          E-posta
          <input type="email" autoComplete="username" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <label>
          Şifre
          <input type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        <ErrorNote message={action.error} />
        <button type="submit" disabled={action.busy}>
          {action.busy ? "Giriş yapılıyor…" : "Giriş yap"}
        </button>
      </form>
    </main>
  );
}

function Shell({ account, onLogout }: { account: Account; onLogout: () => void }) {
  const pages = account.role === "admin" ? adminPages : tenantPages;
  const [path, go] = usePath();
  const current = path === passwordPage ? null : (pages.find((p) => p.path === path) ?? pages[0]);

  // Çıkış isteği başarısız olsa da (ör. oturum zaten kapanmış) giriş ekranına dönülür.
  const logout = () => {
    api
      .post("/api/logout")
      .catch(() => undefined)
      .finally(onLogout);
  };

  return (
    <div className="shell">
      <nav className="sidebar" aria-label="Ana menü">
        <div className="brand">StreamHub</div>
        <ul>
          {pages.map((p) => (
            <li key={p.path}>
              <a
                href={p.path}
                className={current?.path === p.path ? "active" : ""}
                aria-current={current?.path === p.path ? "page" : undefined}
                onClick={(e) => {
                  e.preventDefault();
                  go(p.path);
                }}
              >
                {p.title}
              </a>
            </li>
          ))}
        </ul>
        <div className="account">
          <div className="account-name" title={account.email}>
            {account.name}
          </div>
          <div className="muted small">{account.role === "admin" ? "Platform yöneticisi" : "Yayıncı"}</div>
          <button type="button" className="ghost" onClick={() => go(passwordPage)}>
            Şifre değiştir
          </button>
          <button type="button" className="ghost" onClick={logout}>
            Çıkış yap
          </button>
        </div>
      </nav>
      <main className="content">
        {current ? (
          <>
            <h1>{current.title}</h1>
            {current.render()}
          </>
        ) : (
          <>
            <h1>Şifre değiştir</h1>
            <ChangePassword />
          </>
        )}
      </main>
    </div>
  );
}

function ChangePassword() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [done, setDone] = useState(false);
  const action = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setDone(false);
    void action.run(async () => {
      await api.post("/api/password", { current, new: next });
      setCurrent("");
      setNext("");
      setDone(true);
    });
  };

  return (
    <Panel title="Panel şifresi">
      <form className="stack narrow" onSubmit={submit}>
        <label>
          Mevcut şifre
          <input type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
        </label>
        <label>
          Yeni şifre (en az 10 karakter)
          <input type="password" autoComplete="new-password" required minLength={10} value={next} onChange={(e) => setNext(e.target.value)} />
        </label>
        <ErrorNote message={action.error} />
        {done && <p className="note ok">Şifreniz değişti. Diğer cihazlardaki oturumlarınız kapatıldı.</p>}
        <button type="submit" disabled={action.busy}>
          Şifreyi değiştir
        </button>
      </form>
    </Panel>
  );
}
