import { useEffect, useRef, useState } from 'react';
import { mutatingFetch } from '@/lib/api';
import type { ConcertsResponse, FiltersState } from '@/lib/types';

type State =
  | { kind: 'loading' }
  | { kind: 'loaded'; data: ConcertsResponse }
  | { kind: 'error'; message: string };

function buildQuery(f: FiltersState): string {
  const q = new URLSearchParams();
  if (f.genre) q.set('genre', f.genre);
  if (f.venue) q.set('venue', f.venue);
  if (f.dateFrom) q.set('date_from', f.dateFrom);
  if (f.dateTo) q.set('date_to', f.dateTo);
  if (f.weekday && f.weekday !== 'all') q.set('weekday', f.weekday);
  const s = q.toString();
  return s ? `?${s}` : '';
}

async function fetchFeed(endpoint: string, query: string): Promise<State> {
  try {
    const r = await fetch(`${endpoint}${query}`, { credentials: 'same-origin' });
    if (!r.ok) return { kind: 'error', message: `HTTP ${r.status}` };
    return { kind: 'loaded', data: (await r.json()) as ConcertsResponse };
  } catch (e) {
    return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
  }
}

// While a background refresh is in flight, poll every 10s so a fresh
// snapshot appears without a manual browser refresh.
const REFRESH_POLL_INTERVAL_MS = 10_000;

// A scan that can never succeed (revoked Spotify grant, an upstream that
// stays down) leaves the server reporting refreshing:true indefinitely.
// Without a ceiling the page would poll every 10s forever. ~10 minutes of
// attempts is well past the p99 scan, after which we stop and show what we
// have; changing a filter or reloading starts a fresh round.
const MAX_REFRESH_POLLS = 60;

// Transient failures get a few retries with widening gaps rather than
// killing the poll loop. Anything past this and we surface the error.
const MAX_CONSECUTIVE_ERRORS = 4;
const ERROR_BACKOFF_MS = [5_000, 15_000, 30_000, 60_000];

// Options lets a caller point the hook at a different feed endpoint (the
// saved list serves the same response shape) and force refetches that the
// query string can't express.
type Options = {
  endpoint?: string;
  reloadToken?: number;
};

// useConcerts owns the feed fetch + polling logic. Consumers get the
// current state, a rollback-on-failure mutator pair for saved/subscribed,
// and any error raised by those actions.
export function useConcerts(filters: FiltersState, opts: Options = {}) {
  const endpoint = opts.endpoint ?? '/api/me/concerts';
  const reloadToken = opts.reloadToken ?? 0;
  const [state, setState] = useState<State>({ kind: 'loading' });
  // Surfaced to the user when an optimistic action is rolled back. Without
  // it a star that silently un-stars itself reads as a broken app rather
  // than a failed request.
  const [actionError, setActionError] = useState<string | null>(null);
  const generation = useRef(0);
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const myGen = ++generation.current;
    if (pollTimer.current) clearTimeout(pollTimer.current);
    setState({ kind: 'loading' });
    const q = buildQuery(filters);
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    reloadToken;
    let lastComputedAt: string | undefined;
    let polls = 0;
    let consecutiveErrors = 0;
    // Once we've shown real data, a later failure must not replace it with
    // a bare error screen — the data on screen is still the best we have.
    let haveData = false;

    const schedulePoll = (delayMs: number) => {
      pollTimer.current = setTimeout(async () => {
        if (myGen !== generation.current) return;
        const next = await fetchFeed(endpoint, q);
        if (myGen !== generation.current) return;

        if (next.kind === 'loaded') {
          consecutiveErrors = 0;
          haveData = true;
          if (next.data.computed_at !== lastComputedAt || !next.data.refreshing) {
            setState(next);
            lastComputedAt = next.data.computed_at;
          }
          if (next.data.refreshing) {
            if (++polls < MAX_REFRESH_POLLS) {
              schedulePoll(REFRESH_POLL_INTERVAL_MS);
            } else {
              // Give up, and stop showing a spinner for a refresh nothing
              // is watching any more — otherwise the badge spins forever.
              setState({ kind: 'loaded', data: { ...next.data, refreshing: false } });
            }
          }
          return;
        }

        // Error path. Keep retrying with backoff; only give up — and only
        // clobber the view — once we've exhausted the retries and have
        // nothing better to show.
        consecutiveErrors++;
        if (consecutiveErrors <= MAX_CONSECUTIVE_ERRORS) {
          const backoff =
            ERROR_BACKOFF_MS[Math.min(consecutiveErrors - 1, ERROR_BACKOFF_MS.length - 1)];
          schedulePoll(backoff);
          return;
        }
        if (!haveData) setState(next);
      }, delayMs);
    };

    fetchFeed(endpoint, q).then((initial) => {
      if (myGen !== generation.current) return;
      if (initial.kind === 'loaded') {
        haveData = true;
        setState(initial);
        lastComputedAt = initial.data.computed_at;
        if (initial.data.refreshing) schedulePoll(REFRESH_POLL_INTERVAL_MS);
        return;
      }
      // The very first fetch failed: show the error, but keep trying in the
      // background so a blip on load doesn't strand the user on a dead page.
      setState(initial);
      consecutiveErrors = 1;
      schedulePoll(ERROR_BACKOFF_MS[0]);
    });

    return () => {
      if (pollTimer.current) clearTimeout(pollTimer.current);
    };
    // Rebuild the query key so useEffect only refires when a filter changes,
    // not on any parent re-render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    // reloadToken is in the dependency list so callers can force a refetch
    // for reasons the query string doesn't capture — changing location is
    // the case that matters: the server derives it from the user's saved
    // location, so the URL is identical but the results are not.
  }, [
    endpoint,
    reloadToken,
    filters.genre,
    filters.venue,
    filters.dateFrom,
    filters.dateTo,
    filters.weekday,
  ]);

  // Optimistic mutators — flip local state instantly, let the caller do the
  // network call. If it fails, caller can pass a rollback via the same
  // functions.
  function patchConcert(dedupKey: string, patch: Partial<{ saved: boolean; subscribed: boolean }>) {
    setState((prev) => {
      if (prev.kind !== 'loaded') return prev;
      const next = prev.data.concerts.map((c) =>
        c.dedup_key === dedupKey ? { ...c, ...patch } : c,
      );
      return { kind: 'loaded', data: { ...prev.data, concerts: next } };
    });
  }

  function patchArtistSubscription(artistID: string, subscribed: boolean) {
    setState((prev) => {
      if (prev.kind !== 'loaded') return prev;
      const next = prev.data.concerts.map((c) =>
        c.artist.id === artistID ? { ...c, subscribed } : c,
      );
      return { kind: 'loaded', data: { ...prev.data, concerts: next } };
    });
  }

  async function toggleSaved(dedupKey: string, currentlySaved: boolean) {
    setActionError(null);
    const next = !currentlySaved;
    patchConcert(dedupKey, { saved: next });
    const r = next
      ? await mutatingFetch('/api/me/saved-concerts', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ dedup_key: dedupKey }),
        })
      : await mutatingFetch(`/api/me/saved-concerts/${encodeURIComponent(dedupKey)}`, {
          method: 'DELETE',
        });
    if (!r.ok) {
      patchConcert(dedupKey, { saved: currentlySaved });
      setActionError(
        next ? "Couldn't save that show. Try again." : "Couldn't remove that show. Try again.",
      );
    }
  }

  async function toggleSubscribed(
    artistID: string,
    artistName: string,
    currentlySubscribed: boolean,
  ) {
    if (!artistID) return;
    setActionError(null);
    const next = !currentlySubscribed;
    patchArtistSubscription(artistID, next);
    const url = `/api/me/subscribed-artists/${encodeURIComponent(artistID)}`;
    const r = await mutatingFetch(url, {
      method: next ? 'POST' : 'DELETE',
      headers: next ? { 'Content-Type': 'application/json' } : undefined,
      body: next ? JSON.stringify({ display_name: artistName }) : undefined,
    });
    if (!r.ok) {
      patchArtistSubscription(artistID, currentlySubscribed);
      setActionError(
        next
          ? `Couldn't subscribe to ${artistName}. Try again.`
          : `Couldn't unsubscribe from ${artistName}. Try again.`,
      );
    }
  }

  return {
    state,
    toggleSaved,
    toggleSubscribed,
    actionError,
    dismissActionError: () => setActionError(null),
  };
}
