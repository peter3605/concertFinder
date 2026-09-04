import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Music2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { apiFetch } from '@/lib/api';
import { useDocumentTitle } from '@/lib/use-document-title';
import DiscoverSection from '@/pages/discover';

type SiteInfo = { invite_required?: boolean };

// Landing page for anonymous users. Explains what the app is + a single
// prominent CTA to start the Spotify OAuth flow.
export default function LoginPage() {
  useDocumentTitle('Log in');
  const [params] = useSearchParams();
  // The box is shown only when the server says signups are gated. It is a
  // rendering hint and nothing more — the gate itself is in the auth
  // callback, which is the only place that can tell a signup from a returning
  // user. A client that guessed wrong would show or hide a field; it could
  // never let anyone in.
  const [inviteRequired, setInviteRequired] = useState(false);
  // Pre-filled from ?invite= so an invite can be sent as a single link.
  const [invite, setInvite] = useState(params.get('invite') ?? '');

  useEffect(() => {
    // Public endpoint, and this page is the one anonymous users land on: a
    // 401 here must not be read as a session that expired.
    apiFetch('/api/site-info', {}, { publicEndpoint: true })
      .then((r) => (r.ok ? r.json() : null))
      .then((j: SiteInfo | null) => j && setInviteRequired(Boolean(j.invite_required)))
      .catch(() => {});
  }, []);

  // Trimmed here as well as on the server. The server is the authority — it
  // normalizes case, spaces and underscores — but sending a trailing space
  // through the query string for no reason helps nobody.
  const loginHref =
    inviteRequired && invite.trim()
      ? `/api/auth/login?invite=${encodeURIComponent(invite.trim())}`
      : '/api/auth/login';

  return (
    // Wider than the card so the discover cards below it aren't squeezed into
    // the sign-in column; the card keeps its own max-w-md.
    <div className="mx-auto mt-16 flex max-w-2xl flex-col gap-10">
      <Card className="mx-auto w-full max-w-md">
        <CardHeader className="items-center text-center">
          <div className="mb-2 inline-flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary">
            <Music2 className="h-6 w-6" />
          </div>
          <CardTitle className="text-2xl">Welcome to ConcertFinder</CardTitle>
          <CardDescription>
            A personal concert feed built from your Spotify listening. Log in to
            start.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-3">
          {inviteRequired && (
            <div className="w-full space-y-1.5">
              <Label htmlFor="invite">Invite code</Label>
              <Input
                id="invite"
                name="invite"
                value={invite}
                onChange={(e) => setInvite(e.target.value)}
                placeholder="CF-XXXX-XXXX"
                autoComplete="off"
                autoCapitalize="characters"
                spellCheck={false}
              />
              {/* Says which half of the flow needs the code. Without this,
                  an existing user reads "invite code" as a wall and does not
                  try the button they are perfectly entitled to press. */}
              <p className="text-xs text-muted-foreground">
                New accounts need an invite while ConcertFinder is in early
                access. Already have an account? Just log in — you don't need a
                code.
              </p>
            </div>
          )}
          <Button asChild size="lg" className="w-full">
            <a href={loginHref}>Log in with Spotify</a>
          </Button>
          <p className="text-center text-xs text-muted-foreground">
            We only read your library — never post, follow, or change anything.
            See{' '}
            <Link to="/privacy" className="text-primary hover:underline">
              Privacy
            </Link>{' '}
            for details.
          </p>
        </CardContent>
      </Card>
      {/* Renders nothing when the shared cache has nothing near the default
          coordinate — no empty state and no error. It is a sample of what the
          app finds, and a box explaining that there is no sample would be
          worse than the absence. */}
      <DiscoverSection />
    </div>
  );
}
