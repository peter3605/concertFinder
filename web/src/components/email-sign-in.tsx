import { useState, type FormEvent } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { apiFetch, mutatingFetch, NETWORK_ERROR_MESSAGE, statusMessage } from '@/lib/api';

// Email sign-in (CF-33). Two steps: ask for a link, then either open the
// link from the mail or type its 6-digit code here.
//
// The start request always answers 202, whether or not the address has an
// account, so the message after it has to be true in every case — it cannot
// say "we sent you a link", only that one is on its way if it can be.
export function EmailSignIn({ invite }: { invite: string }) {
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  async function start(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr('');
    try {
      const r = await apiFetch(
        '/api/auth/email/start',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ email: email.trim(), invite: invite.trim() }),
        },
        { publicEndpoint: true },
      );
      if (r.status !== 202) {
        setErr(
          statusMessage(r.status, {
            400: 'Enter a valid email address.',
            403: 'That invite code is not valid. It may have been used up or expired.',
          }),
        );
        return;
      }
      setSent(true);
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  async function submitCode(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr('');
    try {
      const r = await apiFetch(
        '/api/auth/email/code',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ email: email.trim(), code: code.trim() }),
        },
        { publicEndpoint: true },
      );
      if (!r.ok) {
        setErr(
          statusMessage(r.status, {
            400: "That code didn't work. Codes expire after 15 minutes and work once.",
            403: 'Creating an account needs a valid invite code. Start again with one.',
          }),
        );
        return;
      }
      // Full navigation so the app rehydrates with the new session cookie.
      window.location.href = '/';
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  if (!sent) {
    return (
      <form onSubmit={start} className="w-full space-y-2">
        <Label htmlFor="signin-email">Or sign in with email</Label>
        <Input
          id="signin-email"
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
          required
        />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <Button type="submit" variant="outline" className="w-full" disabled={busy || !email.trim()}>
          {busy ? 'Sending…' : 'Email me a sign-in link'}
        </Button>
      </form>
    );
  }

  return (
    <form onSubmit={submitCode} className="w-full space-y-2">
      <p className="text-sm text-muted-foreground">
        If {email.trim()} has an account, or you entered a valid invite code, a
        link and a 6-digit code are on their way. Open the link, or enter the
        code here.
      </p>
      <Label htmlFor="signin-code">Code</Label>
      <Input
        id="signin-code"
        inputMode="numeric"
        autoComplete="one-time-code"
        maxLength={6}
        value={code}
        onChange={(e) => setCode(e.target.value)}
        placeholder="123456"
      />
      {err && <p className="text-sm text-destructive">{err}</p>}
      <Button type="submit" className="w-full" disabled={busy || code.trim().length !== 6}>
        {busy ? 'Signing in…' : 'Sign in'}
      </Button>
      <Button
        type="button"
        variant="ghost"
        className="w-full"
        onClick={() => {
          setSent(false);
          setCode('');
          setErr('');
        }}
      >
        Use a different address
      </Button>
    </form>
  );
}

// Settings: add or change the address on a signed-in account. Nothing
// changes until the link or code is used; then it becomes the account's
// email and a way to sign in.
export function EmailAddressForm({ current, onVerified }: { current: string; onVerified: (email: string) => void }) {
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [sentTo, setSentTo] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [open, setOpen] = useState(!current);

  async function send(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr('');
    try {
      const r = await mutatingFetch('/api/me/email', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email: email.trim() }),
      });
      if (r.status !== 202) {
        setErr(
          statusMessage(r.status, {
            400: 'Enter a valid email address.',
            429: 'Too many confirmation emails to that address. Try again in an hour.',
          }),
        );
        return;
      }
      setSentTo(email.trim());
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  async function verify(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr('');
    try {
      const r = await mutatingFetch('/api/me/email/verify', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email: sentTo, code: code.trim() }),
      });
      if (!r.ok) {
        setErr(
          statusMessage(r.status, {
            400: "That code didn't work. Codes expire after 15 minutes and work once.",
            409: 'That address already signs in to a different ConcertFinder account.',
          }),
        );
        return;
      }
      onVerified(sentTo.toLowerCase());
      setOpen(false);
      setSentTo('');
      setCode('');
      setEmail('');
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <Button variant="link" className="h-auto p-0" onClick={() => setOpen(true)}>
        Change
      </Button>
    );
  }

  if (!sentTo) {
    return (
      <form onSubmit={send} className="grid gap-2">
        <Input
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
          aria-label="Email address"
          required
        />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <Button type="submit" variant="outline" disabled={busy || !email.trim()}>
          {busy ? 'Sending…' : 'Send confirmation'}
        </Button>
      </form>
    );
  }

  return (
    <form onSubmit={verify} className="grid gap-2">
      <p className="text-sm text-muted-foreground">
        We sent a link and a 6-digit code to {sentTo}. Open the link, or enter
        the code here.
      </p>
      <Input
        inputMode="numeric"
        autoComplete="one-time-code"
        maxLength={6}
        value={code}
        onChange={(e) => setCode(e.target.value)}
        placeholder="123456"
        aria-label="Confirmation code"
      />
      {err && <p className="text-sm text-destructive">{err}</p>}
      <Button type="submit" disabled={busy || code.trim().length !== 6}>
        {busy ? 'Confirming…' : 'Confirm'}
      </Button>
    </form>
  );
}
