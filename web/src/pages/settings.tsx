import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle, Mail, Music2, Unplug } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Label } from '@/components/ui/label';
import { mutatingFetch, NETWORK_ERROR_MESSAGE, statusMessage } from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { hasSpotify, type Me } from '@/lib/types';
import { useDocumentTitle } from '@/lib/use-document-title';
import { EmailAddressForm } from '@/components/email-sign-in';
import { PicksEditor } from '@/components/picks-editor';

export default function SettingsPage() {
  useDocumentTitle('Settings');
  const { auth, setMe } = useAuth();
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  if (auth.kind !== 'signed_in') {
    return <p className="text-sm text-muted-foreground">Log in to change settings.</p>;
  }
  const me = auth.me;
  const spotify = hasSpotify(me);

  async function update(patch: Partial<Pick<Me, 'digest_opt_in' | 'instant_notify_opt_in'>>) {
    setSaving(true);
    setErr('');
    try {
      const r = await mutatingFetch('/api/me/email-prefs', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(patch),
      });
      if (!r.ok) {
        // Never the response body: these are Go http.Error strings, and on a
        // bad gateway they are the proxy's HTML.
        setErr(
          statusMessage(r.status, {
            400: "That setting didn't save. Reload and try again.",
            409: 'Add and confirm an email address first.',
          }),
        );
        return;
      }
      setMe({ ...me, ...patch });
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">Settings</h1>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Account</CardTitle>
          <CardDescription>Your sign-in identities + email.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3 text-sm">
          {/* A Spotify account's name is rewritten from Spotify at every
              sign-in, so an edit would be silently undone; only accounts
              without one get the field. */}
          {spotify ? (
            <div className="flex justify-between">
              <span className="text-muted-foreground">Display name</span>
              <span>{me.display_name || me.spotify_user_id}</span>
            </div>
          ) : (
            <DisplayNameRow me={me} setMe={setMe} />
          )}
          {spotify && (
            <div className="flex justify-between">
              <span className="text-muted-foreground">Spotify ID</span>
              <span className="font-mono text-xs">{me.spotify_user_id}</span>
            </div>
          )}
          {/* Replaces "Log in again to grant email access", which no
              re-login could satisfy once Spotify stopped returning the
              address. Confirming one also makes it a way to sign in. */}
          <div className="flex flex-col gap-2 sm:flex-row sm:justify-between">
            <span className="text-muted-foreground">Email</span>
            <div className="flex flex-col items-start gap-2 sm:items-end">
              {me.email && <span>{me.email}</span>}
              <EmailAddressForm current={me.email ?? ''} onVerified={(email) => setMe({ ...me, email })} />
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Music2 className="h-4 w-4 text-primary" /> Your artists and genres
          </CardTitle>
          <CardDescription>
            {spotify
              ? 'Add artists your Spotify library misses. These are searched first, then your listening fills the rest of your feed.'
              : 'Your feed is built from these. Shows appear after the next refresh.'}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <PicksEditor />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Mail className="h-4 w-4 text-primary" /> Email notifications
          </CardTitle>
          <CardDescription>
            {me.email
              ? `Delivered to ${me.email}.`
              : 'Add and confirm an email address above to enable notifications.'}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-6">
          <PrefRow
            id="digest"
            title="Weekly digest"
            description="One email a week with new shows in your area, artists you chose first."
            checked={!!me.digest_opt_in}
            disabled={!me.email || saving}
            onChange={(v) => update({ digest_opt_in: v })}
          />
          <PrefRow
            id="instant"
            title="Instant notifications"
            description="Email as soon as a new show appears for an artist you subscribed to."
            checked={!!me.instant_notify_opt_in}
            disabled={!me.email || saving}
            onChange={(v) => update({ instant_notify_opt_in: v })}
          />
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div>
            {/* A route, not a document: <a href> made this a full page load,
                tearing down the app and refetching the bundle to reach a page
                the router already has. */}
            <Button variant="outline" asChild>
              <Link to="/subscribe">Manage subscribed artists</Link>
            </Button>
          </div>
        </CardContent>
      </Card>

      {spotify && <DisconnectSpotify />}

      <DangerZone displayName={me.display_name} />
    </div>
  );
}

// Disconnecting Spotify without deleting the account.
//
// Sits above the danger zone rather than inside it: this is the recoverable
// option, and burying it next to permanent deletion would push people toward
// the destructive one. App Store Guideline 5.1.1(v) wants a way to revoke the
// connection from inside the app, and until this existed the only one was
// deleting the whole account (plan §10.1.2).
//
// One confirmation step, no typed name. The name check on deletion exists
// because deletion is irreversible; making the *safer* action equally
// laborious would teach people the confirmation means nothing.
function DisconnectSpotify() {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  async function disconnect() {
    setBusy(true);
    setErr('');
    try {
      const r = await mutatingFetch('/api/me/spotify-connection', { method: 'DELETE' });
      if (!r.ok) {
        setErr(statusMessage(r.status));
        return;
      }
      // Every session is gone server-side and the cookie is cleared, so a
      // client-side route change would just render a signed-in shell with no
      // credentials. Full navigation rehydrates as anonymous.
      window.location.href = '/login';
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Unplug className="h-4 w-4" /> Disconnect Spotify
        </CardTitle>
        <CardDescription>
          Deletes the Spotify credential we hold for you and the listening
          profile built from it, and signs you out everywhere. Your saved
          concerts, subscribed artists and notification settings are kept —
          signing in again restores them.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        {!confirming ? (
          <div>
            <Button variant="outline" onClick={() => setConfirming(true)}>
              Disconnect Spotify
            </Button>
          </div>
        ) : (
          <>
            <p className="text-sm text-muted-foreground">
              This stops ConcertFinder from using your Spotify account. It does
              not remove ConcertFinder from Spotify's own list of connected
              apps — to do that, visit{' '}
              <a
                className="underline"
                href="https://www.spotify.com/account/apps"
                target="_blank"
                rel="noreferrer noopener"
              >
                your Spotify apps page
              </a>
              .
            </p>
            {err && <p className="text-sm text-destructive">{err}</p>}
            <div className="flex gap-2">
              <Button onClick={disconnect} disabled={busy}>
                {busy ? 'Disconnecting…' : 'Yes, disconnect'}
              </Button>
              <Button
                variant="ghost"
                onClick={() => {
                  setConfirming(false);
                  setErr('');
                }}
              >
                Cancel
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function DangerZone({ displayName }: { displayName: string }) {
  const [confirming, setConfirming] = useState(false);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  async function deleteAccount() {
    setBusy(true);
    setErr('');
    try {
      const r = await mutatingFetch('/api/me/account', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ confirm_name: typed }),
      });
      if (!r.ok) {
        setErr(
          statusMessage(r.status, {
            400: "That name didn't match your display name. Check it and try again.",
          }),
        );
        return;
      }
      // Session cookie is cleared by the server; force a fresh navigation
      // so the app rehydrates as anon.
      window.location.href = '/login';
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="border-destructive/40">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-destructive">
          <AlertTriangle className="h-4 w-4" /> Delete account
        </CardTitle>
        <CardDescription>
          Permanently removes your user record, saved concerts, subscribed
          artists, snapshots, and notification history. Cannot be undone.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        {!displayName ? (
          // The server confirms deletion against the display name and refuses
          // an empty one, so without a name there is nothing to type.
          <p className="text-sm text-muted-foreground">Set a display name above first. You'll type it to confirm.</p>
        ) : !confirming ? (
          <div>
            <Button variant="destructive" onClick={() => setConfirming(true)}>
              Delete my account
            </Button>
          </div>
        ) : (
          <>
            <Label htmlFor="confirm-name">
              Type your display name (<code className="font-mono">{displayName}</code>) to confirm:
            </Label>
            <Input
              id="confirm-name"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={displayName}
              autoFocus
            />
            {err && <p className="text-sm text-destructive">{err}</p>}
            <div className="flex gap-2">
              <Button
                variant="destructive"
                onClick={deleteAccount}
                disabled={busy || typed.trim().toLowerCase() !== displayName.trim().toLowerCase()}
              >
                {busy ? 'Deleting…' : 'Yes, delete permanently'}
              </Button>
              <Button
                variant="ghost"
                onClick={() => {
                  setConfirming(false);
                  setTyped('');
                  setErr('');
                }}
              >
                Cancel
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function PrefRow({
  id,
  title,
  description,
  checked,
  disabled,
  onChange,
}: {
  id: string;
  title: string;
  description: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div>
        <Label htmlFor={id} className="text-base">
          {title}
        </Label>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onChange} disabled={disabled} />
    </div>
  );
}

function DisplayNameRow({ me, setMe }: { me: Me; setMe: (m: Me) => void }) {
  const [editing, setEditing] = useState(!me.display_name);
  const [name, setName] = useState(me.display_name);
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
      setEditing(false);
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:justify-between">
      <span className="text-muted-foreground">Display name</span>
      {editing ? (
        <form onSubmit={save} className="grid gap-2 sm:w-64">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-label="Display name"
            autoComplete="name"
            maxLength={64}
          />
          {err && <p className="text-sm text-destructive">{err}</p>}
          <Button type="submit" variant="outline" disabled={busy || !name.trim()}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </form>
      ) : (
        <div className="flex flex-col items-start gap-1 sm:items-end">
          <span>{me.display_name}</span>
          <Button variant="link" className="h-auto p-0" onClick={() => setEditing(true)}>
            Change
          </Button>
        </div>
      )}
    </div>
  );
}
