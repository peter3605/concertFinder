import { useEffect, useState } from 'react';
import { RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { FilterBar } from '@/components/filter-bar';
import { ConcertsList } from '@/components/concerts-list';
import { LocationBar } from '@/components/location-bar';
import { ActionError } from '@/components/action-error';
import { useConcerts } from '@/hooks/use-concerts';
import { formatRetry, mutatingFetch } from '@/lib/api';
import { detectAndSaveLocation } from '@/lib/geolocate';
import { useDocumentTitle } from '@/lib/use-document-title';
import { EMPTY_FILTERS, type FiltersState, type Location } from '@/lib/types';

export default function ConcertsPage() {
  useDocumentTitle('Concerts');
  const [filters, setFilters] = useState<FiltersState>(EMPTY_FILTERS);
  const [location, setLocation] = useState<Location | null>(null);
  // Bumped when the user saves a new location, and when they ask for a manual
  // refresh. The concerts request has no location parameter — the server reads
  // it from the user's saved row — so the URL is unchanged and only an
  // explicit token can trigger the refetch.
  const [reloadVersion, setReloadVersion] = useState(0);
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
  // When the server reports is_default, the user has no saved location and is
  // being shown this deployment's USER_LATITUDE/LONGITUDE fallback — a real
  // city that is very unlikely to be theirs. Ask the browser once. Everything
  // about this is best-effort: declining, an unsupported browser, or a failed
  // fix all leave the fallback in place and the location bar working exactly as
  // before, so nothing is surfaced to the user.
  useEffect(() => {
    let cancelled = false;

    (async () => {
      let loc: Location | null = null;
      try {
        const r = await fetch('/api/me/location', { credentials: 'same-origin' });
        loc = r.ok ? ((await r.json()) as Location) : null;
      } catch {
        loc = null;
      }
      if (cancelled) return;
      setLocation(loc);

      if (!loc?.is_default) return;

      const outcome = await detectAndSaveLocation(saveLocation);
      if (cancelled || outcome.kind !== 'saved') return;
      setLocation(outcome.location);
      // A scan already ran at the fallback location. Re-fetch so the list
      // reflects the detected one — the response carries refreshing:true and
      // the hook's bounded poll loop picks up the new snapshot.
      setReloadVersion((v) => v + 1);
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  function onLocationSaved(loc: Location) {
    setLocation(loc);
    setReloadVersion((v) => v + 1);
  }

  // Shared by the location bar and the first-login browser detection above, so
  // both write a location the same way.
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
    if (!r.ok) throw new Error(await r.text());
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
      {location && <LocationBar location={location} onSaved={onLocationSaved} />}
      <ActionError message={refreshError} onDismiss={() => setRefreshError(null)} />
      <ActionError message={actionError} onDismiss={dismissActionError} />
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
      {state.kind === 'loaded' && (
        <ConcertsList
          data={state.data}
          onToggleSave={toggleSaved}
          onToggleSubscribe={toggleSubscribed}
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
