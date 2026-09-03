import { useEffect, useState } from 'react';
import { MapPin } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { mutatingFetch, NETWORK_ERROR_MESSAGE, statusMessage } from '@/lib/api';
import type { Location } from '@/lib/types';

// The server rejects anything outside 1..500 with a 400 whose body is a
// developer sentence. A number input that has been emptied reads back as '',
// which Number() makes 0, and a partially-typed value can be NaN — which
// JSON.stringify writes as `null`. Both were being sent.
const MIN_RADIUS = 1;
const MAX_RADIUS = 500;

function clampRadius(v: number, fallback: number): number {
  if (!Number.isFinite(v) || v < MIN_RADIUS) return fallback;
  return Math.min(MAX_RADIUS, Math.round(v));
}

// Compact location card that shows the current radius/city and expands
// inline for editing. Extracted from the old App.tsx and reskinned to
// match the shadcn look.
export function LocationBar({
  location,
  onSaved,
  openEditorToken = 0,
}: {
  location: Location;
  onSaved: (loc: Location) => void;
  // Bumped by the first-run prompt to open the editor and put the cursor in
  // the city field. A counter rather than a boolean: the prompt can ask more
  // than once — the user declines the browser dialog, then cancels out of the
  // editor — and a boolean already true would not fire the second time.
  openEditorToken?: number;
}) {
  const [editing, setEditing] = useState(false);

  useEffect(() => {
    if (openEditorToken > 0) setEditing(true);
  }, [openEditorToken]);
  const [query, setQuery] = useState('');
  const [radius, setRadius] = useState(location.radius_miles);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  useEffect(() => setRadius(location.radius_miles), [location.radius_miles]);

  async function save() {
    setSaving(true);
    setErr('');
    const miles = clampRadius(radius, location.radius_miles);
    setRadius(miles);
    try {
      const body = query
        ? { query, radius_miles: miles }
        : { latitude: location.latitude, longitude: location.longitude, radius_miles: miles };
      const r = await mutatingFetch('/api/me/location', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        setErr(
          statusMessage(r.status, {
            404: "We couldn't find that place. Try “City, State”.",
            429: "You've used a lot of different locations today. Going back to one you already used still works.",
            502: 'Looking up that city failed. Try again in a moment.',
          }),
        );
        return;
      }
      onSaved((await r.json()) as Location);
      setEditing(false);
      setQuery('');
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <CardContent className="flex items-center gap-3 p-4">
        <MapPin className="h-4 w-4 text-primary shrink-0" />
        {!editing ? (
          <>
            <div className="min-w-0 flex-1 text-sm">
              <span className="font-medium">Within {location.radius_miles} mi</span>{' '}
              <span className="text-muted-foreground">
                of {location.display_name ?? `${location.latitude.toFixed(2)}, ${location.longitude.toFixed(2)}`}
              </span>
            </div>
            <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
              Change
            </Button>
          </>
        ) : (
          // A form, not a div of controls: typing a city and pressing Enter is
          // the obvious way to use two text fields, and without a submit
          // handler it did nothing at all.
          <form
            className="flex flex-1 flex-wrap items-end gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (!saving) void save();
            }}
          >
            <div className="grid flex-1 gap-1.5">
              <Label htmlFor="loc-query">City, state</Label>
              <Input
                id="loc-query"
                type="text"
                placeholder="e.g. Washington, DC"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                autoFocus
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="loc-radius">Radius (mi)</Label>
              <Input
                id="loc-radius"
                type="number"
                min={MIN_RADIUS}
                max={MAX_RADIUS}
                className="w-24"
                value={radius}
                onChange={(e) => setRadius(Number(e.target.value))}
              />
            </div>
            <Button type="submit" disabled={saving}>
              Save
            </Button>
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setEditing(false);
                setRadius(location.radius_miles);
                setErr('');
              }}
            >
              Cancel
            </Button>
            {err && <span className="text-xs text-destructive">{err}</span>}
          </form>
        )}
      </CardContent>
    </Card>
  );
}
