import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { type Account, ApiError, api, sessionEndedEvent } from "./api";
import { AdminTenants } from "./admin";
import { AdminEdges } from "./edges";
import { Categories, Channels, OverviewPage, Sessions, Viewers } from "./tenant";
import { Avatar, ErrorNote, FeedbackProvider, Icon, type IconName, Logo, Panel, navigate, useAction, useFeedback, useTheme } from "./ui";

interface Page {
  path: string;
  title: string;
  // hint, başlığın altında sayfanın ne işe yaradığını tek cümleyle söyler.
  hint: string;
  icon: IconName;
  render: () => ReactNode;
}

const adminPages: Page[] = [
  { path: "/yayincilar", title: "Yayıncılar", hint: "Platformdaki yayıncı hesapları, kotaları ve kullanımları.", icon: "tenants", render: () => <AdminTenants /> },
  { path: "/sunucular", title: "Sunucular", hint: "İzleyicilerin yayını aldığı sunucular ve durumları.", icon: "servers", render: () => <AdminEdges /> },
];

const tenantPages: Page[] = [
  { path: "/genel", title: "Genel bakış", hint: "Kullanımınız ve bağlantı bilgileriniz.", icon: "overview", render: () => <OverviewPage /> },
  { path: "/kanallar", title: "Kanallar", hint: "Kanallarınız ve OBS yayın ayarları.", icon: "channels", render: () => <Channels /> },
  { path: "/kategoriler", title: "Kategoriler", hint: "Kanalların oynatıcıda göründüğü gruplar.", icon: "categories", render: () => <Categories /> },
  { path: "/izleyiciler", title: "İzleyiciler", hint: "İzleyici hesapları ve giriş bilgileri.", icon: "viewers", render: () => <Viewers /> },
  { path: "/oturumlar", title: "Oturumlar", hint: "Şu an süren izlemeler.", icon: "sessions", render: () => <Sessions /> },
];

const passwordPage = "/sifre";

function usePath(): string {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname);
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  return path;
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
  return (
    <FeedbackProvider>
      <Shell account={account} onLogout={() => setAccount(null)} />
    </FeedbackProvider>
  );
}

function Login({ onLogin }: { onLogin: (a: Account) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const action = useAction();

  useEffect(() => {
    document.title = "Giriş · Kanalvo";
  }, []);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      onLogin(await api.post<Account>("/api/login", { email, password }));
    });
  };

  return (
    <main className="center login-page">
      <form className="login" onSubmit={submit}>
        <div className="login-brand">
          <Logo size={44} />
          <div>
            <h1>Kanalvo</h1>
            <p className="muted">Yönetim paneline giriş yapın.</p>
          </div>
        </div>
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

const themeLabels = { auto: "Tema: sistem", light: "Tema: açık", dark: "Tema: koyu" } as const;
const themeIcons = { auto: "auto", light: "sun", dark: "moon" } as const;

function Shell({ account, onLogout }: { account: Account; onLogout: () => void }) {
  const pages = account.role === "admin" ? adminPages : tenantPages;
  const path = usePath();
  const current = path === passwordPage ? null : (pages.find((p) => p.path === path) ?? pages[0]);
  const [theme, cycleTheme] = useTheme();
  const title = current?.title ?? "Şifre değiştir";

  useEffect(() => {
    document.title = `${title} · Kanalvo`;
  }, [title]);

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
        <div className="brand">
          <Logo />
          Kanalvo
        </div>
        <ul>
          {pages.map((p) => (
            <li key={p.path}>
              <a
                href={p.path}
                className={current?.path === p.path ? "active" : ""}
                aria-current={current?.path === p.path ? "page" : undefined}
                onClick={(e) => {
                  e.preventDefault();
                  navigate(p.path);
                }}
              >
                <Icon name={p.icon} />
                {p.title}
              </a>
            </li>
          ))}
        </ul>
        <div className="account">
          <div className="account-who">
            <Avatar name={account.name} />
            <div>
              <div className="account-name" title={account.email}>
                {account.name}
              </div>
              <div className="account-role">{account.role === "admin" ? "Platform yöneticisi" : "Yayıncı"}</div>
            </div>
          </div>
          <div className="account-actions">
            <button type="button" className="icon-button" title={themeLabels[theme]} aria-label={themeLabels[theme]} onClick={cycleTheme}>
              <Icon name={themeIcons[theme]} />
            </button>
            <button
              type="button"
              className={`icon-button${current ? "" : " active"}`}
              title="Şifre değiştir"
              aria-label="Şifre değiştir"
              onClick={() => navigate(passwordPage)}
            >
              <Icon name="key" />
            </button>
            <button type="button" className="icon-button" title="Çıkış yap" aria-label="Çıkış yap" onClick={logout}>
              <Icon name="logout" />
            </button>
          </div>
        </div>
      </nav>
      <main className="content">
        <header className="page-head">
          <h1>{title}</h1>
          <p className="muted">{current?.hint ?? "Panele girerken kullandığınız şifre."}</p>
        </header>
        {current ? current.render() : <ChangePassword />}
      </main>
    </div>
  );
}

function ChangePassword() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const action = useAction();
  const { toast } = useFeedback();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void action.run(async () => {
      await api.post("/api/password", { current, new: next });
      setCurrent("");
      setNext("");
      toast("Şifreniz değişti. Diğer cihazlardaki oturumlarınız kapatıldı.");
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
        <button type="submit" disabled={action.busy}>
          Şifreyi değiştir
        </button>
      </form>
    </Panel>
  );
}
