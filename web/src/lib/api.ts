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

// mutatingFetch injects the CSRF header for POST/PUT/DELETE against /api/me/*.
// GETs can just use `fetch` directly.
export async function mutatingFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers ?? {});
  const csrf = readCookie('cf_csrf');
  if (csrf) headers.set('X-CSRF-Token', csrf);
  return fetch(input, { ...init, credentials: 'same-origin', headers });
}

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
