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

type SiteInfo = { invite_required?: boolean; spotify_signups_open?: boolean; apple_sign_in?: boolean };

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
  // An ordinary invite presented for a Spotify signup (CF-B33). The code
  // comes back with the refusal, unspent, so the email form below is already
  // filled in.
  invite_not_spotify: {
    tab: 'create',
    message:
      "That invite is for creating an account with email or Apple, not Spotify. It hasn't been used, so you can use it below. Spotify sign-up needs a Spotify invite, which is only for people the operator has added on Spotify's side.",
  },
  spotify_full: {
    tab: 'create',
    message:
      "ConcertFinder can't take any more Spotify accounts right now. Your invite code hasn't been used, so you can create your account with email instead.",
  },
  // The Apple callback's own refusal: a token that would not verify, or an
  // exchange with Apple that failed. Not an invite problem, so it lands on
  // whichever tab is open by default.
  apple_failed: {
    tab: 'signin',
    message: "Sign in with Apple didn't complete. Please try again, or use email instead.",
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
  // False until site-info says the Apple routes are mounted, so a deployment
  // without the APPLE_* set (every local stack) never offers a button that
  // leads to a 404.
  const [appleSignIn, setAppleSignIn] = useState(false);
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
        setAppleSignIn(Boolean(j.apple_sign_in));
      })
      .catch(() => {});
  }, []);

  // Trimmed here as well as on the server. The server is the authority (it
  // normalizes case, spaces and underscores), but sending a trailing space
  // through the query string helps nobody.
  const code = inviteRequired ? invite.trim() : '';
  const needsCode = inviteRequired && !code;

  // Whether the code typed so far is a Spotify invite (CF-B33), from
  // GET /api/auth/invite. Spotify sign-up is offered only for one: an
  // ordinary invite holder is not on Spotify's allowlist, and sending them
  // through consent ends on a refusal. Like the site-info flags this decides
  // what is shown, not who gets in; /login and the callback check the kind
  // again. With the gate off there is no code to ask about.
  const [spotifyInvite, setSpotifyInvite] = useState(false);
  useEffect(() => {
    if (!inviteRequired || !code) {
      setSpotifyInvite(false);
      return;
    }
    let cancelled = false;
    // Debounced: the check runs as the person types, and /api/auth's
    // per-IP bucket is shared with the sign-in itself.
    const timer = window.setTimeout(() => {
      apiFetch(`/api/auth/invite?code=${encodeURIComponent(code)}`, {}, { publicEndpoint: true })
        .then((r) => (r.ok ? r.json() : null))
        .then((j: { usable?: boolean; spotify?: boolean } | null) => {
          if (!cancelled) setSpotifyInvite(Boolean(j?.usable && j?.spotify));
        })
        .catch(() => {
          if (!cancelled) setSpotifyInvite(false);
        });
    }, 300);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [inviteRequired, code]);
  const offerSpotifySignup = spotifySignupsOpen && (!inviteRequired || spotifyInvite);
  const signupSpotifyHref = code
    ? `/api/auth/login?invite=${encodeURIComponent(code)}`
    : '/api/auth/login';
  // Same rule as Spotify's: the code rides to the callback in the handshake
  // and is spent only if Apple's answer creates an account.
  const signupAppleHref = code
    ? `/api/auth/apple/login?invite=${encodeURIComponent(code)}`
    : '/api/auth/apple/login';

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
              <AppleSlot>{appleSignIn && <AppleButton href="/api/auth/apple/login" />}</AppleSlot>
              <EmailSignIn invite="" label="Email" submitLabel="Email me a sign-in link" />
              <Divider />
              <SpotifyButton href="/api/auth/login" note="For accounts that were invited to sign in with Spotify." />
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
              <AppleSlot>
                {appleSignIn && <AppleButton href={signupAppleHref} disabled={needsCode} />}
              </AppleSlot>
              {/* The code rides in the email token and is spent only when
                  the link or code is used, so an unclicked email costs
                  nothing. */}
              <EmailSignIn
                invite={code}
                label="Email"
                submitLabel="Email me a link to create my account"
                disabled={needsCode}
              />
              {offerSpotifySignup && (
                <>
                  <Divider />
                  <SpotifyButton
                    href={signupSpotifyHref}
                    note={
                      inviteRequired
                        ? 'Your invite is a Spotify invite. We only read your library and never post, follow, or change anything.'
                        : 'Only a few Spotify accounts can join, and only ones Spotify lets us add. We only read your library and never post, follow, or change anything.'
                    }
                  />
                </>
              )}
              {spotifySignupsOpen && inviteRequired && !spotifyInvite && (
                <p className="text-center text-xs text-muted-foreground">
                  Spotify sign-up needs a Spotify invite, for the few people
                  the operator has added on Spotify&apos;s side. Enter one
                  above to continue with Spotify.
                </p>
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

// Sign in with Apple, above email on both tabs. `empty:hidden` keeps the slot
// from adding a gap where Apple is not configured.
function AppleSlot({ children }: { children?: ReactNode }) {
  return (
    <div data-slot="apple-sign-in" className="empty:hidden">
      {children}
    </div>
  );
}

// Apple's guidelines: black or white, the Apple logo, "Sign in with Apple" or
// "Continue with Apple", and no smaller than other sign-in buttons. Black on
// light and white on dark, picked by CSS so there is no flash on first paint.
// A plain link: /api/auth/apple/login sets the handshake cookie and redirects
// to Apple, which needs a top-level navigation, not a fetch.
function AppleButton({ href, disabled = false }: { href: string; disabled?: boolean }) {
  const cls =
    'inline-flex h-10 w-full items-center justify-center gap-2 rounded-md bg-black px-4 text-sm font-medium text-white transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:bg-white dark:text-black';
  const content = (
    <>
      <AppleLogo />
      Continue with Apple
    </>
  );
  if (disabled) {
    return (
      <button type="button" disabled className={cn(cls, 'cursor-not-allowed opacity-50 hover:opacity-50')}>
        {content}
      </button>
    );
  }
  return (
    <a href={href} className={cls}>
      {content}
    </a>
  );
}

function AppleLogo() {
  return (
    <svg aria-hidden="true" viewBox="0 0 170 170" className="h-4 w-4" fill="currentColor">
      <path d="M150.37 130.25c-2.45 5.66-5.35 10.87-8.71 15.66-4.58 6.53-8.33 11.05-11.22 13.56-4.48 4.12-9.28 6.23-14.42 6.35-3.69 0-8.14-1.05-13.32-3.18-5.2-2.12-9.97-3.17-14.34-3.17-4.58 0-9.49 1.05-14.75 3.17-5.27 2.13-9.51 3.24-12.76 3.35-4.92.21-9.84-1.96-14.75-6.52-3.13-2.73-7.05-7.41-11.75-14.04-5.03-7.08-9.17-15.29-12.41-24.65-3.47-10.11-5.21-19.9-5.21-29.38 0-10.86 2.35-20.22 7.04-28.07 3.69-6.3 8.61-11.27 14.76-14.92s12.79-5.51 19.95-5.63c3.91 0 9.05 1.21 15.43 3.59 6.36 2.39 10.45 3.6 12.24 3.6 1.34 0 5.88-1.42 13.57-4.24 7.28-2.62 13.42-3.7 18.45-3.28 13.63 1.1 23.87 6.47 30.68 16.15-12.19 7.39-18.22 17.73-18.1 31 .11 10.34 3.86 18.94 11.23 25.77 3.34 3.17 7.07 5.62 11.22 7.36-.9 2.61-1.85 5.11-2.86 7.51zM119.11 7.24c0 8.1-2.96 15.67-8.86 22.67-7.12 8.32-15.73 13.13-25.07 12.37a25.2 25.2 0 0 1-.19-3.07c0-7.78 3.39-16.1 9.4-22.9 3-3.44 6.82-6.31 11.45-8.6 4.62-2.26 8.99-3.51 13.1-3.72.12 1.08.17 2.17.17 3.25z" />
    </svg>
  );
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

// Secondary on purpose: most people cannot use it, which the note under it
// says in so many words. On Create an account it also carries the library
// line, because that describes what this button grants and nothing else on
// the page does.
function SpotifyButton({ href, note, disabled = false }: { href: string; note: string; disabled?: boolean }) {
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
      <p className="text-center text-xs text-muted-foreground">{note}</p>
    </div>
  );
}
