import { useState } from 'react';
import { Bell, MapPin, Star, Ticket } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { SOURCE_LABELS, type Act, type Event } from '@/lib/types';
import { cn } from '@/lib/utils';

type Props = {
  event: Event;
  // Both absent on the signed-out discover section, which is the same card
  // over a response that carries no account: there is nothing to save a show
  // to and no artist ID to subscribe with, and a star that silently does
  // nothing is worse than no star.
  onToggleSave?: (dedupKey: string, currentlySaved: boolean) => void;
  onToggleSubscribe?: (artistID: string, artistName: string, currentlySubscribed: boolean) => void;
};

// How many acts to show before collapsing. A festival can match dozens of
// the user's artists; the card should stay scannable next to its neighbours
// rather than becoming a page of its own.
const VISIBLE_ACTS = 4;

function formatDay(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
  });
}

// One show. Most events have a single act and render as the familiar card
// with the artist name dominant. When several of the user's artists share a
// bill — a festival, or a headliner whose opener they also listen to — the
// venue leads instead and the artists list beneath it, each keeping its own
// save and subscribe controls because both are per-artist.
export function EventCard({ event, onToggleSave, onToggleSubscribe }: Props) {
  const [expanded, setExpanded] = useState(false);
  // The server groups by event_key and every event it emits has at least one
  // act, so this is defence rather than a case we expect. It is worth having
  // because the alternative is not a bad card: `acts[0]` on an empty array is
  // undefined, reading `.artist` off it throws during render, and React takes
  // the whole SPA to the error boundary -- one malformed row would blank the
  // entire feed rather than cost one card.
  if (event.acts.length === 0) return null;
  const multi = event.acts.length > 1;
  const shown = expanded ? event.acts : event.acts.slice(0, VISIBLE_ACTS);
  const hidden = event.acts.length - shown.length;
  const place = `${event.venue}, ${event.city}${event.state ? `, ${event.state}` : ''}`;
  const solo = event.acts[0];

  return (
    <Card className="transition-shadow hover:shadow-md">
      <CardContent className="flex flex-col gap-3 p-5">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-muted-foreground">
              <span>{formatDay(event.date)}</span>
              {event.is_festival && <Badge variant="muted">Festival</Badge>}
            </div>
            {multi ? (
              <>
                <h3 className="mt-1 truncate text-lg font-semibold">{event.venue}</h3>
                {/* Wraps rather than truncates. On a phone the act count is
                    ~110px of fixed width, which left the city with almost
                    nothing to truncate into; letting the count drop to its
                    own line keeps both readable. */}
                <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
                  <span className="flex min-w-0 items-center gap-1.5">
                    <MapPin className="h-3.5 w-3.5 shrink-0" />
                    <span className="truncate">
                      {event.city}
                      {event.state ? `, ${event.state}` : ''}
                    </span>
                  </span>
                  <Badge variant="muted">{event.acts.length} of your artists</Badge>
                </div>
              </>
            ) : (
              <>
                <div className="mt-1 flex items-center gap-2">
                  <h3 className="truncate text-lg font-semibold">{solo.artist.name}</h3>
                  <BillingLabel billing={solo.billing} />
                </div>
                <ActReason reason={solo.reason} />
                <div className="mt-1 flex items-center gap-1.5 text-sm text-muted-foreground">
                  <MapPin className="h-3.5 w-3.5 shrink-0" />
                  <span className="truncate">{place}</span>
                </div>
              </>
            )}
            {event.name && (
              <p className="mt-1 truncate text-sm text-muted-foreground" title={event.name}>
                {event.name}
              </p>
            )}
          </div>
          {/* A single-act card keeps its controls in the corner, where they
              have always been. A multi-act card has one pair per artist
              below, so a corner pair would be ambiguous. */}
          {!multi && onToggleSave && onToggleSubscribe && (
            <ActControls
              act={solo}
              onToggleSave={onToggleSave}
              onToggleSubscribe={onToggleSubscribe}
            />
          )}
        </div>

        {multi && (
          <ul className="flex flex-col divide-y divide-border/60 border-y border-border/60">
            {shown.map((a) => (
              <li key={a.dedup_key} className="flex items-center justify-between gap-3 py-1.5">
                <div className="min-w-0 flex-1">
                  <span className="flex items-center gap-2">
                    <span className="truncate text-sm font-medium">{a.artist.name}</span>
                    <BillingLabel billing={a.billing} />
                  </span>
                  <ActReason reason={a.reason} />
                  {a.artist.genres && a.artist.genres.length > 0 && (
                    <div className="mt-0.5 flex flex-wrap gap-1">
                      {a.artist.genres.slice(0, 2).map((g) => (
                        <Badge key={g} variant="muted">
                          {g}
                        </Badge>
                      ))}
                    </div>
                  )}
                </div>
                {onToggleSave && onToggleSubscribe && (
                  <ActControls
                    act={a}
                    onToggleSave={onToggleSave}
                    onToggleSubscribe={onToggleSubscribe}
                  />
                )}
              </li>
            ))}
          </ul>
        )}

        {multi && (hidden > 0 || expanded) && (
          <button
            onClick={() => setExpanded(!expanded)}
            className="-my-1.5 self-start py-1.5 text-sm text-muted-foreground underline-offset-4 hover:underline coarse:-my-2.5 coarse:py-2.5"
            aria-expanded={expanded}
          >
            {expanded ? 'Show fewer' : `Show ${hidden} more`}
          </button>
        )}

        {!multi && solo.artist.genres && solo.artist.genres.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {solo.artist.genres.slice(0, 4).map((g) => (
              <Badge key={g} variant="muted">
                {g}
              </Badge>
            ))}
          </div>
        )}

        {event.links.length > 0 && (
          <div className="flex flex-wrap gap-2 border-t border-border/60 pt-3">
            {event.links.map((l) => (
              <Button key={l.url} variant="outline" size="sm" asChild>
                <a href={l.url} target="_blank" rel="noreferrer">
                  <Ticket className="h-3.5 w-3.5" />
                  {SOURCE_LABELS[l.source] ?? l.source}
                </a>
              </Button>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// Where an act sits on the bill, when we have any basis for saying.
//
// Absent billing renders nothing at all rather than a neutral label: it is
// genuinely unknown for every event that did not come from Ticketmaster, and
// a blank is honest where "Support" would be a claim. The wording stays soft
// for the same reason — the ordering behind it is inferred, not published.
function BillingLabel({ billing }: { billing?: Act['billing'] }) {
  if (!billing) return null;
  return <Badge variant="muted">{billing === 'headliner' ? 'Headlining' : 'Support'}</Badge>;
}

// Why this artist is in the feed, in the server's own words. Absent for
// artists the profile has nothing honest to say about, and absent for every
// act on a profile computed before the field existed — so nothing renders
// rather than an empty line or a hedge.
function ActReason({ reason }: { reason?: string }) {
  if (!reason) return null;
  return <p className="mt-0.5 truncate text-xs text-muted-foreground">{reason}</p>;
}

// Star (save) + bell (subscribe) for one artist. Labels name the artist
// because a card can carry several of these, and "Save this show" repeated
// six times tells a screen reader user nothing about which one they're on.
function ActControls({
  act,
  onToggleSave,
  onToggleSubscribe,
}: {
  act: Act;
  onToggleSave: NonNullable<Props['onToggleSave']>;
  onToggleSubscribe: NonNullable<Props['onToggleSubscribe']>;
}) {
  const name = act.artist.name;
  // Unset is the same icon, unfilled — not the `Off` variant. A struck-through
  // bell means "muted" everywhere else on the web, so BellOff read as "we are
  // suppressing alerts for this artist" when it actually meant the opposite,
  // and StarOff said the same thing about a show nobody had saved yet.
  return (
    <div className="flex shrink-0 gap-1">
      <Button
        variant="ghost"
        size="icon"
        onClick={() => onToggleSave(act.dedup_key, !!act.saved)}
        aria-label={act.saved ? `Remove ${name} from saved` : `Save ${name}`}
        title={act.saved ? `Remove ${name} from saved` : `Save ${name}`}
        className={cn(act.saved ? 'text-yellow-500 hover:text-yellow-600' : 'text-muted-foreground')}
      >
        <Star className={act.saved ? 'fill-current' : undefined} />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        onClick={() => act.artist.id && onToggleSubscribe(act.artist.id, name, !!act.subscribed)}
        disabled={!act.artist.id}
        aria-label={act.subscribed ? `Unsubscribe from ${name}` : `Subscribe to ${name}`}
        title={
          act.subscribed
            ? `Stop notifications for ${name}`
            : `Notify me when ${name} has a new show`
        }
        className={cn(act.subscribed ? 'text-primary hover:text-primary/80' : 'text-muted-foreground')}
      >
        <Bell className={act.subscribed ? 'fill-current' : undefined} />
      </Button>
    </div>
  );
}
