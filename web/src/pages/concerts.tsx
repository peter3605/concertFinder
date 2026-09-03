import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { FilterBar } from '@/components/filter-bar';
import { ConcertsList } from '@/components/concerts-list';
import { LocationBar } from '@/components/location-bar';
import { LocationPrompt } from '@/components/location-prompt';
import { SaveSubscribeHint } from '@/components/save-subscribe-hint';
import { ActionError } from '@/components/action-error';
import { useConcerts } from '@/hooks/use-concerts';
import { apiFetch, formatRetry, mutatingFetch, statusMessage } from '@/lib/api';
import { useDocumentTitle } from '@/lib/use-document-title';
import {
  EMPTY_FILTERS,
  hasActiveFilters,
  type FiltersState,
  type Location,
} from '@/lib/types';

export default function ConcertsPage() {
  useDocumentTitle('Concerts');
  const [filters, setFilters] = useState<FiltersState>(EMPTY_FILTERS);
  const [location, setLocation] = useState<Location | null>(null);
  // Bumped when the user saves a new location, and when they ask for a manual
  // refresh. The concerts request has no location parameter — the server reads
  // it from the user's saved row — so the URL is unchanged and only an
  // explicit token can trigger the refetch.
  const [reloadVersion, setReloadVersion] = useState(0);
  // Bumped to open the location bar's city field — from the first-run prompt,
  // whether the user asks for it or the browser refuses the geolocation dialog.
  const [editLocationToken, setEditLocationToken] = useState(0);
  const [refreshBusy, setRefreshBusy] = useState(false);
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const { state, toggleSaved, toggleSubscribed, actionError, dismissActionError } = useConcerts(
    filters,
    { reloadToken: reloadVersion },
  );
  const refreshing = state.kind === 'loaded' && state.data.refreshing;

  async function requestRefresh() {
    setRefreshBusy(true);
    setRefreshError(null);
    try {
      const r = await mutatingFetch('/api/me/concerts/refresh', { method: 'POST' });
      if (r.status === 202) {
        // Refetch, which picks up refreshing:true and hands off to the hook's
        // existing bounded poll loop.
        setReloadVersion((v) => v + 1);
        return;
      }
      if (r.status === 429) {
        const body = (await r.json()) as { retry_after?: string; reason?: string };
        setRefreshError(refusalMessage(body.reason, body.retry_after));
        return;
      }
      setRefreshError("Couldn't start a refresh. Try again.");
    } catch (e) {
      setRefreshError(e instanceof Error ? e.message : String(e));
    } finally {
      setRefreshBusy(false);
    }
  }

  // Location is fetched independently so the LocationBar can render even
  // if concerts are still loading.
  //
  // is_default means the user has no saved location and is looking at this
  // deployment's USER_LATITUDE/LONGITUDE fallback — a real city that is very
  // unlikely to be theirs. That used to fire the browser's geolocation dialog
  // here, unannounced. It doesn't any more: the browser remembers a denial per
  // origin and will not ask again, so an unexplained prompt spends the only
  // question we get. LocationPrompt asks first, in words, and offers the city
  // field beside it.
  useEffect(() => {
    let cancelled = false;

    (async () => {
      let loc: Location | null = null;
      try {
        const r = await apiFetch('/api/me/location');
        loc = r.ok ? ((await r.json()) as Location) : null;
      } catch {
        loc = null;
      }
      if (cancelled) return;
      setLocation(loc);
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  function onLocationSaved(loc: Location) {
    setLocation(loc);
    setReloadVersion((v) => v + 1);
  }

  // Shared by the location bar and the first-run prompt's browser detection,
  // so both write a location the same way.
  async function saveLocation(body: {
    latitude: number;
    longitude: number;
    radius_miles: number;
  }): Promise<Location> {
    const r = await mutatingFetch('/api/me/location', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    // The status, not the body: a Go http.Error string reads as an internal
    // note and a proxy failure is HTML. Nothing renders this today (the
    // prompt writes its own copy for a failed detection), which is exactly
    // why it would be the one left behind.
    if (!r.ok) throw new Error(statusMessage(r.status));
    return (await r.json()) as Location;
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">Upcoming concerts</h1>
          <p className="text-sm text-muted-foreground">
            Built from your Spotify listening. Refreshed daily.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={requestRefresh}
          disabled={refreshBusy || refreshing}
          title="Check for new shows now"
        >
          <RefreshCw className={refreshing ? 'h-4 w-4 animate-spin' : 'h-4 w-4'} />
          {refreshing ? 'Refreshing…' : 'Refresh'}
        </Button>
      </div>
      {location?.is_default && (
        <LocationPrompt
          save={saveLocation}
          onLocated={onLocationSaved}
          onTypeCity={() => setEditLocationToken((v) => v + 1)}
        />
      )}
      {location && (
        <LocationBar
          location={location}
          onSaved={onLocationSaved}
          openEditorToken={editLocationToken}
        />
      )}
      <ActionError message={refreshError} onDismiss={() => setRefreshError(null)} />
      <ActionError message={actionError} onDismiss={dismissActionError} />
      {/* The bar and the list stay mounted across a filter change — the fetch
          marks them stale instead. Unmounting them dropped focus to <body> and
          scrolled to the top, so setting a date range cost a round trip
          between the two inputs. */}
      {state.kind === 'loaded' && (
        <FilterBar
          filters={filters}
          facets={state.data.facets.genres}
          venueFacets={state.data.facets.venues}
          onChange={setFilters}
        />
      )}
      {state.kind === 'loading' && <div className="text-sm text-muted-foreground">Loading…</div>}
      {state.kind === 'error' && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
          Error: {state.message}
        </div>
      )}
      {state.kind === 'loaded' && state.data.count > 0 && <SaveSubscribeHint />}
      {state.kind === 'loaded' && (
        <ConcertsList
          data={state.data}
          busy={state.stale}
          refreshStopped={state.pollStopped}
          onToggleSave={toggleSaved}
          onToggleSubscribe={toggleSubscribed}
          emptyAction={
            // Only when the list is empty on its own terms. With a filter on,
            // the thing to do is clear it, and the empty copy already says so.
            hasActiveFilters(filters) ? undefined : (
              <Button asChild size="sm">
                <Link to="/subscribe">Get alerts when an artist announces a show</Link>
              </Button>
            )
          }
        />
      )}
    </div>
  );
}

// A refusal is a wait, not a failure — say which wait it is. Quota exhaustion
// lasts until the daily ledger resets and no amount of clicking helps; the
// cooldown is a matter of minutes.
function refusalMessage(reason: string | undefined, retryAfter: string | undefined): string {
  const when = retryAfter ? formatRetry(retryAfter) : null;
  if (reason === 'quota_exhausted') {
    return when
      ? `You've used today's search allowance. More shows after ${when}.`
      : "You've used today's search allowance for now.";
  }
  return when
    ? `Just refreshed — you can check again after ${when}.`
    : 'Just refreshed. Try again in a few minutes.';
}
