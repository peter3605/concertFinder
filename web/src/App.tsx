import { useEffect, useState } from 'react';

type Me = { id: string; spotify_user_id: string; display_name: string };

type TicketLink = { source: string; url: string };
type Artist = { id: string; name: string };
type Concert = {
  artist: Artist;
  date: string;
  venue: string;
  city: string;
  state?: string;
  country?: string;
  links: TicketLink[];
  dedup_key: string;
};

type Location = { latitude: number; longitude: number; radius_miles: number };
type ConcertsResponse = { location: Location; count: number; concerts: Concert[] };

type State =
  | { kind: 'loading' }
  | { kind: 'anon' }
  | { kind: 'signed_in'; me: Me }
  | { kind: 'error'; message: string };

type ConcertsState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'loaded'; data: ConcertsResponse }
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

async function fetchConcerts(): Promise<ConcertsState> {
  try {
    const r = await fetch('/api/me/concerts', { credentials: 'same-origin' });
    if (!r.ok) return { kind: 'error', message: `HTTP ${r.status}` };
    return { kind: 'loaded', data: (await r.json()) as ConcertsResponse };
  } catch (e) {
    return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
  }
}

function groupByMonth(concerts: Concert[]): { key: string; label: string; items: Concert[] }[] {
  const groups = new Map<string, Concert[]>();
  for (const c of concerts) {
    const d = new Date(c.date);
    const key = `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(c);
  }
  const sortedKeys = Array.from(groups.keys()).sort();
  return sortedKeys.map((key) => {
    const [year, month] = key.split('-').map(Number);
    const label = new Date(Date.UTC(year, month - 1, 1)).toLocaleString(undefined, {
      month: 'long',
      year: 'numeric',
      timeZone: 'UTC',
    });
    return { key, label, items: groups.get(key)! };
  });
}

function formatDay(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });
}

function sourceLabel(source: string): string {
  switch (source) {
    case 'ticketmaster': return 'Ticketmaster';
    case 'bandsintown': return 'Bandsintown';
    case 'official': return 'Official';
    case 'songkick': return 'Songkick';
    default: return source;
  }
}

export default function App() {
  const [state, setState] = useState<State>({ kind: 'loading' });
  const [concerts, setConcerts] = useState<ConcertsState>({ kind: 'idle' });

  useEffect(() => {
    fetchMe().then(setState);
  }, []);

  useEffect(() => {
    if (state.kind === 'signed_in') {
      setConcerts({ kind: 'loading' });
      fetchConcerts().then(setConcerts);
    }
  }, [state.kind]);

  async function logout() {
    await fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' });
    setState({ kind: 'anon' });
    setConcerts({ kind: 'idle' });
  }

  return (
    <main>
      <header>
        <h1>ConcertFinder</h1>
        {state.kind === 'signed_in' && (
          <div className="user">
            <span>{state.me.display_name || state.me.spotify_user_id}</span>
            <button onClick={logout}>Log out</button>
          </div>
        )}
      </header>

      {state.kind === 'loading' && <p>Loading...</p>}
      {state.kind === 'anon' && (
        <a href="/api/auth/login"><button>Log in with Spotify</button></a>
      )}
      {state.kind === 'error' && <p>Error: {state.message}</p>}

      {state.kind === 'signed_in' && (
        <section>
          {concerts.kind === 'loading' && <p>Finding your shows...</p>}
          {concerts.kind === 'error' && <p>Error: {concerts.message}</p>}
          {concerts.kind === 'loaded' && (
            <>
              <p className="meta">
                {concerts.data.count} show{concerts.data.count === 1 ? '' : 's'} within{' '}
                {concerts.data.location.radius_miles} miles
              </p>
              {concerts.data.count === 0 && <p>No matches yet. Try widening your radius in .env.</p>}
              {groupByMonth(concerts.data.concerts).map((g) => (
                <div key={g.key} className="month">
                  <h2>{g.label}</h2>
                  <ul>
                    {g.items.map((c) => (
                      <li key={c.dedup_key}>
                        <div className="row">
                          <div className="who">
                            <strong>{c.artist.name}</strong>
                            <span className="day"> · {formatDay(c.date)}</span>
                          </div>
                          <div className="where">
                            {c.venue}, {c.city}{c.state ? `, ${c.state}` : ''}
                          </div>
                          <div className="links">
                            {c.links.map((l) => (
                              <a key={l.url} href={l.url} target="_blank" rel="noreferrer">
                                {sourceLabel(l.source)}
                              </a>
                            ))}
                          </div>
                        </div>
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </>
          )}
        </section>
      )}

      <footer>
        <small>Powered by Spotify</small>
      </footer>
    </main>
  );
}
