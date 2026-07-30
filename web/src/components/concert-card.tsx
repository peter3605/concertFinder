import { Bell, BellOff, MapPin, Star, StarOff, Ticket } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { SOURCE_LABELS, type Concert } from '@/lib/types';
import { cn } from '@/lib/utils';

type Props = {
  concert: Concert;
  onToggleSave: (dedupKey: string, currentlySaved: boolean) => void;
  onToggleSubscribe: (artistID: string, currentlySubscribed: boolean) => void;
};

function formatDay(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
  });
}

// One concert row. Rich card with clear hierarchy: artist name is the
// dominant type, date is secondary, venue/city tertiary. Star (save) and
// bell (subscribe) live as compact icon buttons in the top-right so they
// don't compete with the ticket-purchase call to action.
export function ConcertCard({ concert, onToggleSave, onToggleSubscribe }: Props) {
  return (
    <Card className="transition-shadow hover:shadow-md">
      <CardContent className="flex flex-col gap-3 p-5">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-muted-foreground">
              <span>{formatDay(concert.date)}</span>
            </div>
            <h3 className="mt-1 truncate text-lg font-semibold">{concert.artist.name}</h3>
            <div className="mt-1 flex items-center gap-1.5 text-sm text-muted-foreground">
              <MapPin className="h-3.5 w-3.5 shrink-0" />
              <span className="truncate">
                {concert.venue}, {concert.city}
                {concert.state ? `, ${concert.state}` : ''}
              </span>
            </div>
          </div>
          <div className="flex shrink-0 gap-1">
            <Button
              variant="ghost"
              size="icon"
              onClick={() => onToggleSave(concert.dedup_key, !!concert.saved)}
              aria-label={concert.saved ? 'Remove from saved' : 'Save this show'}
              title={concert.saved ? 'Remove from saved' : 'Save this show'}
              className={cn(
                concert.saved ? 'text-yellow-500 hover:text-yellow-600' : 'text-muted-foreground',
              )}
            >
              {concert.saved ? <Star className="fill-current" /> : <StarOff />}
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={() =>
                concert.artist.id && onToggleSubscribe(concert.artist.id, !!concert.subscribed)
              }
              disabled={!concert.artist.id}
              aria-label={
                concert.subscribed ? 'Unsubscribe from artist' : 'Subscribe to artist'
              }
              title={
                concert.subscribed
                  ? `Stop notifications for ${concert.artist.name}`
                  : `Notify me when ${concert.artist.name} has a new show`
              }
              className={cn(
                concert.subscribed ? 'text-primary hover:text-primary/80' : 'text-muted-foreground',
              )}
            >
              {concert.subscribed ? <Bell className="fill-current" /> : <BellOff />}
            </Button>
          </div>
        </div>

        {concert.artist.genres && concert.artist.genres.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {concert.artist.genres.slice(0, 4).map((g) => (
              <Badge key={g} variant="muted">
                {g}
              </Badge>
            ))}
          </div>
        )}

        {concert.links.length > 0 && (
          <div className="flex flex-wrap gap-2 border-t border-border/60 pt-3">
            {concert.links.map((l) => (
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
