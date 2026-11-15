import { useEffect, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Music2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { apiFetch } from '@/lib/api';
import { cn } from '@/lib/utils';
import { useDocumentTitle } from '@/lib/use-document-title';
import DiscoverSection from '@/pages/discover';
import { EmailSignIn } from '@/components/email-sign-in';

type SiteInfo = { invite_required?: boolean; spotify_signups_open?: boolean };

type Tab = 'signin' | 'create';

// The refusals the auth callback sends back as /login?error=<code>. The
// codes are the LoginError* constants in internal/auth/handlers.go; a code
// missing here still shows the generic message on the sign-in tab. Every
// one of these belongs to someone creating an account: a returning user is
// on the allowlist, needs no code, and holds a Spotify place already.
const REFUSALS: Record<string, { tab: Tab; message: string }> = {
  invite_required: {
    tab: 'create',
    message:
      'Creating an account needs an invite code while ConcertFinder is in early access. Enter yours below. Already have an account? Use Sign in instead; accounts never need a code.',
  },
  invite_invalid: {
    tab: 'create',
    message:
      "That invite code isn't valid. It may have been used up or expired. Check it for typos, or ask whoever sent it for another.",
  },
  spotify_full: {
    tab: 'create',
    message:
      "ConcertFinder can't take any more Spotify accounts right now. Your invite code hasn't been used, so you can create your account with email instead.",
  },
  spotify_not_registered: {
    tab: 'create',
    message:
      "Spotify only lets accounts the operator has added use this app. Your invite code hasn't been used, so you can create your account with email instead.",
  },
};

const GENERIC_REFUSAL = { tab: 'signin' as Tab, message: "Sign-in didn't complete. Please try again." };

// Landing page for anonymous users: a Sign in tab for people with an account
// and a Create an account tab for everyone else.
//
// Email leads both because it is the sign-in open to everyone. Spotify is
// capped at a handful of accounts (SPOTIFY_MAX_USERS, plus Spotify's own
// Development Mode allowlist), so it is offered to returning users always
// and to new ones only while site-info says a place is free.
export default function LoginPage() {
  useDocumentTitle('Log in');
  const [params] = useSearchParams();
  const errorCode = params.get('error');
  const refusal = errorCode ? (REFUSALS[errorCode] ?? GENERIC_REFUSAL) : null;

  // Both flags are rendering hints and nothing more. The gates are in the
  // auth callback and db.UpsertUserWithAdmission, the only places that can
  // tell a signup from a returning user; a client that guessed wrong would
  // show or hide a control, never let anyone in.
  const [inviteRequired, setInviteRequired] = useState(false);
  const [spotifySignupsOpen, setSpotifySignupsOpen] = useState(false);
  // Pre-filled from ?invite= so an invite can be sent as a single link. The
  // callback also passes an unspent code back on a refusal, so it is still
  // there when the person tries email instead.
  const [invite, setInvite] = useState(params.get('invite') ?? '');
  const [tab, setTab] = useState<Tab>(
    refusal ? refusal.tab : params.get('invite') ? 'create' : 'signin',
  );

  useEffect(() => {
    // Public endpoint, and this page is the one anonymous users land on: a
    // 401 here must not be read as a session that expired.
    apiFetch('/api/site-info', {}, { publicEndpoint: true })
      .then((r) => (r.ok ? r.json() : null))
      .then((j: SiteInfo | null) => {
        if (!j) return;
        setInviteRequired(Boolean(j.invite_required));
        setSpotifySignupsOpen(Boolean(j.spotify_signups_open));
      })
      .catch(() => {});
  }, []);

  // Trimmed here as well as on the server. The server is the authority (it
  // normalizes case, spaces and underscores), but sending a trailing space
  // through the query string helps nobody.
  const code = inviteRequired ? invite.trim() : '';
  const needsCode = inviteRequired && !code;
  const signupSpotifyHref = code
    ? `/api/auth/login?invite=${encodeURIComponent(code)}`
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
            Upcoming shows near you by the artists you care about, in one feed.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div role="tablist" aria-label="Sign in or create an account" className="grid grid-cols-2 rounded-md bg-muted p-1">
            <TabButton id="signin" current={tab} onSelect={setTab}>
              Sign in
            </TabButton>
            <TabButton id="create" current={tab} onSelect={setTab}>
              Create an account
            </TabButton>
          </div>

          {refusal && refusal.tab === tab && (
            <p role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
              {refusal.message}
            </p>
          )}

          {tab === 'signin' ? (
            <div role="tabpanel" id="panel-signin" aria-labelledby="tab-signin" className="flex flex-col gap-3">
              <AppleSlot />
              <EmailSignIn invite="" label="Email" submitLabel="Email me a sign-in link" />
              <Divider />
              <SpotifyButton href="/api/auth/login" />
            </div>
          ) : (
            <div role="tabpanel" id="panel-create" aria-labelledby="tab-create" className="flex flex-col gap-3">
              {inviteRequired && (
                <div className="space-y-1.5">
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
                  <p className="text-xs text-muted-foreground">
                    New accounts need an invite while ConcertFinder is in early
                    access.
                  </p>
                </div>
              )}
              <AppleSlot />
              {/* The code rides in the email token and is spent only when
                  the link or code is used, so an unclicked email costs
                  nothing. */}
              <EmailSignIn
                invite={code}
                label="Email"
                submitLabel="Email me a link to create my account"
                disabled={needsCode}
              />
              {spotifySignupsOpen && (
                <>
                  <Divider />
                  <SpotifyButton href={signupSpotifyHref} disabled={needsCode} />
                </>
              )}
            </div>
          )}

          <p className="text-center text-xs text-muted-foreground">
            See{' '}
            <Link to="/privacy" className="text-primary hover:underline">
              Privacy
            </Link>{' '}
            and{' '}
            <Link to="/terms" className="text-primary hover:underline">
              Terms
            </Link>
            .
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

function TabButton({
  id,
  current,
  onSelect,
  children,
}: {
  id: Tab;
  current: Tab;
  onSelect: (t: Tab) => void;
  children: ReactNode;
}) {
  const selected = id === current;
  return (
    <button
      type="button"
      role="tab"
      id={`tab-${id}`}
      aria-selected={selected}
      aria-controls={`panel-${id}`}
      onClick={() => onSelect(id)}
      className={cn(
        'rounded-sm px-3 py-1.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        selected ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  );
}

// Reserved for Sign in with Apple (CF-36), above email on both tabs. Empty
// until then, and `empty:hidden` keeps an empty slot from adding a gap.
function AppleSlot() {
  return <div data-slot="apple-sign-in" className="empty:hidden" />;
}

function Divider() {
  return (
    <div className="flex items-center gap-3 text-xs text-muted-foreground">
      <span className="h-px flex-1 bg-border" />
      or
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}

// Secondary on purpose: most people cannot use it. The library line sits
// beside it because it describes what this button grants and nothing else on
// the page.
function SpotifyButton({ href, disabled = false }: { href: string; disabled?: boolean }) {
  return (
    <div className="space-y-1.5">
      {disabled ? (
        <Button variant="outline" className="w-full" disabled>
          Continue with Spotify
        </Button>
      ) : (
        <Button asChild variant="outline" className="w-full">
          <a href={href}>Continue with Spotify</a>
        </Button>
      )}
      <p className="text-center text-xs text-muted-foreground">
        With Spotify we only read your library. We never post, follow, or change
        anything.
      </p>
    </div>
  );
}
