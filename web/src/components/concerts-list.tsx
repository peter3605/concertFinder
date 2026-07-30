import { RefreshCw } from 'lucide-react';
import { ConcertCard } from '@/components/concert-card';
import { timeAgo } from '@/lib/api';
import type { Concert, ConcertsResponse } from '@/lib/types';

// Shared list rendering used by both the Concerts page and the Saved page.
// Handles the header meta line, empty states, and the month grouping.
type Props = {
  data: ConcertsResponse;
  onToggleSave: (dedupKey: string, currentlySaved: boolean) => void;
  onToggleSubscribe: (artistID: string, artistName: string, currentlySubscribed: boolean) => void;
  emptyMessage?: string;
  firstTimeMessage?: string;
};

export function ConcertsList({
  data,
  onToggleSave,
  onToggleSubscribe,
  emptyMessage = 'No matches. Try clearing filters or widening the radius.',
  firstTimeMessage = 'Setting up your feed for the first time — this takes a minute or two. New results will appear automatically.',
}: Props) {
  const isFirstTime = data.count === 0 && !data.computed_at && data.refreshing;
  const isEmpty = data.count === 0 && data.computed_at;
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <span>
          {data.count} show{data.count === 1 ? '' : 's'}
        </span>
        {data.computed_at && <span aria-hidden>·</span>}
        {data.computed_at && <span>updated {timeAgo(data.computed_at)}</span>}
        {data.refreshing && (
          <span className="flex items-center gap-1">
            <RefreshCw className="h-3.5 w-3.5 animate-spin" />
            refreshing…
          </span>
        )}
      </div>

      {isFirstTime && (
        <p className="rounded-md border border-dashed border-border p-4 text-sm text-muted-foreground">
          {firstTimeMessage}
        </p>
      )}

      {isEmpty && !isFirstTime && (
        <p className="rounded-md border border-dashed border-border p-4 text-sm text-muted-foreground">
          {emptyMessage}
        </p>
      )}

      {groupByMonth(data.concerts).map((g) => (
        <section key={g.key}>
          <h2 className="mb-2 text-xs font-semibold uppercase tracking-widest text-muted-foreground">
            {g.label}
          </h2>
          <div className="grid gap-3">
            {g.items.map((c) => (
              <ConcertCard
                key={c.dedup_key}
                concert={c}
                onToggleSave={onToggleSave}
                onToggleSubscribe={(artistID, currentlySubscribed) =>
                  onToggleSubscribe(artistID, c.artist.name, currentlySubscribed)
                }
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function groupByMonth(concerts: Concert[]) {
  const groups = new Map<string, Concert[]>();
  for (const c of concerts) {
    const d = new Date(c.date);
    const key = `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(c);
  }
  return Array.from(groups.keys())
    .sort()
    .map((key) => {
      const [year, month] = key.split('-').map(Number);
      const label = new Date(Date.UTC(year, month - 1, 1)).toLocaleString(undefined, {
        month: 'long',
        year: 'numeric',
        timeZone: 'UTC',
      });
      return { key, label, items: groups.get(key)! };
    });
}
