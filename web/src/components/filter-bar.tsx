import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { Facet, FiltersState, Weekday } from '@/lib/types';
import { cn } from '@/lib/utils';

type Props = {
  filters: FiltersState;
  facets: Facet[];
  onChange: (f: FiltersState) => void;
};

// Sticky filter block. Sits above the concert list on all breakpoints;
// on wider screens it goes in one row, on mobile it stacks.
export function FilterBar({ filters, facets, onChange }: Props) {
  const topGenres = facets.slice(0, 10);
  const genreActive = filters.genre !== '';
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
      className={cn('h-7 rounded-full px-3 text-xs')}
    >
      {children}
    </Button>
  );
}
