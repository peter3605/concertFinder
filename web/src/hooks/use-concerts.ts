import { useEffect, useRef, useState } from 'react';
import { apiFetch, mutatingFetch } from '@/lib/api';
import type { Act, ConcertsResponse, FiltersState } from '@/lib/types';

// `stale` is true while a fetch is in flight over data we already have — a
// filter change, a location change, a manual refresh. It exists so those never
// go back through `loading`: unmounting the loaded branch tore down the filter
// bar and the list mid-interaction, which dropped keyboard focus to <body> and
// scrolled to the top, so setting a date range cost a round trip between the
// two inputs. Consumers keep rendering the last data and mark it aria-busy.
// `pollStopped` marks a refresh we have stopped watching — the poll ceiling
// was reached, or the retries were. It is optional because it is the unusual
// case, and every state that omits it means "nothing has given up".
type State =
  | { kind: 'loading' }
  | { kind: 'loaded'; data: ConcertsResponse; stale: boolean; pollStopped?: boolean }
  | { kind: 'error'; message: string };

// What one fetch produced, before it is folded into the state above. Separate
// from State because a fetch has no opinion about staleness — that is a
// property of the view it is replacing.
type FetchResult =
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

async function fetchFeed(endpoint: string, query: string): Promise<FetchResult> {
  try {
    const r = await apiFetch(`${endpoint}${query}`);
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
  // A double-click on one control must not put a POST and a DELETE in flight
  // together: whichever response the server handles last decides the stored
  // state, and that is not necessarily the last click. Each set is keyed by
  // what its request addresses — a dedup_key for a save, an artist for a
  // subscription — which is also the unit the optimistic patch rewrites.
  const savePending = useRef(new Set<string>());
  const subscribePending = useRef(new Set<string>());

  useEffect(() => {
    const myGen = ++generation.current;
    if (pollTimer.current) clearTimeout(pollTimer.current);
    // Only the very first load has nothing to show. Every later one keeps the
    // previous response on screen and flags it stale.
    setState((prev) => (prev.kind === 'loaded' ? { ...prev, stale: true } : { kind: 'loading' }));
    const q = buildQuery(filters);
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    reloadToken;
    let lastComputedAt: string | undefined;
    let polls = 0;
    let consecutiveErrors = 0;
    // Once we've shown real data, a later failure must not replace it with
    // a bare error screen — the data on screen is still the best we have.
    let haveData = false;
    // True while there is still something to poll for. Distinct from having a
    // timer armed: a hidden tab has no timer and every reason to resume when
    // it comes back, while a finished or abandoned refresh has neither.
    let wantsPoll = false;

    const isHidden = () => document.visibilityState === 'hidden';

    const schedulePoll = (delayMs: number) => {
      wantsPoll = true;
      if (pollTimer.current) clearTimeout(pollTimer.current);
      // Don't poll a tab nobody is looking at. Browsers throttle background
      // timers to roughly one a minute anyway, so the loop half-runs either
      // way; this makes it stop honestly and resume on the events below,
      // rather than dribbling requests at a rate nothing in the code states.
      if (isHidden()) return;
      pollTimer.current = setTimeout(async () => {
        if (myGen !== generation.current) return;
        const next = await fetchFeed(endpoint, q);
        if (myGen !== generation.current) return;

        if (next.kind === 'loaded') {
          consecutiveErrors = 0;
          haveData = true;
          if (next.data.computed_at !== lastComputedAt || !next.data.refreshing) {
            setState({ ...next, stale: false });
            lastComputedAt = next.data.computed_at;
          }
          if (next.data.refreshing) {
            if (++polls < MAX_REFRESH_POLLS) {
              schedulePoll(REFRESH_POLL_INTERVAL_MS);
            } else {
              // Give up, and stop showing a spinner for a refresh nothing
              // is watching any more — otherwise the badge spins forever.
              // pollStopped is what says so in words; a spinner that simply
              // stops is indistinguishable from one that finished.
              wantsPoll = false;
              setState({
                kind: 'loaded',
                data: { ...next.data, refreshing: false },
                stale: false,
                pollStopped: true,
              });
            }
          } else {
            wantsPoll = false;
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
        wantsPoll = false;
        if (!haveData) {
          setState(next);
          return;
        }
        // We still have something to show, so the error stays off screen —
        // but the list must stop claiming a refresh is on its way, and the
        // spinner has to come down with it.
        setState((prev) =>
          prev.kind === 'loaded'
            ? {
                ...prev,
                data: { ...prev.data, refreshing: false },
                stale: false,
                pollStopped: true,
              }
            : prev,
        );
      }, delayMs);
    };

    // Coming back to the tab, or back onto the network, is exactly when the
    // answer is most likely to have changed and least likely to have been
    // seen. Without this a laptop closed mid-scan reopened onto a spinner
    // whose timer had been throttled into uselessness.
    const resume = () => {
      if (myGen !== generation.current || !wantsPoll || isHidden()) return;
      schedulePoll(0);
    };
    document.addEventListener('visibilitychange', resume);
    window.addEventListener('online', resume);

    fetchFeed(endpoint, q).then((initial) => {
      if (myGen !== generation.current) return;
      if (initial.kind === 'loaded') {
        haveData = true;
        setState({ ...initial, stale: false });
        lastComputedAt = initial.data.computed_at;
        if (initial.data.refreshing) schedulePoll(REFRESH_POLL_INTERVAL_MS);
        return;
      }
      // The first fetch of this generation failed. Anything already on screen
      // answers a query the user has since changed, so it stays only while the
      // retries run; the error replaces it once they are exhausted, below.
      // Meanwhile keep trying in the background so a blip on load doesn't
      // strand the user on a dead page.
      const failure: State = initial;
      setState((prev) => (prev.kind === 'loaded' ? prev : failure));
      consecutiveErrors = 1;
      schedulePoll(ERROR_BACKOFF_MS[0]);
    });

    return () => {
      if (pollTimer.current) clearTimeout(pollTimer.current);
      document.removeEventListener('visibilitychange', resume);
      window.removeEventListener('online', resume);
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
  // Both mutators reach into an event's acts, since saves and subscriptions
  // are per artist even though the artists share a card. patchAct rewrites
  // the one act the user clicked; patchArtistSubscription rewrites that
  // artist everywhere, because subscribing is a property of the artist and
  // they may appear on several bills in the same list.
  function patchActs(
    prev: State,
    match: (act: Act) => boolean,
    patch: Partial<{ saved: boolean; subscribed: boolean }>,
  ): State {
    if (prev.kind !== 'loaded') return prev;
    const events = prev.data.events.map((e) =>
      e.acts.some(match) ? { ...e, acts: e.acts.map((a) => (match(a) ? { ...a, ...patch } : a)) } : e,
    );
    // Staleness is carried through: an optimistic star must not look like the
    // in-flight fetch it happens to overlap with has landed.
    return { ...prev, data: { ...prev.data, events } };
  }

  function patchConcert(dedupKey: string, patch: Partial<{ saved: boolean; subscribed: boolean }>) {
    setState((prev) => patchActs(prev, (a) => a.dedup_key === dedupKey, patch));
  }

  function patchArtistSubscription(artistID: string, subscribed: boolean) {
    setState((prev) => patchActs(prev, (a) => a.artist.id === artistID, { subscribed }));
  }

  async function toggleSaved(dedupKey: string, currentlySaved: boolean) {
    if (savePending.current.has(dedupKey)) return;
    savePending.current.add(dedupKey);
    setActionError(null);
    const next = !currentlySaved;
    patchConcert(dedupKey, { saved: next });
    try {
      // A rejected fetch (dropped connection, offline tab) and a refused
      // request are the same failure to the user, so both land in the catch
      // and get the same rollback. Without it the star stayed lit until a
      // later poll silently un-lit it.
      const r = next
        ? await mutatingFetch('/api/me/saved-concerts', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ dedup_key: dedupKey }),
          })
        : await mutatingFetch(`/api/me/saved-concerts/${encodeURIComponent(dedupKey)}`, {
            method: 'DELETE',
          });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
    } catch {
      patchConcert(dedupKey, { saved: currentlySaved });
      setActionError(
        next ? "Couldn't save that show. Try again." : "Couldn't remove that show. Try again.",
      );
    } finally {
      savePending.current.delete(dedupKey);
    }
  }

  async function toggleSubscribed(
    artistID: string,
    artistName: string,
    currentlySubscribed: boolean,
  ) {
    if (!artistID) return;
    if (subscribePending.current.has(artistID)) return;
    subscribePending.current.add(artistID);
    setActionError(null);
    const next = !currentlySubscribed;
    // The rollback is as wide as the patch: an artist can be on several
    // bills, so one failed click has to un-flip every bell it lit.
    patchArtistSubscription(artistID, next);
    const url = `/api/me/subscribed-artists/${encodeURIComponent(artistID)}`;
    try {
      const r = await mutatingFetch(url, {
        method: next ? 'POST' : 'DELETE',
        headers: next ? { 'Content-Type': 'application/json' } : undefined,
        body: next ? JSON.stringify({ display_name: artistName }) : undefined,
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
    } catch {
      patchArtistSubscription(artistID, currentlySubscribed);
      setActionError(
        next
          ? `Couldn't subscribe to ${artistName}. Try again.`
          : `Couldn't unsubscribe from ${artistName}. Try again.`,
      );
    } finally {
      subscribePending.current.delete(artistID);
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
