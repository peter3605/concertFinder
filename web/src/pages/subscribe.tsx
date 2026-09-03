import { useEffect, useRef, useState } from 'react';
import { Bell, Loader2, Search, X } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { ActionError } from '@/components/action-error';
import { SpotifyAttribution } from '@/components/spotify-attribution';
import { apiFetch, mutatingFetch } from '@/lib/api';
import { useDocumentTitle } from '@/lib/use-document-title';

// Full-fledged subscribe/search page. Any Spotify artist (touring or not)
// can be followed here; the bell on concert cards is the shortcut for
// artists already surfacing in your feed.
type SearchArtist = { id: string; name: string; genres?: string[]; image_url?: string };
type SubscribedArtist = { id: string; name: string };

const SEARCH_DEBOUNCE_MS = 300;

export default function SubscribePage() {
  useDocumentTitle('Subscriptions');
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<SearchArtist[]>([]);
  const [searching, setSearching] = useState(false);
  const [searchErr, setSearchErr] = useState('');
  const [subs, setSubs] = useState<SubscribedArtist[]>([]);
  // Subscribe/unsubscribe are optimistic; without a message a rollback
  // looks like the button undoing itself for no reason.
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const generation = useRef(0);
  // Subscribe and unsubscribe are the same URL with different methods, so a
  // double-click could put both in flight and let the server's ordering — not
  // the user's — decide the outcome. One change per artist at a time.
  const pendingArtists = useRef(new Set<string>());

  useEffect(() => {
    apiFetch('/api/me/subscribed-artists')
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((j: { artists: SubscribedArtist[] }) => setSubs(j.artists ?? []))
      .catch(() => setSubs([]))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    if (debounceTimer.current) clearTimeout(debounceTimer.current);
    // Invalidate whatever is in flight here, at the top of the effect —
    // not inside the debounce timer. Clearing the box takes the early return
    // below, which never armed a timer, so the generation never advanced and
    // a fetch that resolved afterwards refilled the results under an empty
    // search field. The same return is why `searching` is cleared explicitly:
    // that request's `finally` is now on a stale generation and will not.
    const my = ++generation.current;
    const trimmed = query.trim();
    if (!trimmed) {
      setResults([]);
      setSearchErr('');
      setSearching(false);
      return;
    }
    debounceTimer.current = setTimeout(async () => {
      setSearching(true);
      setSearchErr('');
      try {
        const r = await apiFetch(`/api/me/artists/search?q=${encodeURIComponent(trimmed)}`);
        if (my !== generation.current) return;
        if (!r.ok) {
          setSearchErr(`Search failed (${r.status})`);
          setResults([]);
          return;
        }
        const j = (await r.json()) as { artists: SearchArtist[] };
        setResults(j.artists ?? []);
      } catch (e) {
        if (my !== generation.current) return;
        setSearchErr(e instanceof Error ? e.message : String(e));
      } finally {
        if (my === generation.current) setSearching(false);
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      if (debounceTimer.current) clearTimeout(debounceTimer.current);
    };
  }, [query]);

  const subscribedIDs = new Set(subs.map((s) => s.id));

  // Both mutators update functionally rather than restoring a list snapshot
  // taken before the click: two different artists can be in flight at once,
  // and replaying the old array would undo whichever one succeeded.
  async function subscribe(a: SearchArtist) {
    if (pendingArtists.current.has(a.id)) return;
    pendingArtists.current.add(a.id);
    setActionErr(null);
    setSubs((s) => [...s.filter((x) => x.id !== a.id), { id: a.id, name: a.name }].sort(byName));
    try {
      // A rejected fetch and a refused request are the same failure to the
      // user; both roll back here. Without the catch a dropped connection
      // left the button reading "Subscribed" for an artist nobody followed.
      const r = await mutatingFetch(`/api/me/subscribed-artists/${encodeURIComponent(a.id)}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ display_name: a.name }),
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
    } catch {
      setSubs((s) => s.filter((x) => x.id !== a.id));
      setActionErr(`Couldn't subscribe to ${a.name}. Try again.`);
    } finally {
      pendingArtists.current.delete(a.id);
    }
  }

  async function unsubscribe(artistID: string) {
    if (pendingArtists.current.has(artistID)) return;
    pendingArtists.current.add(artistID);
    setActionErr(null);
    // Captured before the optimistic removal, since restoring it is the
    // rollback and the name is what the error message says.
    const removed = subs.find((s) => s.id === artistID);
    const name = removed?.name ?? 'that artist';
    setSubs((s) => s.filter((x) => x.id !== artistID));
    try {
      const r = await mutatingFetch(`/api/me/subscribed-artists/${encodeURIComponent(artistID)}`, {
        method: 'DELETE',
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
    } catch {
      if (removed) {
        setSubs((s) => [...s.filter((x) => x.id !== artistID), removed].sort(byName));
      }
      setActionErr(`Couldn't unsubscribe from ${name}. Try again.`);
    } finally {
      pendingArtists.current.delete(artistID);
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">Subscribed artists</h1>
        <p className="max-w-xl text-sm text-muted-foreground">
          Follow any artist — including ones without an announced tour. When a
          new show lands in your area for someone you follow, you'll get an
          email immediately (as long as instant notifications are on in
          Settings).
        </p>
      </div>

      <ActionError message={actionErr} onDismiss={() => setActionErr(null)} />

      <Card>
        <CardContent className="p-4">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="h-11 pl-10 pr-10 text-base"
              type="text"
              placeholder="Search Spotify for an artist"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              autoFocus
            />
            {query && (
              <button
                onClick={() => setQuery('')}
                aria-label="Clear"
                className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-muted-foreground hover:bg-accent coarse:p-2.5"
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
          {searching && (
            <div className="mt-3 flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" /> Searching…
            </div>
          )}
          {searchErr && (
            <p className="mt-3 text-sm text-destructive">{searchErr}</p>
          )}
          {results.length > 0 && (
            <ul className="mt-3 divide-y divide-border">
              {results.map((a) => {
                const isSub = subscribedIDs.has(a.id);
                return (
                  <li key={a.id} className="flex items-center gap-3 py-2">
                    {a.image_url ? (
                      <img
                        src={a.image_url}
                        alt=""
                        className="h-12 w-12 shrink-0 rounded-md object-cover"
                      />
                    ) : (
                      <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-md bg-muted text-xs uppercase text-muted-foreground">
                        {a.name.slice(0, 2)}
                      </div>
                    )}
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium">{a.name}</div>
                      {a.genres && a.genres.length > 0 && (
                        <div className="mt-0.5 flex flex-wrap gap-1">
                          {a.genres.slice(0, 3).map((g) => (
                            <Badge key={g} variant="muted" className="text-[10px]">
                              {g}
                            </Badge>
                          ))}
                        </div>
                      )}
                    </div>
                    <Button
                      variant={isSub ? 'secondary' : 'default'}
                      size="sm"
                      onClick={() => (isSub ? unsubscribe(a.id) : subscribe(a))}
                    >
                      <Bell className={isSub ? 'fill-current' : ''} />
                      {isSub ? 'Subscribed' : 'Subscribe'}
                    </Button>
                  </li>
                );
              })}
            </ul>
          )}
          {results.length > 0 && (
            <p className="mt-3 text-xs text-muted-foreground">
              <SpotifyAttribution label="Artist results powered by" />
            </p>
          )}
        </CardContent>
      </Card>

      <div>
        <h2 className="mb-3 text-lg font-semibold">Your subscriptions</h2>
        {loading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : subs.length === 0 ? (
          <Card>
            <CardContent className="p-6 text-center text-sm text-muted-foreground">
              Nothing yet. Use the search above to add someone.
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardContent className="p-0">
              <ul className="divide-y divide-border">
                {subs.map((s) => (
                  <li key={s.id} className="flex items-center gap-3 px-4 py-3">
                    <Bell className="h-4 w-4 text-primary" />
                    <div className="min-w-0 flex-1 truncate font-medium">
                      {s.name || s.id}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => unsubscribe(s.id)}
                      aria-label="Unsubscribe"
                    >
                      Remove
                    </Button>
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}

function byName(a: SubscribedArtist, b: SubscribedArtist): number {
  return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
}
