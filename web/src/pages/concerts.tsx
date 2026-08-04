import { useEffect, useState } from 'react';
import { FilterBar } from '@/components/filter-bar';
import { ConcertsList } from '@/components/concerts-list';
import { LocationBar } from '@/components/location-bar';
import { ActionError } from '@/components/action-error';
import { useConcerts } from '@/hooks/use-concerts';
import { useDocumentTitle } from '@/lib/use-document-title';
import { EMPTY_FILTERS, type FiltersState, type Location } from '@/lib/types';

export default function ConcertsPage() {
  useDocumentTitle('Concerts');
  const [filters, setFilters] = useState<FiltersState>(EMPTY_FILTERS);
  const [location, setLocation] = useState<Location | null>(null);
  // Bumped when the user saves a new location. The concerts request has no
  // location parameter — the server reads it from the user's saved row — so
  // the URL is unchanged and only an explicit token can trigger the refetch.
  const [locationVersion, setLocationVersion] = useState(0);
  const { state, toggleSaved, toggleSubscribed, actionError, dismissActionError } = useConcerts(
    filters,
    { reloadToken: locationVersion },
  );

  // Location is fetched independently so the LocationBar can render even
  // if concerts are still loading.
  useEffect(() => {
    fetch('/api/me/location', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((l: Location | null) => setLocation(l))
      .catch(() => setLocation(null));
  }, []);

  function onLocationSaved(loc: Location) {
    setLocation(loc);
    setLocationVersion((v) => v + 1);
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">Upcoming concerts</h1>
        <p className="text-sm text-muted-foreground">
          Built from your Spotify listening. Refreshed daily.
        </p>
      </div>
      {location && <LocationBar location={location} onSaved={onLocationSaved} />}
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
