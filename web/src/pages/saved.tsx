import { useState } from 'react';
import { ConcertsList } from '@/components/concerts-list';
import { useConcerts } from '@/hooks/use-concerts';
import type { FiltersState } from '@/lib/types';

export default function SavedPage() {
  // Only the year-range filters are meaningful here — the saved set is
  // small and users likely want to see everything.
  const [filters] = useState<FiltersState>({
    genre: '',
    dateFrom: '',
    dateTo: '',
    weekday: 'all',
  });
  const { state, toggleSaved, toggleSubscribed } = useConcerts({ ...filters, savedOnly: true });

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">Saved concerts</h1>
        <p className="text-sm text-muted-foreground">
          Shows you starred from the main list. Click the star again to remove.
        </p>
      </div>
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
          emptyMessage="Nothing saved yet. Star a concert from the main list to add it here."
        />
      )}
    </div>
  );
}
