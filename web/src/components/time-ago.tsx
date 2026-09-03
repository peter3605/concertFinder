import { useEffect, useState } from 'react';
import { timeAgo } from '@/lib/api';

// 30s rather than a minute: timeAgo renders the first minute in seconds, so a
// minute-long tick would leave "12s ago" frozen for most of the interval it
// was meant to fix.
const TICK_MS = 30_000;

// "updated 1m ago" used to be computed once, when the list rendered. A page
// that has finished loading and is not being interacted with does not render
// again, so the one number on screen whose entire job is to be current was the
// one that stayed still — reading "1m ago" an hour later.
export function TimeAgo({ iso }: { iso: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => tick((t) => t + 1), TICK_MS);
    return () => clearInterval(id);
  }, []);
  return <time dateTime={iso}>{timeAgo(iso)}</time>;
}
