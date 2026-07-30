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
  facets: { genres: Facet[] };
  computed_at?: string;
  refreshing: boolean;
};

export type Weekday = 'all' | 'weekday' | 'weekend';

export type FiltersState = {
  genre: string;
  dateFrom: string;
  dateTo: string;
  weekday: Weekday;
};

export const SOURCE_LABELS: Record<string, string> = {
  ticketmaster: 'Ticketmaster',
  bandsintown: 'Bandsintown',
  official: 'Official site',
  songkick: 'Songkick',
};
