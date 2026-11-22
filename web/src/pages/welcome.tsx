import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { EmailAddressForm } from '@/components/email-sign-in';
import { LocationBar } from '@/components/location-bar';
import { LocationPrompt } from '@/components/location-prompt';
import { PicksEditor, type PickCounts } from '@/components/picks-editor';
import { apiFetch, mutatingFetch, NETWORK_ERROR_MESSAGE, statusMessage } from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { hasSpotify, type Location, type Me } from '@/lib/types';
import { useDocumentTitle } from '@/lib/use-document-title';

type Step = 'name' | 'email' | 'location' | 'picks';

const STEP_TITLES: Record<Step, string> = {
  name: 'What should we call you?',
  email: 'Where should we send updates?',
  location: 'Where do you go to shows?',
  picks: 'Who do you want to see?',
};

// First run for an account that has nothing to build a feed from: one that
// signed in with Apple or email, so there is no Spotify library behind it.
//
// Each step is asked only if it is still missing, in this order:
//
//   name     Email sign-in never supplies one, and Apple shares it only the
//            first time and only if the person agrees. Account deletion
//            confirms against it, so an account without one cannot delete
//            itself.
//   email    Apple may hide the address or the person may not share it. The
//            digest and alerts need one. Skippable: the feed works without it.
//   location Before picks on purpose. The first scan starts as soon as there
//            is something to scan, and with no location it would search the
//            deployment's default city, which is probably not theirs.
//   picks    Artists or genres, at least one. With neither, the server has
//            nothing to scan and answers needs_picks instead of queueing one.
//
// The concerts page sends people here when its response says needs_picks or
// the account has no name. A Spotify account never sees it: its library is
// the feed, and it adds picks from Settings.
export default function WelcomePage() {
  useDocumentTitle('Welcome');
  const { auth, setMe } = useAuth();
  const navigate = useNavigate();
  const [steps, setSteps] = useState<Step[] | null>(null);
  const [index, setIndex] = useState(0);
  const [location, setLocation] = useState<Location | null>(null);

  const me = auth.kind === 'signed_in' ? auth.me : null;

  // Decided once, on arrival. Recomputing as answers land would drop the step
  // being worked on out from under the person the moment it is satisfied.
  useEffect(() => {
    if (!me || steps) return;
    let cancelled = false;
    (async () => {
      let loc: Location | null = null;
      try {
        const r = await apiFetch('/api/me/location');
        loc = r.ok ? ((await r.json()) as Location) : null;
      } catch {
        loc = null;
      }
      if (cancelled) return;
      setLocation(loc);
      const todo: Step[] = [];
      if (!me.display_name) todo.push('name');
      if (!me.email) todo.push('email');
      if (!loc || loc.is_default) todo.push('location');
      if (!hasSpotify(me)) todo.push('picks');
      if (todo.length === 0) {
        navigate('/', { replace: true });
        return;
      }
      setSteps(todo);
    })();
    return () => {
      cancelled = true;
    };
  }, [me, steps, navigate]);

  if (!me || !steps) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const step = steps[index];
  const last = index === steps.length - 1;
  const next = () => (last ? navigate('/', { replace: true }) : setIndex((i) => i + 1));

  return (
    <div className="mx-auto flex max-w-xl flex-col gap-6">
      <div>
        <p className="text-sm text-muted-foreground">
          Step {index + 1} of {steps.length}
        </p>
        <h1 className="text-2xl font-semibold">{STEP_TITLES[step]}</h1>
      </div>
      {step === 'name' && <NameStep me={me} setMe={setMe} onDone={next} />}
      {step === 'email' && <EmailStep me={me} setMe={setMe} onDone={next} />}
      {step === 'location' && (
        <LocationStep location={location} setLocation={setLocation} onDone={next} last={last} />
      )}
      {step === 'picks' && <PicksStep onDone={next} />}
    </div>
  );
}

function StepCard({ description, children }: { description: ReactNode; children: ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="sr-only">Details</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">{children}</CardContent>
    </Card>
  );
}

function NameStep({ me, setMe, onDone }: { me: Me; setMe: (m: Me) => void; onDone: () => void }) {
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr('');
    try {
      const r = await mutatingFetch('/api/me/profile', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ display_name: name }),
      });
      if (!r.ok) {
        setErr(statusMessage(r.status, { 400: 'Use 1 to 64 characters, with no line breaks.' }));
        return;
      }
      const j = (await r.json()) as { display_name: string };
      setMe({ ...me, display_name: j.display_name });
      onDone();
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  return (
    <StepCard description="Shown in the app and in the emails we send you. Nobody else sees it.">
      <form onSubmit={save} className="flex flex-col gap-3">
        <Label htmlFor="display-name">Your name</Label>
        <Input
          id="display-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoComplete="name"
          maxLength={64}
          autoFocus
        />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <Button type="submit" disabled={busy || !name.trim()}>
          {busy ? 'Saving…' : 'Continue'}
        </Button>
      </form>
    </StepCard>
  );
}

function EmailStep({ me, setMe, onDone }: { me: Me; setMe: (m: Me) => void; onDone: () => void }) {
  return (
    <StepCard description="A weekly email of new shows near you, and an alert when an artist you follow announces one. Confirming the address also lets you sign in with it.">
      <EmailAddressForm
        current=""
        onVerified={(email) => {
          setMe({ ...me, email });
          onDone();
        }}
      />
      <Button variant="ghost" onClick={onDone}>
        Skip for now
      </Button>
      <p className="text-xs text-muted-foreground">You can add one later in Settings.</p>
    </StepCard>
  );
}

function LocationStep({
  location,
  setLocation,
  onDone,
  last,
}: {
  location: Location | null;
  setLocation: (l: Location) => void;
  onDone: () => void;
  last: boolean;
}) {
  const [editToken, setEditToken] = useState(0);
  const chosen = location !== null && !location.is_default;

  async function save(body: { latitude: number; longitude: number; radius_miles: number }): Promise<Location> {
    const r = await mutatingFetch('/api/me/location', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!r.ok) throw new Error(statusMessage(r.status));
    return (await r.json()) as Location;
  }

  return (
    <StepCard description="We look for shows within a radius of one place. You can change it any time from the concerts page.">
      {!chosen && (
        <LocationPrompt save={save} onLocated={setLocation} onTypeCity={() => setEditToken((v) => v + 1)} />
      )}
      {location && <LocationBar location={location} onSaved={setLocation} openEditorToken={editToken} />}
      <Button onClick={onDone} disabled={!chosen}>
        {last ? 'Show my concerts' : 'Continue'}
      </Button>
    </StepCard>
  );
}

function PicksStep({ onDone }: { onDone: () => void }) {
  const [counts, setCounts] = useState<PickCounts>({ artists: 0, genres: 0 });
  const onCounts = useCallback((c: PickCounts) => setCounts(c), []);
  const any = counts.artists + counts.genres > 0;
  return (
    <StepCard description="Pick artists, genres, or both. Your feed shows their upcoming concerts near you, and you can change these in Settings.">
      <PicksEditor onCountsChange={onCounts} />
      <Button onClick={onDone} disabled={!any}>
        {any ? 'Show my concerts' : 'Pick at least one artist or genre'}
      </Button>
    </StepCard>
  );
}
