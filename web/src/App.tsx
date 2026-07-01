import { useEffect, useState } from 'react';

type Me = { id: string; spotify_user_id: string; display_name: string };

type State =
  | { kind: 'loading' }
  | { kind: 'anon' }
  | { kind: 'signed_in'; me: Me }
  | { kind: 'error'; message: string };

async function fetchMe(): Promise<State> {
  try {
    const r = await fetch('/api/auth/me', { credentials: 'same-origin' });
    if (r.status === 401) return { kind: 'anon' };
    if (!r.ok) return { kind: 'error', message: `HTTP ${r.status}` };
    return { kind: 'signed_in', me: (await r.json()) as Me };
  } catch (e) {
    return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
  }
}

export default function App() {
  const [state, setState] = useState<State>({ kind: 'loading' });

  useEffect(() => {
    fetchMe().then(setState);
  }, []);

  async function logout() {
    await fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' });
    setState({ kind: 'anon' });
  }

  return (
    <main>
      <h1>ConcertFinder</h1>
      {state.kind === 'loading' && <p>Loading...</p>}
      {state.kind === 'anon' && (
        <a href="/api/auth/login">
          <button>Log in with Spotify</button>
        </a>
      )}
      {state.kind === 'signed_in' && (
        <>
          <p>Signed in as {state.me.display_name || state.me.spotify_user_id}</p>
          <button onClick={logout}>Log out</button>
        </>
      )}
      {state.kind === 'error' && <p>Error: {state.message}</p>}
      <footer>
        <small>Powered by Spotify</small>
      </footer>
    </main>
  );
}
