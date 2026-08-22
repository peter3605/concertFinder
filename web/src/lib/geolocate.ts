import type { Location } from './types';

// Default radius for an auto-detected location. Matches the .env fallback and
// what the location picker starts at, so detection changes *where* we look and
// not how wide.
const DEFAULT_RADIUS_MILES = 50;

// How long to wait on the browser before giving up. The permission prompt
// itself does not count against this — the clock starts once the user allows —
// but a device with no recent fix can take a while, and the concerts list is
// already rendering behind it.
const TIMEOUT_MS = 10_000;

// Accept a cached fix up to 5 minutes old. Nobody moves far enough in five
// minutes to change which shows are within 50 miles, and this makes a
// second login instant.
const MAX_AGE_MS = 5 * 60_000;

export type GeolocateOutcome =
  | { kind: 'saved'; location: Location }
  | { kind: 'declined' }
  | { kind: 'unsupported' }
  | { kind: 'failed'; message: string };

/**
 * Ask the browser where the user is and save it as their location.
 *
 * Only call this when the server said `is_default: true` — i.e. the user has
 * never chosen a location and is currently being shown the deployment's
 * configured fallback. A user who has picked a location must never be
 * re-prompted.
 *
 * Every failure path is non-fatal by design. This runs on first login, and the
 * app is fully usable without it: the user keeps the fallback location and can
 * set one by hand in the location bar exactly as before. A denied permission is
 * a legitimate answer, not an error to surface.
 */
export async function detectAndSaveLocation(
  save: (loc: { latitude: number; longitude: number; radius_miles: number }) => Promise<Location>,
): Promise<GeolocateOutcome> {
  if (typeof navigator === 'undefined' || !navigator.geolocation) {
    return { kind: 'unsupported' };
  }

  let position: GeolocationPosition;
  try {
    position = await new Promise<GeolocationPosition>((resolve, reject) => {
      navigator.geolocation.getCurrentPosition(resolve, reject, {
        enableHighAccuracy: false, // city-level is plenty for a 50-mile radius, and coarse mode skips the GPS spin-up
        timeout: TIMEOUT_MS,
        maximumAge: MAX_AGE_MS,
      });
    });
  } catch (e) {
    // GeolocationPositionError codes: 1 PERMISSION_DENIED, 2 POSITION_UNAVAILABLE, 3 TIMEOUT.
    // Checking `code` by number rather than instanceof: the error is a
    // GeolocationPositionError, not an Error, so it fails instanceof Error and
    // has no stack — treating it as a generic throwable loses the distinction
    // between "user said no" and "device could not answer".
    const code = (e as { code?: number } | null)?.code;
    if (code === 1) return { kind: 'declined' };
    return { kind: 'failed', message: code === 3 ? 'timed out' : 'position unavailable' };
  }

  try {
    const location = await save({
      latitude: position.coords.latitude,
      longitude: position.coords.longitude,
      radius_miles: DEFAULT_RADIUS_MILES,
    });
    return { kind: 'saved', location };
  } catch (e) {
    return { kind: 'failed', message: e instanceof Error ? e.message : String(e) };
  }
}
