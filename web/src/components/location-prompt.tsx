import { useState } from 'react';
import { MapPin } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { detectAndSaveLocation } from '@/lib/geolocate';
import type { Location } from '@/lib/types';

type Props = {
  // Writes the detected fix as the user's location. Shared with the location
  // bar so both paths save the same way.
  save: (body: { latitude: number; longitude: number; radius_miles: number }) => Promise<Location>;
  onLocated: (loc: Location) => void;
  // Hands the user to the location bar's city field. Called both when they
  // ask for it and when the browser refuses, so declining is never a dead end.
  onTypeCity: () => void;
};

// Shown once, before the browser's geolocation dialog, to a user who has never
// chosen a location and is currently looking at the deployment's fallback city.
//
// The card exists because the browser prompt it leads to is a one-shot: a
// denial is remembered by the browser per origin, is not re-askable from
// JavaScript, and takes a trip into site settings to undo. Firing it
// unannounced — which is what this page used to do on first login — spends
// that single question on a dialog the user had no reason to expect, and the
// reflex answer to an unexplained one is no. So the reason goes first, and the
// city field is offered beside it rather than as a consolation afterwards.
export function LocationPrompt({ save, onLocated, onTypeCity }: Props) {
  const [busy, setBusy] = useState(false);
  // Set once the browser has answered badly. `refused` also hides the button:
  // after a denial the same call returns the same error instantly, so leaving
  // it there offers a control that cannot work.
  const [note, setNote] = useState('');
  const [refused, setRefused] = useState(false);

  async function useBrowser() {
    setBusy(true);
    setNote('');
    try {
      const outcome = await detectAndSaveLocation(save);
      if (outcome.kind === 'saved') {
        onLocated(outcome.location);
        return;
      }
      if (outcome.kind === 'declined' || outcome.kind === 'unsupported') {
        setRefused(true);
      }
      setNote(
        outcome.kind === 'declined'
          ? 'No problem — type a city instead.'
          : "We couldn't get a location from your browser. Type a city instead.",
      );
      onTypeCity();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardContent className="flex flex-col gap-3 p-4">
        <div className="flex items-start gap-3">
          <MapPin className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
          <div className="text-sm">
            <p className="font-medium">Where should we look for shows?</p>
            <p className="mt-1 text-muted-foreground">
              Until you tell us, this list is for a default city that is probably not yours. Your
              browser will ask before sharing anything, and we keep a rounded coordinate and a
              radius — never a location history.
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 pl-7">
          {!refused && (
            <Button size="sm" onClick={useBrowser} disabled={busy}>
              {busy ? 'Locating…' : 'Use my location'}
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={onTypeCity}>
            Type a city instead
          </Button>
          {note && <span className="text-xs text-muted-foreground">{note}</span>}
        </div>
      </CardContent>
    </Card>
  );
}
