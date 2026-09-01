// Shared types across pages.

export type Me = {
  id: string;
  spotify_user_id: string;
  display_name: string;
  email?: string;
  digest_opt_in?: boolean;
  instant_notify_opt_in?: boolean;
};

export type TicketLink = { source: string; url: string };
export type Artist = { id: string; name: string; genres?: string[] };

export type Concert = {
  artist: Artist;
  date: string;
  venue: string;
  city: string;
  state?: string;
  country?: string;
  links: TicketLink[];
  dedup_key: string;
  saved?: boolean;
  subscribed?: boolean;
};

// One of the user's artists on a bill. Saving and subscribing stay per
// artist even though the artists share a card, so each act keeps the
// dedup_key that save/unsave posts back.
export type Act = {
  artist: Artist;
  dedup_key: string;
  saved?: boolean;
  subscribed?: boolean;
  // Inferred from position in Ticketmaster's attraction list, which is not
  // documented as billing order — see internal/concerts/billing.go. Absent
  // means unknown, which is the honest answer for everything the Phase 2
  // fallback chain finds, and must not be rendered as "support".
  billing?: 'headliner' | 'support';
};

// One show, carrying every artist from the user's profile playing it. A
// festival the user matched six artists at is a single Event — previously
// it was six Concert rows filling most of a screen for one night out.
export type Event = {
  event_key: string;
  // Earliest act's set time; acts within a festival start at different
  // times, so this is for sorting and month grouping, not a claim about
  // when a particular act plays.
  date: string;
  venue: string;
  city: string;
  state?: string;
  country?: string;
  acts: Act[];
  links: TicketLink[];
  // The promoter's title for the night — a festival or a package tour. The
  // server omits it when it would only repeat an act's name, which is what
  // Ticketmaster calls an ordinary club show.
  name?: string;
  // Ticketmaster's own classification, not a guess from the name. Sparse:
  // absent means "not marked", not "definitely not a festival".
  is_festival?: boolean;
};

export type Location = {
  latitude: number;
  longitude: number;
  radius_miles: number;
  display_name?: string;
  // True when the server served the deployment-wide USER_LATITUDE/LONGITUDE
  // fallback because the user has no saved location. Absent once they have one.
  is_default?: boolean;
};

export type Facet = { value: string; count: number };

export type ConcertsResponse = {
  location: Location;
  // Number of events — one per card. Not a count of artist matches, which
  // would report a festival as six.
  count: number;
  events: Event[];
  facets: { genres: Facet[]; venues: Facet[] };
  computed_at?: string;
  refreshing: boolean;
  // False when the scan behind these results didn't cover every artist.
  // Lets the UI distinguish a quiet area from a truncated scan.
  complete: boolean;
  // Set when the shortfall was the daily upstream quota, which resets at
  // this time. Absent when another scan could help sooner.
  retry_after?: string;
};

export type Weekday = 'all' | 'weekday' | 'weekend';

export type FiltersState = {
  genre: string;
  // Venue name as shown in the facet list. The server compares it under the
  // same normalization it uses to build those facets, so differences in
  // case or punctuation between sources don't matter.
  venue: string;
  dateFrom: string;
  dateTo: string;
  weekday: Weekday;
};

// Shared starting point so every page agrees on "no filters applied".
export const EMPTY_FILTERS: FiltersState = {
  genre: '',
  venue: '',
  dateFrom: '',
  dateTo: '',
  weekday: 'all',
};

// Whether the user is looking at a deliberately narrowed list. The empty
// state's wording and the Clear filters button both hinge on this, and an
// empty list is the moment they must agree — telling someone "Nothing saved
// yet" while a genre pill is still lit is the one answer that is false.
export function hasActiveFilters(f: FiltersState): boolean {
  return (
    f.genre !== '' || f.venue !== '' || f.dateFrom !== '' || f.dateTo !== '' || f.weekday !== 'all'
  );
}

export const SOURCE_LABELS: Record<string, string> = {
  ticketmaster: 'Ticketmaster',
  official: 'Official site',
  songkick: 'Songkick',
  // No longer produced, but rows written before Bandsintown was removed still
  // carry these links. Keeping the label means they render as a name rather
  // than a raw slug for the few days it takes the janitor and nightly scans
  // to age them out.
  bandsintown: 'Bandsintown',
};
