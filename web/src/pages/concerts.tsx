import { useEffect, useState } from 'react';
import { FilterBar } from '@/components/filter-bar';
import { ConcertsList } from '@/components/concerts-list';
import { LocationBar } from '@/components/location-bar';
import { useConcerts } from '@/hooks/use-concerts';
import type { FiltersState, Location } from '@/lib/types';

export default function ConcertsPage() {
  const [filters, setFilters] = useState<FiltersState>({
    genre: '',
    dateFrom: '',
    dateTo: '',
    weekday: 'all',
  });
  const [location, setLocation] = useState<Location | null>(null);
  const { state, toggleSaved, toggleSubscribed } = useConcerts(filters);

  // Location is fetched independently so the LocationBar can render even
  // if concerts are still loading.
  useEffect(() => {
    fetch('/api/me/location', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((l: Location | null) => setLocation(l))
      .catch(() => setLocation(null));
  }, []);

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">Upcoming concerts</h1>
        <p className="text-sm text-muted-foreground">
          Built from your Spotify listening. Refreshed daily.
        </p>
      </div>
      {location && <LocationBar location={location} onSaved={setLocation} />}
      {state.kind === 'loaded' && (
        <FilterBar filters={filters} facets={state.data.facets.genres} onChange={setFilters} />
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
