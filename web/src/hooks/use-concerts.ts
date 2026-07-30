import { useEffect, useRef, useState } from 'react';
import { mutatingFetch } from '@/lib/api';
import type { ConcertsResponse, FiltersState } from '@/lib/types';

type State =
  | { kind: 'loading' }
  | { kind: 'loaded'; data: ConcertsResponse }
  | { kind: 'error'; message: string };

function buildQuery(f: FiltersState & { savedOnly?: boolean }): string {
  const q = new URLSearchParams();
  if (f.genre) q.set('genre', f.genre);
  if (f.dateFrom) q.set('date_from', f.dateFrom);
  if (f.dateTo) q.set('date_to', f.dateTo);
  if (f.weekday && f.weekday !== 'all') q.set('weekday', f.weekday);
  if (f.savedOnly) q.set('saved_only', 'true');
  const s = q.toString();
  return s ? `?${s}` : '';
}

async function fetchConcerts(query: string): Promise<State> {
  try {
    const r = await fetch(`/api/me/concerts${query}`, { credentials: 'same-origin' });
    if (!r.ok) return { kind: 'error', message: `HTTP ${r.status}` };
    return { kind: 'loaded', data: (await r.json()) as ConcertsResponse };
  } catch (e) {
    return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
  }
}

// While a background refresh is in flight, poll every 10s so a fresh
// snapshot appears without a manual browser refresh.
const REFRESH_POLL_INTERVAL_MS = 10_000;

// useConcerts owns the concerts fetch + polling logic. Consumers pass
// filters + optional savedOnly and get back the current state plus a
// mutator that can flip saved/subscribed on individual rows without a
// full re-fetch.
export function useConcerts(filters: FiltersState & { savedOnly?: boolean }) {
  const [state, setState] = useState<State>({ kind: 'loading' });
  const generation = useRef(0);
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const myGen = ++generation.current;
    if (pollTimer.current) clearTimeout(pollTimer.current);
    setState({ kind: 'loading' });
    const q = buildQuery(filters);
    let lastComputedAt: string | undefined;

    const schedulePoll = () => {
      pollTimer.current = setTimeout(async () => {
        if (myGen !== generation.current) return;
        const next = await fetchConcerts(q);
        if (myGen !== generation.current) return;
        if (next.kind === 'loaded') {
          if (next.data.computed_at !== lastComputedAt || !next.data.refreshing) {
            setState(next);
            lastComputedAt = next.data.computed_at;
          }
          if (next.data.refreshing) schedulePoll();
        } else {
          setState(next);
        }
      }, REFRESH_POLL_INTERVAL_MS);
    };

    fetchConcerts(q).then((initial) => {
      if (myGen !== generation.current) return;
      setState(initial);
      if (initial.kind === 'loaded') {
        lastComputedAt = initial.data.computed_at;
        if (initial.data.refreshing) schedulePoll();
      }
    });

    return () => {
      if (pollTimer.current) clearTimeout(pollTimer.current);
    };
    // Rebuild the query key so useEffect only refires when a filter changes,
    // not on any parent re-render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters.genre, filters.dateFrom, filters.dateTo, filters.weekday, filters.savedOnly]);

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
    if (!r.ok) patchConcert(dedupKey, { saved: currentlySaved });
  }

  async function toggleSubscribed(
    artistID: string,
    artistName: string,
    currentlySubscribed: boolean,
  ) {
    if (!artistID) return;
    const next = !currentlySubscribed;
    patchArtistSubscription(artistID, next);
    const url = `/api/me/subscribed-artists/${encodeURIComponent(artistID)}`;
    const r = await mutatingFetch(url, {
      method: next ? 'POST' : 'DELETE',
      headers: next ? { 'Content-Type': 'application/json' } : undefined,
      body: next ? JSON.stringify({ display_name: artistName }) : undefined,
    });
    if (!r.ok) patchArtistSubscription(artistID, currentlySubscribed);
  }

  return { state, toggleSaved, toggleSubscribed };
}
