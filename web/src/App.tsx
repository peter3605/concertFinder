import { useEffect, useMemo, useState } from 'react';

type Me = { id: string; spotify_user_id: string; display_name: string };

type TicketLink = { source: string; url: string };
type Artist = { id: string; name: string; genres?: string[] };
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

type Location = { latitude: number; longitude: number; radius_miles: number; display_name?: string };
type Facet = { value: string; count: number };
type ConcertsResponse = {
  location: Location;
  count: number;
  concerts: Concert[];
  facets: { genres: Facet[] };
};

type AuthState =
  | { kind: 'loading' }
  | { kind: 'anon' }
  | { kind: 'signed_in'; me: Me }
  | { kind: 'error'; message: string };

type ConcertsState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'loaded'; data: ConcertsResponse }
  | { kind: 'error'; message: string };

type Weekday = 'all' | 'weekday' | 'weekend';

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

function buildQuery(f: { genre?: string; dateFrom?: string; dateTo?: string; weekday?: Weekday }): string {
  const q = new URLSearchParams();
  if (f.genre) q.set('genre', f.genre);
  if (f.dateFrom) q.set('date_from', f.dateFrom);
  if (f.dateTo) q.set('date_to', f.dateTo);
  if (f.weekday && f.weekday !== 'all') q.set('weekday', f.weekday);
  const s = q.toString();
  return s ? `?${s}` : '';
}

async function fetchConcerts(query: string): Promise<ConcertsState> {
  try {
    const r = await fetch(`/api/me/concerts${query}`, { credentials: 'same-origin' });
    if (!r.ok) return { kind: 'error', message: `HTTP ${r.status}` };
    return { kind: 'loaded', data: (await r.json()) as ConcertsResponse };
  } catch (e) {
    return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
  }
}

function groupByMonth(concerts: Concert[]) {
  const groups = new Map<string, Concert[]>();
  for (const c of concerts) {
    const d = new Date(c.date);
    const key = `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(c);
  }
  return Array.from(groups.keys())
    .sort()
    .map((key) => {
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
  return new Date(iso).toLocaleDateString(undefined, {
    weekday: 'short', month: 'short', day: 'numeric',
  });
}

const sourceLabels: Record<string, string> = {
  ticketmaster: 'Ticketmaster',
  bandsintown: 'Bandsintown',
  official: 'Official',
  songkick: 'Songkick',
};

function LocationBar({ location, onSaved }: { location: Location; onSaved: (loc: Location) => void }) {
  const [editing, setEditing] = useState(false);
  const [query, setQuery] = useState('');
  const [radius, setRadius] = useState(location.radius_miles);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  async function save() {
    setSaving(true);
    setErr('');
    try {
      const body = query
        ? { query, radius_miles: radius }
        : { latitude: location.latitude, longitude: location.longitude, radius_miles: radius };
      const r = await fetch('/api/me/location', {
        method: 'PUT',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        setErr(await r.text());
        setSaving(false);
        return;
      }
      const loc = (await r.json()) as Location;
      onSaved(loc);
      setEditing(false);
      setQuery('');
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  if (!editing) {
    return (
      <div className="location-bar">
        <span>
          Within {location.radius_miles} mi of{' '}
          {location.display_name ?? `${location.latitude.toFixed(2)}, ${location.longitude.toFixed(2)}`}
        </span>
        <button className="linkish" onClick={() => setEditing(true)}>Change</button>
      </div>
    );
  }
  return (
    <div className="location-bar editing">
      <input
        type="text"
        placeholder="City, state"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        autoFocus
      />
      <label>
        Radius
        <input
          type="number"
          min={1}
          max={500}
          value={radius}
          onChange={(e) => setRadius(Number(e.target.value))}
        />
        mi
      </label>
      <button onClick={save} disabled={saving}>Save</button>
      <button className="linkish" onClick={() => { setEditing(false); setErr(''); }}>Cancel</button>
      {err && <span className="err">{err}</span>}
    </div>
  );
}

type FiltersState = {
  genre: string;
  dateFrom: string;
  dateTo: string;
  weekday: Weekday;
};

function FilterBar({
  filters,
  facets,
  onChange,
}: {
  filters: FiltersState;
  facets: Facet[];
  onChange: (f: FiltersState) => void;
}) {
  const topGenres = useMemo(() => facets.slice(0, 12), [facets]);
  return (
    <div className="filter-bar">
      <div className="genres">
        <button
          className={filters.genre === '' ? 'pill active' : 'pill'}
          onClick={() => onChange({ ...filters, genre: '' })}
        >
          All genres
        </button>
        {topGenres.map((f) => (
          <button
            key={f.value}
            className={filters.genre === f.value ? 'pill active' : 'pill'}
            onClick={() => onChange({ ...filters, genre: f.value })}
          >
            {f.value} <em>({f.count})</em>
          </button>
        ))}
      </div>
      <div className="daterow">
        <label>
          From
          <input
            type="date"
            value={filters.dateFrom}
            onChange={(e) => onChange({ ...filters, dateFrom: e.target.value })}
          />
        </label>
        <label>
          To
          <input
            type="date"
            value={filters.dateTo}
            onChange={(e) => onChange({ ...filters, dateTo: e.target.value })}
          />
        </label>
        <label>
          Days
          <select
            value={filters.weekday}
            onChange={(e) => onChange({ ...filters, weekday: e.target.value as Weekday })}
          >
            <option value="all">Any</option>
            <option value="weekday">Weekday</option>
            <option value="weekend">Weekend (Fri–Sun)</option>
          </select>
        </label>
      </div>
    </div>
  );
}

export default function App() {
  const [auth, setAuth] = useState<AuthState>({ kind: 'loading' });
  const [location, setLocation] = useState<Location | null>(null);
  const [concerts, setConcerts] = useState<ConcertsState>({ kind: 'idle' });
  const [filters, setFilters] = useState<FiltersState>({ genre: '', dateFrom: '', dateTo: '', weekday: 'all' });

  useEffect(() => { fetchMe().then(setAuth); }, []);

  useEffect(() => {
    if (auth.kind !== 'signed_in') return;
    fetch('/api/me/location', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((l: Location | null) => setLocation(l));
  }, [auth.kind]);

  useEffect(() => {
    if (auth.kind !== 'signed_in') return;
    setConcerts({ kind: 'loading' });
    const q = buildQuery({
      genre: filters.genre,
      dateFrom: filters.dateFrom,
      dateTo: filters.dateTo,
      weekday: filters.weekday,
    });
    fetchConcerts(q).then(setConcerts);
  }, [auth.kind, filters, location?.latitude, location?.longitude, location?.radius_miles]);

  async function logout() {
    await fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' });
    setAuth({ kind: 'anon' });
    setConcerts({ kind: 'idle' });
    setLocation(null);
  }

  return (
    <main>
      <header>
        <h1>ConcertFinder</h1>
        {auth.kind === 'signed_in' && (
          <div className="user">
            <span>{auth.me.display_name || auth.me.spotify_user_id}</span>
            <button onClick={logout}>Log out</button>
          </div>
        )}
      </header>

      {auth.kind === 'loading' && <p>Loading...</p>}
      {auth.kind === 'anon' && (
        <a href="/api/auth/login"><button>Log in with Spotify</button></a>
      )}
      {auth.kind === 'error' && <p>Error: {auth.message}</p>}

      {auth.kind === 'signed_in' && location && (
        <LocationBar location={location} onSaved={setLocation} />
      )}

      {auth.kind === 'signed_in' && concerts.kind === 'loaded' && (
        <FilterBar
          filters={filters}
          facets={concerts.data.facets.genres}
          onChange={setFilters}
        />
      )}

      {auth.kind === 'signed_in' && (
        <section>
          {concerts.kind === 'loading' && <p>Finding your shows...</p>}
          {concerts.kind === 'error' && <p>Error: {concerts.message}</p>}
          {concerts.kind === 'loaded' && (
            <>
              <p className="meta">
                {concerts.data.count} show{concerts.data.count === 1 ? '' : 's'} match
              </p>
              {concerts.data.count === 0 && <p>No matches. Try clearing filters or widening the radius.</p>}
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
                          {c.artist.genres && c.artist.genres.length > 0 && (
                            <div className="tags">
                              {c.artist.genres.slice(0, 3).map((g) => (
                                <span key={g} className="tag">{g}</span>
                              ))}
                            </div>
                          )}
                          <div className="links">
                            {c.links.map((l) => (
                              <a key={l.url} href={l.url} target="_blank" rel="noreferrer">
                                {sourceLabels[l.source] ?? l.source}
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

      <footer><small>Powered by Spotify</small></footer>
    </main>
  );
}
