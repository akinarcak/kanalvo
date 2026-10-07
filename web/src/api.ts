// Panel API istemcisi. Değişiklik yapan her istek X-StreamHub-Panel başlığını taşır; sunucu
// bu başlık olmadan değişikliği reddeder (başka sitelerden gelen isteklere karşı).

// Sunucu oturumu artık tanımıyorsa bu olay yayılır; uygulama giriş ekranına döner.
export const sessionEndedEvent = "streamhub:session-ended";

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
  ) {
    super(message);
  }
}

// Paged, dilimlenen bir listenin istenen parçası ve toplam kayıt sayısıdır.
export interface Paged<T> {
  items: T[];
  total: number;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  return (await requestWithTotal<T>(method, path, body)).data;
}

async function requestWithTotal<T>(method: string, path: string, body?: unknown): Promise<{ data: T; total: number }> {
  const headers: Record<string, string> = {};
  if (method !== "GET") headers["X-StreamHub-Panel"] = "1";
  if (body !== undefined) headers["Content-Type"] = "application/json";

  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError("Sunucuya ulaşılamadı. Bağlantınızı kontrol edin.", 0, "network");
  }
  if (res.status === 204) return { data: undefined as T, total: 0 };

  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    // gövde JSON değilse aşağıda genel hata üretilir
  }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string } } | null)?.error;
    const sessionEnded = res.status === 401 || (res.status === 403 && err?.code === "suspended");
    if (sessionEnded && path !== "/api/login" && path !== "/api/me") window.dispatchEvent(new Event(sessionEndedEvent));
    throw new ApiError(err?.message ?? `Beklenmeyen yanıt (${res.status}).`, res.status, err?.code ?? "unknown");
  }
  return { data: data as T, total: Number(res.headers.get("X-Total-Count") ?? 0) };
}

export const api = {
  get: <T,>(path: string) => request<T>("GET", path),
  // page, limit ve offset ile dilimlenen bir listeyi toplam sayısıyla birlikte getirir.
  page: async <T,>(path: string, params: Record<string, string | number>): Promise<Paged<T>> => {
    const query = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) if (v !== "") query.set(k, String(v));
    const { data, total } = await requestWithTotal<T[]>("GET", `${path}?${query}`);
    return { items: data, total };
  },
  post: <T,>(path: string, body?: unknown) => request<T>("POST", path, body),
  patch: <T,>(path: string, body: unknown) => request<T>("PATCH", path, body),
  del: (path: string) => request<void>("DELETE", path),
};

export type Role = "admin" | "tenant";

export interface Account {
  role: Role;
  id: number;
  name: string;
  email: string;
}

export interface Quotas {
  max_channels: number;
  max_viewers: number;
  max_connections: number;
}

export interface Tenant {
  id: number;
  name: string;
  email: string;
  status: "active" | "suspended";
  quotas: Quotas;
  created_at: string;
  channels: number;
  viewers: number;
  live_channels: number;
  active_sessions: number;
}

export interface Stats {
  tenants: number;
  channels: number;
  live_channels: number;
  viewers: number;
  active_sessions: number;
}

export interface Overview {
  name: string;
  email: string;
  quotas: Quotas;
  usage: { channels: number; viewers: number; connections: number };
  ingest_url: string;
  xtream_url: string;
}

export interface Category {
  id: number;
  name: string;
}

export interface Channel {
  id: number;
  name: string;
  category_id: number | null;
  logo_url: string;
  live: boolean;
  created_at: string;
  ingest_url: string;
  stream_key: string;
}

export interface Viewer {
  id: number;
  username: string;
  password: string;
  status: "active" | "suspended";
  expires_at: string | null;
  max_connections: number;
  created_at: string;
  playlist_url: string;
}

export interface Session {
  id: number;
  viewer: string;
  channel: string;
  kind: "ts" | "hls";
  ip: string;
  started_at: string;
  edge: string;
}

// Edge, izleyicilerin yayını aldığı sunucudur. builtin, panelin çalıştığı sunucudur.
export interface Edge {
  id: number;
  name: string;
  builtin: boolean;
  base_url: string;
  hls_base_url: string;
  control_url: string;
  pull_ip: string;
  weight: number;
  enabled: boolean;
  healthy: boolean;
  last_seen_at: string | null;
  active_sessions: number;
  // Uzak sunucunun .env dosyasına yazılacak satırlar; yerel sunucuda yoktur.
  setup?: string;
}
