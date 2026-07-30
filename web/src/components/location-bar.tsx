import { useEffect, useState } from 'react';
import { MapPin } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { mutatingFetch } from '@/lib/api';
import type { Location } from '@/lib/types';

// Compact location card that shows the current radius/city and expands
// inline for editing. Extracted from the old App.tsx and reskinned to
// match the shadcn look.
export function LocationBar({
  location,
  onSaved,
}: {
  location: Location;
  onSaved: (loc: Location) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [query, setQuery] = useState('');
  const [radius, setRadius] = useState(location.radius_miles);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  useEffect(() => setRadius(location.radius_miles), [location.radius_miles]);

  async function save() {
    setSaving(true);
    setErr('');
    try {
      const body = query
        ? { query, radius_miles: radius }
        : { latitude: location.latitude, longitude: location.longitude, radius_miles: radius };
      const r = await mutatingFetch('/api/me/location', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        setErr(await r.text());
        return;
      }
      onSaved((await r.json()) as Location);
      setEditing(false);
      setQuery('');
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
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
          <div className="flex flex-1 flex-wrap items-end gap-3">
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
                min={1}
                max={500}
                className="w-24"
                value={radius}
                onChange={(e) => setRadius(Number(e.target.value))}
              />
            </div>
            <Button onClick={save} disabled={saving}>
              Save
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                setEditing(false);
                setErr('');
              }}
            >
              Cancel
            </Button>
            {err && <span className="text-xs text-destructive">{err}</span>}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
