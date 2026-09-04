import { useCallback, useEffect, useState } from 'react';
import { Check, Copy, Loader2, Ticket } from 'lucide-react';
import { ActionError } from '@/components/action-error';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { apiFetch, mutatingFetch, NETWORK_ERROR_MESSAGE, statusMessage } from '@/lib/api';
import { useDocumentTitle } from '@/lib/use-document-title';

// The operator's console: mint invite codes, see what state each one is in,
// revoke one.
//
// Nothing tells this page whether its user is an admin, and that is the
// design. There is no is_admin field in /api/auth/me or anywhere else, because
// the same payloads are decoded by iOS builds already on people's phones and
// every field in them is additive-only forever — an admin surface has no
// business taking out that kind of mortgage. Instead the page asks the server
// the only question that matters, by making the request it wants to make: 200
// renders the console, 403 renders "not yours". One round trip either way, and
// the client cannot disagree with the server about who is an admin.
//
// The consequence is that there is no nav link. You reach this by typing
// /admin, which for a console with one user is the right trade.

type InviteState = 'usable' | 'spent' | 'expired' | 'disabled';

type Invite = {
  code: string;
  note: string;
  max_redemptions: number;
  redemptions: number;
  // Computed server-side by db.InviteCode.State, never derived here. The
  // predicate for "usable" lives in SQL and is mirrored once in Go; a third
  // copy in TypeScript is how a code this page calls usable gets refused at
  // redemption.
  state: InviteState;
  expires_at: string | null;
  disabled_at: string | null;
  created_at: string;
};

// Access is one of three things, and "forbidden" is a real answer rather than
// an error: an ordinary signed-in user landing here should be told this is not
// theirs, not shown a broken page.
type Access = 'loading' | 'admin' | 'forbidden' | 'error';

const STATE_VARIANT: Record<InviteState, 'default' | 'secondary' | 'muted' | 'outline'> = {
  usable: 'default',
  spent: 'muted',
  expired: 'muted',
  disabled: 'outline',
};

function formatDay(iso: string | null): string {
  if (!iso) return 'never';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return 'unknown';
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

export default function AdminPage() {
  useDocumentTitle('Invite codes');
  const [access, setAccess] = useState<Access>('loading');
  const [invites, setInvites] = useState<Invite[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState('');
  const [uses, setUses] = useState('1');
  const [expiresDays, setExpiresDays] = useState('0');
  const [minting, setMinting] = useState(false);
  // The code from the most recent mint, held so it can be copied. It is also
  // in the table below, but a freshly minted code is the one thing on this
  // page somebody needs to get out of the browser and into a message.
  const [justMinted, setJustMinted] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await apiFetch('/api/admin/invites');
      if (r.status === 403) {
        setAccess('forbidden');
        return;
      }
      if (!r.ok) {
        setAccess('error');
        setErr(statusMessage(r.status));
        return;
      }
      const body = (await r.json()) as { invites: Invite[] | null };
      setInvites(body.invites ?? []);
      setAccess('admin');
    } catch {
      setAccess('error');
      setErr(NETWORK_ERROR_MESSAGE);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function mint(e: React.FormEvent) {
    e.preventDefault();
    setMinting(true);
    setErr(null);
    try {
      const r = await mutatingFetch('/api/admin/invites', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          note: note.trim(),
          uses: Number(uses) || 1,
          expires_days: Number(expiresDays) || 0,
        }),
      });
      if (!r.ok) {
        // Never the response body — those are Go http.Error strings written
        // for a log, and on a bad gateway they are the proxy's HTML.
        setErr(
          statusMessage(r.status, {
            400: 'Check the number of uses (1–50) and the expiry (0–400 days).',
            403: 'That mint was refused. Reload and try again.',
          }),
        );
        return;
      }
      const created = (await r.json()) as Invite;
      setJustMinted(created.code);
      setCopied(false);
      setNote('');
      setInvites((prev) => [created, ...prev]);
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    } finally {
      setMinting(false);
    }
  }

  async function disable(code: string) {
    setErr(null);
    try {
      const r = await mutatingFetch(`/api/admin/invites/${encodeURIComponent(code)}/disable`, {
        method: 'POST',
      });
      if (!r.ok) {
        setErr(statusMessage(r.status, { 404: 'That code no longer exists.' }));
        return;
      }
      // Refetch rather than patching state locally: disabling is the one
      // action here whose result the server computes (disabled_at, and the
      // state that follows from it), and guessing it client-side is how the
      // console starts disagreeing with the database.
      await load();
    } catch {
      setErr(NETWORK_ERROR_MESSAGE);
    }
  }

  async function copy(code: string) {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
    } catch {
      // Clipboard access is denied often enough (insecure context, permission
      // policy) that failing loudly would be noise. The code is on screen and
      // selectable either way.
      setCopied(false);
    }
  }

  if (access === 'loading') {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Checking access…
      </div>
    );
  }

  if (access === 'forbidden') {
    return (
      <div className="mx-auto max-w-2xl">
        <h1 className="text-2xl font-semibold">Invite codes</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          You&rsquo;re signed in, but this page is for administrators. Nothing is wrong with your
          account.
        </p>
      </div>
    );
  }

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">Invite codes</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Signups need one of these. Logins don&rsquo;t &mdash; anyone who already has an account
          keeps their access.
        </p>
      </div>

      <ActionError message={err} onDismiss={() => setErr(null)} />

      <Card>
        <CardHeader>
          <CardTitle>Mint a code</CardTitle>
          <CardDescription>
            One use and no expiry unless you say otherwise. The note is for you &mdash; whoever
            redeems the code never sees it.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={mint} className="grid gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="invite-note">Who it&rsquo;s for</Label>
              <Input
                id="invite-note"
                value={note}
                maxLength={200}
                placeholder="alex from work"
                onChange={(e) => setNote(e.target.value)}
              />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="invite-uses">Uses</Label>
                <Input
                  id="invite-uses"
                  type="number"
                  min={1}
                  max={50}
                  value={uses}
                  onChange={(e) => setUses(e.target.value)}
                />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="invite-expiry">Expires in (days)</Label>
                <Input
                  id="invite-expiry"
                  type="number"
                  min={0}
                  max={400}
                  value={expiresDays}
                  onChange={(e) => setExpiresDays(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">0 means it never expires.</p>
              </div>
            </div>
            <div>
              <Button type="submit" disabled={minting}>
                {minting ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
                Mint code
              </Button>
            </div>
          </form>

          {justMinted ? (
            <div className="mt-4 flex items-center gap-3 rounded-md border bg-muted/40 p-3">
              <Ticket className="h-4 w-4 shrink-0 text-muted-foreground" />
              <code className="flex-1 font-mono text-sm tracking-wide">{justMinted}</code>
              <Button variant="secondary" size="sm" onClick={() => void copy(justMinted)}>
                {copied ? <Check className="mr-1 h-3.5 w-3.5" /> : <Copy className="mr-1 h-3.5 w-3.5" />}
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>All codes</CardTitle>
          <CardDescription>
            Revoking keeps the code readable, so you can still see who it let in.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {invites.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No codes yet. Mint one above &mdash; until then nobody new can sign up.
            </p>
          ) : (
            // Scrolls inside itself rather than pushing the page sideways on a
            // narrow screen.
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs uppercase tracking-wide text-muted-foreground">
                    <th className="py-2 pr-4 font-medium">Code</th>
                    <th className="py-2 pr-4 font-medium">Used</th>
                    <th className="py-2 pr-4 font-medium">State</th>
                    <th className="py-2 pr-4 font-medium">Expires</th>
                    <th className="py-2 pr-4 font-medium">Note</th>
                    <th className="py-2" />
                  </tr>
                </thead>
                <tbody>
                  {invites.map((c) => (
                    <tr key={c.code} className="border-b last:border-0">
                      <td className="py-2 pr-4 font-mono text-xs whitespace-nowrap">{c.code}</td>
                      <td className="py-2 pr-4 whitespace-nowrap tabular-nums">
                        {c.redemptions}/{c.max_redemptions}
                      </td>
                      <td className="py-2 pr-4">
                        <Badge variant={STATE_VARIANT[c.state]}>{c.state}</Badge>
                      </td>
                      <td className="py-2 pr-4 whitespace-nowrap text-muted-foreground">
                        {formatDay(c.expires_at)}
                      </td>
                      <td className="py-2 pr-4 text-muted-foreground">{c.note}</td>
                      <td className="py-2 text-right">
                        {c.state === 'disabled' ? null : (
                          <Button variant="ghost" size="sm" onClick={() => void disable(c.code)}>
                            Revoke
                          </Button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
