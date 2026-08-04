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

export type Location = {
  latitude: number;
  longitude: number;
  radius_miles: number;
  display_name?: string;
};

export type Facet = { value: string; count: number };

export type ConcertsResponse = {
  location: Location;
  count: number;
  concerts: Concert[];
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

export const SOURCE_LABELS: Record<string, string> = {
  ticketmaster: 'Ticketmaster',
  bandsintown: 'Bandsintown',
  official: 'Official site',
  songkick: 'Songkick',
};
