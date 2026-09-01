import { RefreshCw } from 'lucide-react';
import { EventCard } from '@/components/event-card';
import { formatRetry, timeAgo } from '@/lib/api';
import type { Event, ConcertsResponse } from '@/lib/types';

// Shared list rendering used by both the Concerts page and the Saved page.
// Handles the header meta line, empty states, and the month grouping.
type Props = {
  data: ConcertsResponse;
  onToggleSave: (dedupKey: string, currentlySaved: boolean) => void;
  onToggleSubscribe: (artistID: string, artistName: string, currentlySubscribed: boolean) => void;
  emptyMessage?: string;
  firstTimeMessage?: string;
  // False for lists that are not built by a scan. /me/saved-concerts reads
  // the saves table directly and never sends computed_at, so an empty saved
  // list otherwise reads as a feed still being built.
  awaitsFirstScan?: boolean;
};

// A scan that produces nothing before MAX_REFRESH_POLLS gives up leaves
// refreshing:false with still no snapshot behind it. Requiring refreshing
// here meant that state matched neither branch and the page rendered the
// bare words "0 shows".
const STALLED_MESSAGE =
  'Still building your feed — this is taking longer than usual. Reload to keep waiting.';

export function ConcertsList({
  data,
  onToggleSave,
  onToggleSubscribe,
  emptyMessage = 'No matches. Try clearing filters or widening the radius.',
  firstTimeMessage = 'Setting up your feed for the first time — this takes a minute or two. New results will appear automatically.',
  awaitsFirstScan = true,
}: Props) {
  const isFirstTime = awaitsFirstScan && data.count === 0 && !data.computed_at;
  const isEmpty = data.count === 0 && !isFirstTime;
  // A partial scan and a genuinely quiet area produce the same short list;
  // say which one this is rather than letting the user assume the worst.
  const isPartial = data.complete === false && !data.refreshing;
  return (
    <div className="flex flex-col gap-4">
      <div
        className="flex items-center gap-2 text-sm text-muted-foreground"
        aria-live="polite"
      >
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

      {isPartial && (
        <p className="rounded-md border border-dashed border-border p-4 text-sm text-muted-foreground">
          This is a partial list — we didn&rsquo;t finish checking every artist.
          {data.retry_after
            ? ` More shows should appear after ${formatRetry(data.retry_after)}.`
            : ' More shows should appear on the next refresh.'}
        </p>
      )}

      {isFirstTime && (
        <p className="rounded-md border border-dashed border-border p-4 text-sm text-muted-foreground">
          {data.refreshing ? firstTimeMessage : STALLED_MESSAGE}
        </p>
      )}

      {isEmpty && (
        <p className="rounded-md border border-dashed border-border p-4 text-sm text-muted-foreground">
          {emptyMessage}
        </p>
      )}

      {groupByMonth(data.events).map((g) => (
        <section key={g.key}>
          <h2 className="mb-2 text-xs font-semibold uppercase tracking-widest text-muted-foreground">
            {g.label}
          </h2>
          <div className="grid gap-3">
            {g.items.map((e) => (
              <EventCard
                key={e.event_key}
                event={e}
                onToggleSave={onToggleSave}
                onToggleSubscribe={onToggleSubscribe}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

// Group by the month the show falls in *locally*. EventCard renders each
// date with the browser's timezone, so bucketing by UTC month put a show at
// e.g. 2026-09-01T00:30Z under "September" while its card read "Aug 31".
function groupByMonth(events: Event[]) {
  const groups = new Map<string, Event[]>();
  for (const c of events) {
    const d = new Date(c.date);
    const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(c);
  }
  return Array.from(groups.keys())
    .sort()
    .map((key) => {
      const [year, month] = key.split('-').map(Number);
      const label = new Date(year, month - 1, 1).toLocaleString(undefined, {
        month: 'long',
        year: 'numeric',
      });
      return { key, label, items: groups.get(key)! };
    });
}
