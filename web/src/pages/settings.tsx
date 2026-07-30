import { useState } from 'react';
import { AlertTriangle, Mail } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Label } from '@/components/ui/label';
import { mutatingFetch } from '@/lib/api';
import { useAuth } from '@/lib/auth';
import type { Me } from '@/lib/types';

export default function SettingsPage() {
  const { auth, setMe } = useAuth();
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  if (auth.kind !== 'signed_in') {
    return <p className="text-sm text-muted-foreground">Log in to change settings.</p>;
  }
  const me = auth.me;

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
        setErr(await r.text());
        return;
      }
      setMe({ ...me, ...patch });
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
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
          <CardDescription>Your Spotify identity + email.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3 text-sm">
          <div className="flex justify-between">
            <span className="text-muted-foreground">Display name</span>
            <span>{me.display_name || me.spotify_user_id}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Spotify ID</span>
            <span className="font-mono text-xs">{me.spotify_user_id}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Email</span>
            <span>
              {me.email || (
                <a href="/api/auth/login" className="text-primary hover:underline">
                  Log in again to grant email access
                </a>
              )}
            </span>
          </div>
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
              : 'Log in again with the email scope to enable notifications.'}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-6">
          <PrefRow
            id="digest"
            title="Daily digest"
            description="One email a day summarizing upcoming shows in your area."
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
            <Button variant="outline" asChild>
              <a href="/subscribe">Manage subscribed artists</a>
            </Button>
          </div>
        </CardContent>
      </Card>

      <DangerZone displayName={me.display_name || me.spotify_user_id} />
    </div>
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
        setErr(await r.text());
        return;
      }
      // Session cookie is cleared by the server; force a fresh navigation
      // so the app rehydrates as anon.
      window.location.href = '/login';
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
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
        {!confirming ? (
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
