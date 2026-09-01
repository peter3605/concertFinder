import { useEffect, useState } from 'react';
import { EventCard } from '@/components/event-card';
import { apiFetch } from '@/lib/api';
import type { DiscoverResponse, Event } from '@/lib/types';

// Where the signed-out view looks. A visitor to the login page has no account
// and therefore no saved location, and asking the browser for one here is
// exactly the unannounced geolocation prompt the first-run flow was just
// rewritten to stop doing — worse here, because the answer is spent before
// anyone has seen what the app is for.
//
// So it is a fixed coordinate: New York City, matching the USER_LATITUDE /
// USER_LONGITUDE fallback in .env.example. It is also the densest US concert
// market, which matters because this endpoint reads only what other users'
// scans have already cached — a city nobody scans renders nothing at all.
const DEFAULT_LAT = 40.7128;
const DEFAULT_LNG = -74.006;
const DEFAULT_PLACE = 'New York';

// A teaser, not a listings page. The server caps at 50; showing all of them
// would bury the sign-in button this sits underneath.
const MAX_EVENTS = 6;

// "Popular shows near you" for a visitor who has not signed in.
//
// Everything about this is deliberately unpersonalised, and the copy has to
// keep saying so: these are Ticketmaster lineups near a fixed coordinate, not
// anyone's listening. Calling it a feed, or letting a star or a bell appear on
// a card, would promise the thing that only exists after logging in.
//
// Failure is silence. An empty cache, a 4xx, a dropped connection — all render
// nothing. This is the first screen a stranger sees, and an error box under
// the sign-in button is worse than no section at all.
export default function DiscoverSection() {
  const [events, setEvents] = useState<Event[]>([]);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        // publicEndpoint: a 401 here must not sign anyone out. There is
        // nobody signed in, and the provider's own state is not this
        // component's business.
        const r = await apiFetch(
          `/api/discover?lat=${DEFAULT_LAT}&lng=${DEFAULT_LNG}`,
          {},
          { publicEndpoint: true },
        );
        if (!r.ok) return;
        const body = (await r.json()) as DiscoverResponse;
        if (!cancelled) setEvents(body.events ?? []);
      } catch {
        // See above: nothing to show is a normal outcome, not an error state.
      }
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  if (events.length === 0) return null;

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-lg font-semibold">Popular shows near {DEFAULT_PLACE}</h2>
        <p className="text-sm text-muted-foreground">
          A sample of what&rsquo;s on sale — not picked for you. Log in and the list is built from
          the artists you actually listen to, in your own city.
        </p>
      </div>
      <div className="grid gap-3">
        {events.slice(0, MAX_EVENTS).map((e) => (
          <EventCard key={e.event_key} event={e} />
        ))}
      </div>
    </section>
  );
}
