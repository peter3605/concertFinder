import { useEffect, useState } from 'react';

type Health =
  | { kind: 'loading' }
  | { kind: 'ok' }
  | { kind: 'error'; message: string };

export default function App() {
  const [health, setHealth] = useState<Health>({ kind: 'loading' });

  useEffect(() => {
    // /api is proxied to the Go backend's /healthz (see vite.config.ts).
    fetch('/api/healthz')
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json();
      })
      .then(() => setHealth({ kind: 'ok' }))
      .catch((e: unknown) =>
        setHealth({ kind: 'error', message: e instanceof Error ? e.message : String(e) }),
      );
  }, []);

  return (
    <main>
      <h1>ConcertFinder</h1>
      {health.kind === 'loading' && <p>Checking backend...</p>}
      {health.kind === 'ok' && <p>Backend OK</p>}
      {health.kind === 'error' && <p>Backend unreachable: {health.message}</p>}
      <footer>
        <small>Powered by Spotify</small>
      </footer>
    </main>
  );
}
