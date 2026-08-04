import { X } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { EMPTY_FILTERS, type Facet, type FiltersState, type Weekday } from '@/lib/types';
import { cn } from '@/lib/utils';

type Props = {
  filters: FiltersState;
  facets: Facet[];
  venueFacets?: Facet[];
  onChange: (f: FiltersState) => void;
};

// Sticky filter block. Sits above the concert list on all breakpoints;
// on wider screens it goes in one row, on mobile it stacks.
//
// Genres are pills (a handful dominate, and one tap is the common case);
// venues are a select, because the list is longer, the names are far wider,
// and picking one is a deliberate act rather than browsing.
export function FilterBar({ filters, facets, venueFacets = [], onChange }: Props) {
  const topGenres = facets.slice(0, 10);
  const genreActive = filters.genre !== '';
  // A venue the user picked can fall out of the facet list once other
  // filters narrow things down. Keep it selectable so the dropdown doesn't
  // silently snap back to "Any venue" while the filter is still applied.
  const venueOptions =
    filters.venue && !venueFacets.some((f) => f.value === filters.venue)
      ? [{ value: filters.venue, count: 0 }, ...venueFacets]
      : venueFacets;
  // The empty state tells people to try clearing filters, so give them a
  // way to actually do it rather than resetting four controls by hand.
  const anyActive =
    filters.genre !== '' ||
    filters.venue !== '' ||
    filters.dateFrom !== '' ||
    filters.dateTo !== '' ||
    filters.weekday !== 'all';
  return (
    <div className="flex flex-col gap-3">
      {topGenres.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          <GenrePill
            active={!genreActive}
            onClick={() => onChange({ ...filters, genre: '' })}
          >
            All genres
          </GenrePill>
          {topGenres.map((f) => (
            <GenrePill
              key={f.value}
              active={filters.genre === f.value}
              onClick={() => onChange({ ...filters, genre: f.value })}
            >
              {f.value}
              <span className="ml-1 opacity-60">{f.count}</span>
            </GenrePill>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1.5">
          <Label htmlFor="date-from">From</Label>
          <Input
            id="date-from"
            type="date"
            className="w-[10rem]"
            value={filters.dateFrom}
            onChange={(e) => onChange({ ...filters, dateFrom: e.target.value })}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="date-to">To</Label>
          <Input
            id="date-to"
            type="date"
            className="w-[10rem]"
            value={filters.dateTo}
            onChange={(e) => onChange({ ...filters, dateTo: e.target.value })}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="days">Days</Label>
          <select
            id="days"
            value={filters.weekday}
            onChange={(e) => onChange({ ...filters, weekday: e.target.value as Weekday })}
            className="flex h-9 rounded-md border border-input bg-transparent px-3 text-sm shadow-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="all">Any</option>
            <option value="weekday">Weekday</option>
            <option value="weekend">Weekend (Fri–Sun)</option>
          </select>
        </div>
        {venueOptions.length > 0 && (
          <div className="grid gap-1.5">
            <Label htmlFor="venue">Venue</Label>
            <select
              id="venue"
              value={filters.venue}
              onChange={(e) => onChange({ ...filters, venue: e.target.value })}
              className="flex h-9 max-w-[16rem] rounded-md border border-input bg-transparent px-3 text-sm shadow-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <option value="">Any venue</option>
              {venueOptions.map((f) => (
                <option key={f.value} value={f.value}>
                  {f.value}
                  {f.count > 0 ? ` (${f.count})` : ''}
                </option>
              ))}
            </select>
          </div>
        )}
        {anyActive && (
          <Button
            variant="ghost"
            size="sm"
            className="h-9"
            onClick={() => onChange({ ...EMPTY_FILTERS })}
          >
            <X className="h-3.5 w-3.5" />
            Clear filters
          </Button>
        )}
      </div>
    </div>
  );
}

function GenrePill({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <Button
      variant={active ? 'default' : 'outline'}
      size="sm"
      onClick={onClick}
      aria-pressed={active}
      className={cn('h-7 rounded-full px-3 text-xs')}
    >
      {children}
    </Button>
  );
}
