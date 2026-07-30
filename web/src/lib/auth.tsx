import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { mutatingFetch } from './api';
import type { Me } from './types';

type AuthState =
  | { kind: 'loading' }
  | { kind: 'anon' }
  | { kind: 'signed_in'; me: Me }
  | { kind: 'error'; message: string };

type Ctx = {
  auth: AuthState;
  setMe: (m: Me) => void;
  logout: () => Promise<void>;
};

const AuthCtx = createContext<Ctx | null>(null);

async function fetchMe(): Promise<AuthState> {
  try {
    const r = await fetch('/api/auth/me', { credentials: 'same-origin' });
    if (r.status === 401) return { kind: 'anon' };
    if (!r.ok) return { kind: 'error', message: `HTTP ${r.status}` };
    return { kind: 'signed_in', me: (await r.json()) as Me };
  } catch (e) {
    return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [auth, setAuth] = useState<AuthState>({ kind: 'loading' });

  useEffect(() => {
    let cancelled = false;
    fetchMe().then((a) => {
      if (!cancelled) setAuth(a);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const setMe = useCallback((m: Me) => setAuth({ kind: 'signed_in', me: m }), []);
  const logout = useCallback(async () => {
    await mutatingFetch('/api/auth/logout', { method: 'POST' });
    setAuth({ kind: 'anon' });
  }, []);

  return <AuthCtx.Provider value={{ auth, setMe, logout }}>{children}</AuthCtx.Provider>;
}

export function useAuth() {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error('useAuth must be used within AuthProvider');
  return ctx;
}
