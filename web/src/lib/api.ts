// Shared API helpers. Extracted from App.tsx during the frontend rewrite so
// pages/components can call these without reaching into a giant file.

export function readCookie(name: string): string {
  const prefix = name + '=';
  for (const part of document.cookie.split(';')) {
    const s = part.trim();
    if (s.startsWith(prefix)) return decodeURIComponent(s.slice(prefix.length));
  }
  return '';
}

// api.ts is not a component and cannot call a hook, so AuthProvider hands it
// the reactive sign-out for as long as it is mounted. Unset before mount and
// after unmount, which is why every call site is a guarded optional call
// rather than an assertion: a 401 landing in that window must be a no-op, not
// a state update into a dead tree.
type SessionExpiredHandler = () => void;
let sessionExpired: SessionExpiredHandler | null = null;

export function setSessionExpiredHandler(fn: SessionExpiredHandler | null): void {
  sessionExpired = fn;
}

type ApiFetchOptions = {
  // Set for endpoints whose 401 is not a session expiry: /api/site-info is
  // public and reachable while signed out, and /api/auth/me answers 401 as
  // its ordinary signed-out reply. Reacting to either would sign out a user
  // who was never signed in — and for /api/auth/me it is the provider's own
  // first fetch, so it would re-enter the state it is about to set.
  publicEndpoint?: boolean;
};

// Every request in the app goes through here. A 401 on an authenticated
// endpoint means the session died underneath us, and nothing else
// re-evaluates auth after mount: without this the page rendered
// "Error: HTTP 401" while the header still showed the user's name, with no
// way back to /login short of clearing cookies.
export async function apiFetch(
  input: string,
  init: RequestInit = {},
  opts: ApiFetchOptions = {},
): Promise<Response> {
  const r = await fetch(input, { ...init, credentials: 'same-origin' });
  if (r.status === 401 && !opts.publicEndpoint) sessionExpired?.();
  return r;
}

// mutatingFetch injects the CSRF header for POST/PUT/DELETE against /api/me/*.
// It is apiFetch plus that header, not a second path to the network — a
// mutation that 401s has to invalidate the session like anything else.
export function mutatingFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers ?? {});
  const csrf = readCookie('cf_csrf');
  if (csrf) headers.set('X-CSRF-Token', csrf);
  return apiFetch(input, { ...init, headers });
}

// What a failed request says to the user.
//
// The body of a refusal is not user-facing copy: Go's `http.Error` strings are
// written for a log ("radius_miles must be 1..500", "internal error"), and a
// request that dies before reaching the app carries whatever HTML the proxy
// felt like — both of which used to be rendered verbatim, in the interface, as
// the explanation. Status is the only part of a failure this app can read, so
// the message is chosen from it.
//
// `overrides` is for the cases where a handler's meaning is narrower than the
// status: 404 from the location endpoint is "no such city", not "no such page".
export function statusMessage(status: number, overrides: Record<number, string> = {}): string {
  const custom = overrides[status];
  if (custom) return custom;
  if (status === 401) return 'Your session expired. Log in again.';
  if (status === 403) return "That request wasn't allowed. Reload and try again.";
  if (status === 404) return "We couldn't find that.";
  if (status === 409) return "You've reached the limit for this list. Remove something first.";
  if (status === 413) return 'That was too large to send.';
  if (status === 429) return 'Too many requests just now — wait a moment and try again.';
  if (status >= 500) return 'Something went wrong on our end. Try again in a moment.';
  if (status >= 400) return "That didn't work. Check what you entered and try again.";
  return `Something went wrong (HTTP ${status}).`;
}

// The other half: a fetch that rejects never had a status. Its `message` is
// the browser's ("Failed to fetch", "NetworkError when attempting to fetch
// resource"), which is the same defect in a different coat.
export const NETWORK_ERROR_MESSAGE = "Couldn't reach the server. Check your connection and try again.";

// Renders a future instant as a local time the user can act on ("after
// 8:00 PM") rather than an opaque UTC timestamp. Used for both the daily
// quota reset and the manual-refresh cooldown.
export function formatRetry(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return 'the next refresh';
  const sameDay = d.toDateString() === new Date().toDateString();
  const time = d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  return sameDay ? time : `${d.toLocaleDateString(undefined, { weekday: 'long' })} ${time}`;
}

export function timeAgo(iso: string): string {
  const secs = Math.max(1, Math.floor((Date.now() - Date.parse(iso)) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  return `${days}d ago`;
}
