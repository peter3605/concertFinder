import { useState } from 'react';
import { ConcertsList } from '@/components/concerts-list';
import { FilterBar } from '@/components/filter-bar';
import { ActionError } from '@/components/action-error';
import { useConcerts } from '@/hooks/use-concerts';
import { useDocumentTitle } from '@/lib/use-document-title';
import { EMPTY_FILTERS, hasActiveFilters, type FiltersState } from '@/lib/types';

export default function SavedPage() {
  useDocumentTitle('Saved');
  const [filters, setFilters] = useState<FiltersState>(EMPTY_FILTERS);
  // Saved reads from its own endpoint, which joins the saves table to the
  // shared concerts table. It deliberately does not filter the concert feed:
  // a snapshot only holds the current affinity top-200, so a star used to
  // disappear the moment its artist dropped out of that list.
  const { state, toggleSaved, toggleSubscribed, actionError, dismissActionError } = useConcerts(
    filters,
    { endpoint: '/api/me/saved-concerts' },
  );

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">Saved concerts</h1>
        <p className="text-sm text-muted-foreground">
          Shows you starred. These stay here even if the artist drops out of your
          feed — click the star again to remove one.
        </p>
      </div>
      <ActionError message={actionError} onDismiss={dismissActionError} />
      {/* Gated on the load, not on the results: hiding the bar when a filter
          matches nothing took away the only control that could undo it, and
          left "Nothing saved yet" claiming the saves themselves were gone. */}
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
          awaitsFirstScan={false}
          emptyMessage={
            hasActiveFilters(filters)
              ? 'No saved shows match these filters.'
              : 'Nothing saved yet. Star a concert from the main list to add it here.'
          }
        />
      )}
    </div>
  );
}
