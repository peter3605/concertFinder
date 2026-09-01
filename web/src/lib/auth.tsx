import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { apiFetch, mutatingFetch, setSessionExpiredHandler } from './api';
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
  // The reactive counterpart to logout: the session is already gone, so
  // there is nothing to POST to and a request would only 401 in turn.
  signOut: () => void;
};

const AuthCtx = createContext<Ctx | null>(null);

async function fetchMe(): Promise<AuthState> {
  try {
    // publicEndpoint: a 401 here is the signed-out answer, not an expiry.
    const r = await apiFetch('/api/auth/me', {}, { publicEndpoint: true });
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
  const signOut = useCallback(() => setAuth({ kind: 'anon' }), []);
  const logout = useCallback(async () => {
    await mutatingFetch('/api/auth/logout', { method: 'POST' });
    setAuth({ kind: 'anon' });
  }, []);

  // Registered in an effect, and cleared on unmount, so api.ts can only ever
  // reach a mounted provider. RequireAuth turns the resulting 'anon' into the
  // redirect to /login for free.
  useEffect(() => {
    setSessionExpiredHandler(signOut);
    return () => setSessionExpiredHandler(null);
  }, [signOut]);

  return <AuthCtx.Provider value={{ auth, setMe, logout, signOut }}>{children}</AuthCtx.Provider>;
}

export function useAuth() {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error('useAuth must be used within AuthProvider');
  return ctx;
}
