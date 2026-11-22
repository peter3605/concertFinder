import { useEffect, useRef, useState } from 'react';
import { Check, Loader2, Plus, Search, X } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { apiFetch, mutatingFetch, NETWORK_ERROR_MESSAGE, statusMessage } from '@/lib/api';
import type { ArtistPick, GenrePick, PickSearchResult, TMGenre } from '@/lib/types';
import { cn } from '@/lib/utils';

// The picker is debounced harder than the Spotify search on /subscribe: every
// uncached query spends a Ticketmaster permit charged to this user, and the
// server's per-user limiter is 1/s with a burst of 10.
const SEARCH_DEBOUNCE_MS = 400;

// Ticketmaster's keyword search has no prefix matching, so a two-letter query
// is a permit spent on nothing useful.
const MIN_QUERY = 2;

// Refusals specific to these endpoints. 409 is the cap (30 artists, 5 genres)
// and 429 here is the daily Ticketmaster allowance, not a burst limit.
const PICK_ERRORS: Record<number, string> = {
  409: "That's the most you can pick. Remove one to add another.",
  429: "You've used today's search allowance. Try again tomorrow.",
  502: "Ticketmaster isn't answering right now. Try again in a moment.",
};

export type PickCounts = { artists: number; genres: number };

// Artists and genres the user chose themselves, searched on Ticketmaster.
//
// Used by onboarding, where an account with no Spotify identity has no other
// source for a feed, and by Settings, where a Spotify user adds artists their
// library misses (picks are searched first, then the Spotify profile fills
// the rest). Nothing here touches Spotify, so it carries no Spotify
// attribution, whoever is looking.
export function PicksEditor({ onCountsChange }: { onCountsChange?: (c: PickCounts) => void }) {
  const [artists, setArtists] = useState<ArtistPick[]>([]);
  const [maxArtists, setMaxArtists] = useState(30);
  const [genres, setGenres] = useState<GenrePick[]>([]);
  const [maxGenres, setMaxGenres] = useState(5);
  const [catalog, setCatalog] = useState<TMGenre[] | null>(null);
  const [catalogErr, setCatalogErr] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [err, setErr] = useState('');
  // One change per ID at a time, as on /subscribe: an add and a remove for
  // the same pick in flight together let the server's ordering decide.
  const pending = useRef(new Set<string>());
  const [, force] = useState(0);

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      apiFetch('/api/me/picks').then((r) => (r.ok ? r.json() : Promise.reject(r.status))),
      apiFetch('/api/me/genres').then((r) => (r.ok ? r.json() : Promise.reject(r.status))),
    ])
      .then(([a, g]: [{ picks: ArtistPick[]; max: number }, { picks: GenrePick[]; max: number }]) => {
        if (cancelled) return;
        setArtists(a.picks ?? []);
        setMaxArtists(a.max || 30);
        setGenres(g.picks ?? []);
        setMaxGenres(g.max || 5);
      })
      .catch((status) => {
        if (!cancelled) setErr(typeof status === 'number' ? statusMessage(status) : NETWORK_ERROR_MESSAGE);
      })
      .finally(() => {
        if (!cancelled) setLoaded(true);
      });
    // The catalog is fetched separately so a Ticketmaster outage costs the
    // genre chips, not the artist list the user already has.
    apiFetch('/api/me/genres/available')
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((j: { genres: TMGenre[] }) => {
        if (!cancelled) setCatalog(j.genres ?? []);
      })
      .catch((status) => {
        if (!cancelled)
          setCatalogErr(typeof status === 'number' ? statusMessage(status, PICK_ERRORS) : NETWORK_ERROR_MESSAGE);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (loaded) onCountsChange?.({ artists: artists.length, genres: genres.length });
  }, [loaded, artists.length, genres.length, onCountsChange]);

  async function run(key: string, fn: () => Promise<void>) {
    if (pending.current.has(key)) return;
    pending.current.add(key);
    force((n) => n + 1);
    setErr('');
    try {
      await fn();
    } finally {
      pending.current.delete(key);
      force((n) => n + 1);
    }
  }

  // Adds are not optimistic: the server takes only the ID and answers with
  // Ticketmaster's name, which is the one that goes into the feed's keys.
  function addArtist(res: PickSearchResult) {
    return run(`a:${res.attraction_id}`, async () => {
      try {
        const r = await mutatingFetch('/api/me/picks', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ attraction_id: res.attraction_id }),
        });
        if (!r.ok) {
          setErr(statusMessage(r.status, PICK_ERRORS));
          return;
        }
        const pick = (await r.json()) as ArtistPick;
        setArtists((xs) => [...xs.filter((x) => x.attraction_id !== pick.attraction_id), pick]);
      } catch {
        setErr(NETWORK_ERROR_MESSAGE);
      }
    });
  }

  function removeArtist(p: ArtistPick) {
    return run(`a:${p.attraction_id}`, async () => {
      setArtists((xs) => xs.filter((x) => x.attraction_id !== p.attraction_id));
      try {
        const r = await mutatingFetch(`/api/me/picks/${encodeURIComponent(p.attraction_id)}`, {
          method: 'DELETE',
        });
        if (!r.ok) throw new Error(String(r.status));
      } catch {
        setArtists((xs) => (xs.some((x) => x.attraction_id === p.attraction_id) ? xs : [...xs, p]));
        setErr(`Couldn't remove ${p.name}. Try again.`);
      }
    });
  }

  function toggleGenre(g: TMGenre) {
    const held = genres.find((x) => x.genre_id === g.id);
    return run(`g:${g.id}`, async () => {
      if (held) {
        setGenres((xs) => xs.filter((x) => x.genre_id !== g.id));
        try {
          const r = await mutatingFetch(`/api/me/genres/${encodeURIComponent(g.id)}`, { method: 'DELETE' });
          if (!r.ok) throw new Error(String(r.status));
        } catch {
          setGenres((xs) => (xs.some((x) => x.genre_id === g.id) ? xs : [...xs, held]));
          setErr(`Couldn't remove ${g.name}. Try again.`);
        }
        return;
      }
      try {
        const r = await mutatingFetch('/api/me/genres', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ genre_id: g.id }),
        });
        if (!r.ok) {
          setErr(statusMessage(r.status, PICK_ERRORS));
          return;
        }
        const pick = (await r.json()) as GenrePick;
        setGenres((xs) => [...xs.filter((x) => x.genre_id !== pick.genre_id), pick]);
      } catch {
        setErr(NETWORK_ERROR_MESSAGE);
      }
    });
  }

  if (!loaded) return <p className="text-sm text-muted-foreground">Loading your picks…</p>;

  const pickedIDs = new Set(artists.map((a) => a.attraction_id));
  const atArtistCap = artists.length >= maxArtists;

  return (
    <div className="flex flex-col gap-6">
      {err && (
        <p role="alert" className="text-sm text-destructive">
          {err}
        </p>
      )}

      <section className="flex flex-col gap-3" aria-labelledby="picks-artists">
        <div className="flex items-baseline justify-between">
          <h3 id="picks-artists" className="font-medium">
            Artists
          </h3>
          <span className="text-xs text-muted-foreground">
            {artists.length} of {maxArtists}
          </span>
        </div>
        {artists.length > 0 && (
          <ul className="flex flex-wrap gap-2" aria-label="Artists you picked">
            {artists.map((a) => (
              <li key={a.attraction_id}>
                <Badge variant="secondary" className="gap-1 py-1 pl-2.5 pr-1 text-sm font-normal">
                  {a.name}
                  <button
                    type="button"
                    onClick={() => removeArtist(a)}
                    disabled={pending.current.has(`a:${a.attraction_id}`)}
                    aria-label={`Remove ${a.name}`}
                    className="rounded-full p-0.5 hover:bg-background/60"
                  >
                    <X className="h-3.5 w-3.5" />
                  </button>
                </Badge>
              </li>
            ))}
          </ul>
        )}
        <ArtistSearch
          pickedIDs={pickedIDs}
          disabled={atArtistCap}
          isPending={(id) => pending.current.has(`a:${id}`)}
          onAdd={addArtist}
        />
      </section>

      <section className="flex flex-col gap-3" aria-labelledby="picks-genres">
        <div className="flex items-baseline justify-between">
          <h3 id="picks-genres" className="font-medium">
            Genres
          </h3>
          <span className="text-xs text-muted-foreground">
            {genres.length} of {maxGenres}
          </span>
        </div>
        <p className="text-sm text-muted-foreground">
          Shows in these genres near you, whoever is playing.
        </p>
        {catalogErr && <p className="text-sm text-destructive">{catalogErr}</p>}
        {!catalog && !catalogErr && <p className="text-sm text-muted-foreground">Loading genres…</p>}
        {catalog && (
          <div className="flex flex-wrap gap-2" role="group" aria-label="Genres">
            {catalog.map((g) => {
              const on = genres.some((x) => x.genre_id === g.id);
              const full = !on && genres.length >= maxGenres;
              return (
                <button
                  key={g.id}
                  type="button"
                  aria-pressed={on}
                  disabled={full || pending.current.has(`g:${g.id}`)}
                  onClick={() => toggleGenre(g)}
                  className={cn(
                    'inline-flex min-h-[36px] items-center gap-1 rounded-full border px-3 text-sm transition-colors disabled:opacity-50',
                    on
                      ? 'border-primary bg-primary text-primary-foreground'
                      : 'border-border hover:bg-accent hover:text-accent-foreground',
                  )}
                >
                  {on && <Check className="h-3.5 w-3.5" />}
                  {g.name}
                </button>
              );
            })}
          </div>
        )}
      </section>
    </div>
  );
}

function ArtistSearch({
  pickedIDs,
  disabled,
  isPending,
  onAdd,
}: {
  pickedIDs: Set<string>;
  disabled: boolean;
  isPending: (id: string) => boolean;
  onAdd: (r: PickSearchResult) => void;
}) {
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<PickSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const [searchErr, setSearchErr] = useState('');
  const generation = useRef(0);

  // Same shape as /subscribe's search: the generation advances at the top of
  // the effect so clearing the box also discards a request still in flight.
  useEffect(() => {
    const my = ++generation.current;
    const q = query.trim();
    if (q.length < MIN_QUERY) {
      setResults([]);
      setSearchErr('');
      setSearching(false);
      return;
    }
    const timer = setTimeout(async () => {
      setSearching(true);
      setSearchErr('');
      try {
        const r = await apiFetch(`/api/me/picks/search?q=${encodeURIComponent(q)}`);
        if (my !== generation.current) return;
        if (!r.ok) {
          setResults([]);
          setSearchErr(statusMessage(r.status, PICK_ERRORS));
          return;
        }
        const j = (await r.json()) as { results: PickSearchResult[] };
        setResults(j.results ?? []);
      } catch {
        if (my === generation.current) setSearchErr(NETWORK_ERROR_MESSAGE);
      } finally {
        if (my === generation.current) setSearching(false);
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [query]);

  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor="pick-search" className="sr-only">
        Search for an artist
      </Label>
      <div className="relative">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          id="pick-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={disabled ? 'Remove an artist to add another' : 'Search for an artist'}
          disabled={disabled}
          autoComplete="off"
          className="pl-9"
        />
        {searching && (
          <Loader2 className="absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 animate-spin text-muted-foreground" />
        )}
      </div>
      {searchErr && <p className="text-sm text-destructive">{searchErr}</p>}
      {!searching && !searchErr && query.trim().length >= MIN_QUERY && results.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No artists on Ticketmaster match that. Check the spelling; partial names don't match.
        </p>
      )}
      {results.length > 0 && (
        <ul className="divide-y divide-border rounded-md border border-border" aria-label="Search results">
          {results.map((r) => {
            const picked = pickedIDs.has(r.attraction_id);
            return (
              <li key={r.attraction_id} className="flex items-center gap-3 px-3 py-2">
                {r.image_url ? (
                  <img src={r.image_url} alt="" className="h-9 w-9 shrink-0 rounded object-cover" />
                ) : (
                  <div className="h-9 w-9 shrink-0 rounded bg-muted" />
                )}
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">{r.name}</div>
                  <div className="truncate text-xs text-muted-foreground">
                    {[r.genre, r.upcoming > 0 ? `${r.upcoming} upcoming` : 'No shows listed now']
                      .filter(Boolean)
                      .join(' · ')}
                  </div>
                </div>
                <Button
                  size="sm"
                  variant={picked ? 'secondary' : 'outline'}
                  disabled={picked || disabled || isPending(r.attraction_id)}
                  onClick={() => onAdd(r)}
                  aria-label={picked ? `${r.name} added` : `Add ${r.name}`}
                >
                  {picked ? <Check className="h-4 w-4" /> : <Plus className="h-4 w-4" />}
                  {picked ? 'Added' : 'Add'}
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
