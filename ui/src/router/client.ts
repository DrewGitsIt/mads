// Thin fetch wrapper shared by every surface — no ad-hoc fetches in
// components. The Router's address is exposed via VITE_ROUTER_URL; the
// default matches the Router's own `-addr` flag default (localhost:8080).
export const ROUTER_BASE =
  (import.meta.env.VITE_ROUTER_URL as string | undefined) ?? "http://localhost:8080";

async function readJSON<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`router request failed: ${res.status} ${body}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export async function routerGet<T>(
  path: string,
  params?: Record<string, string | undefined>,
): Promise<T> {
  const url = new URL(path, ROUTER_BASE);
  if (params) {
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined) url.searchParams.set(key, value);
    }
  }
  const res = await fetch(url);
  return readJSON<T>(res);
}

export async function routerPost<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(new URL(path, ROUTER_BASE), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return readJSON<T>(res);
}
