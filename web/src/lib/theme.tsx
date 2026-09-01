import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';

// Three states: 'light' | 'dark' | 'system'. The last defers to the user's
// OS preference at runtime via matchMedia. Choice is persisted in
// localStorage under `cf-theme`.
export type ThemeChoice = 'light' | 'dark' | 'system';

type Ctx = {
  theme: ThemeChoice;
  resolved: 'light' | 'dark';
  setTheme: (t: ThemeChoice) => void;
};

const ThemeCtx = createContext<Ctx | null>(null);

const STORAGE_KEY = 'cf-theme';

function readInitial(): ThemeChoice {
  if (typeof window === 'undefined') return 'system';
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === 'light' || stored === 'dark' || stored === 'system') return stored;
  return 'system';
}

function systemPrefers(): 'light' | 'dark' {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemeChoice>(readInitial);
  const [resolved, setResolved] = useState<'light' | 'dark'>(() =>
    theme === 'system' ? systemPrefers() : theme,
  );

  useEffect(() => {
    const applied = theme === 'system' ? systemPrefers() : theme;
    setResolved(applied);
    const root = document.documentElement;
    root.classList.toggle('dark', applied === 'dark');
    // If following system, listen for changes to prefers-color-scheme so a
    // user flipping their OS setting instantly re-renders the app.
    if (theme !== 'system') return;
    const mql = window.matchMedia('(prefers-color-scheme: dark)');
    const listener = () => {
      const next = mql.matches ? 'dark' : 'light';
      setResolved(next);
      root.classList.toggle('dark', next === 'dark');
    };
    mql.addEventListener('change', listener);
    return () => mql.removeEventListener('change', listener);
  }, [theme]);

  const setTheme = (t: ThemeChoice) => {
    window.localStorage.setItem(STORAGE_KEY, t);
    setThemeState(t);
  };

  return <ThemeCtx.Provider value={{ theme, resolved, setTheme }}>{children}</ThemeCtx.Provider>;
}

export function useTheme() {
  const ctx = useContext(ThemeCtx);
  if (!ctx) throw new Error('useTheme must be used within ThemeProvider');
  return ctx;
}
