# Admission policy

What "launch" means for ConcertFinder, why it is gated, and how to work the
gate. Settles CF-13.

**The policy in one line:** ConcertFinder launches invite-only. Anyone can be
admitted, one invite code at a time, at a rate the operator controls.

---

## 1. Why there is a gate at all

The constraint is Ticketmaster, not Spotify, and it is arithmetic rather than
judgement:

| Quantity | Value | Where |
|---|---|---|
| Ticketmaster account ceiling | 5000 calls/day **per API key** | `RATE_CAP_TM_ACCOUNT_DAILY`, migration 0017 |
| Artists submitted per scan | 200 | `spotify.MaxScoredArtists` |
| Calls per artist on a cold scan | 2 (resolve attraction, then events) | `ticketmaster.CallsPerArtistColdScan` |
| Permits reserved per scan, up front | **400** | `jobs.scanQuotaFor` = `artists * 2` |
| **Cold scans available per day** | **~12** | 5000 ÷ 400 |

The nightly fanout (`FanoutScanConcerts`) enqueues one scan per user with a
session in the last 14 days. So **roughly twelve active users in distinct
cities is where the nightly fanout alone exhausts the account ceiling**, before
anybody has opened the app.

Two things make that number a floor rather than a wall, and neither removes the
need for a gate:

- `concert_cache` (12h, `DefaultCacheTTL`) makes the second user in an
  already-scanned city far cheaper than the first. Users concentrated in one
  metro cost much less than twelve users in twelve cities.
- Reservations are refunded. `scanQuotaFor` is an upper bound, and a warm scan
  hands most of it back.

What makes exceeding it survivable is `rate_ledger_account`: since migration
0017, running out produces a dated `retry_after` in both clients instead of
upstream 403s that arrive looking exactly like artists with no shows. That is
the difference between a visible limit and a silently half-empty feed — it is
**not** a reason to skip staging admission. A user told to come back tomorrow
is still a user whose feed does not work today.

### The gate that exists today, and why it is not enough

Right now nobody can sign in unless their Spotify account is on the
Development Mode allowlist, maintained by hand in Spotify's dashboard
(`internal/auth/handlers.go` returns a 403 with its own copy for this). That is
a real gate. It has two problems:

1. It is not ours, and it **disappears the moment Extended Quota Mode is
   granted** (CF-09). That grant is the event this policy exists to survive:
   the deployment would go from "25 accounts typed in by hand" to "anyone with
   a Spotify account" with nothing in this repository noticing.
2. It blocks App Review's own reviewer, which is why CF-17 exists.

CF-09 is a multi-week external application with no guaranteed approval. The
gate had to be built before it lands, not after.

---

## 2. The policy

- **Signups require an invite code. Logins never do.** The gate fires only when
  a callback would *create* a user, so returning users and every account
  predating migration 0021 are unaffected. Nobody is ever locked out of an
  account they already have.
- **Codes are minted by the operator**, one at a time or in small batches, with
  a note recording who each was for.
- **Admission rate is a judgement call, not a configured number.** There is
  deliberately no "N per day" automation: at this scale the operator issuing
  codes slowly *is* the rate limiter, and a number in a config file would be a
  second thing to keep true.
- **A waitlist is explicitly out of scope for now.** It was considered and
  rejected for launch, for a reason that is worth recording: the "you're in"
  email cannot be delivered. `infra/ses.tf` verifies exactly one recipient and
  SES is still in sandbox (CF-08), so every email to anybody but the operator
  is rejected. A waitlist that cannot tell people they have been admitted is
  worse than no waitlist — it collects addresses and then goes quiet. Revisit
  once CF-08 lands and demand actually exceeds hand-issued codes.

### When to open up

Turn the gate off (`INVITE_REQUIRED=false`) when **all** of:

1. Extended Quota Mode is granted (CF-09), so Spotify is no longer capping
   accounts at 25 anyway; **and**
2. the Ticketmaster account allowance has been raised, or measured usage shows
   real headroom against the ~12 cold scans/day above; **and**
3. SES is out of sandbox (CF-08), so admitted users actually receive the
   digests they opt into.

Until then, opening up produces a worse product for everyone already using it,
not a bigger one.

---

## 3. How to work the gate

The api image is distroless — no shell — so invite administration is a mode of
the server binary, the same shape as `-healthcheck`:

```bash
# Mint a single-use code. The code is printed on stdout, alone on its line.
docker compose exec api /server -mint-invite -note "alex from work"

# A code that seats three people and expires in a fortnight.
docker compose exec api /server -mint-invite -note "book club" -uses 3 -expires-days 14

# What has been handed out, and what became of it.
docker compose exec api /server -list-invites

# Revoke one. It stays readable, so a user admitted by it keeps their provenance.
docker compose exec api /server -disable-invite CF-ABCD-EFGH
```

Codes look like `CF-ABCD-EFGH`. The alphabet omits `I`, `L`, `O`, `U`, `0` and
`1`, because these get read down a phone and typed off screenshots. What the
user types is normalized (case, spaces, underscores) by
`db.NormalizeInviteCode`, so `cf abcd efgh` works.

An invite can be sent as a single link — the web login page pre-fills from
`?invite=`:

```
https://concertfinder.app/login?invite=CF-ABCD-EFGH
```

---

## 4. Things that will bite

- **`INVITE_REQUIRED` defaults to `true`, including when unset.** This is the
  opposite of every other flag in `.env.example`, and deliberate: both
  directions of a wrong default are silent, and this is the recoverable one. A
  gate wrongly on is a message to one person who can be sent a code. A gate
  wrongly off quietly spends the shared allowance and presents to everyone as a
  thinner feed. An unparseable value keeps the default rather than reading as
  false. **The server logs which mode it is in at startup**, in both
  directions, because the operator already has an account and is therefore the
  last person who would notice either failure.
- **The `/login` pre-check is advisory; `/callback` is the authority.** The
  pre-check is read-only so an abandoned login cannot burn somebody's invite —
  and most logins are abandoned at Spotify's consent screen, which sits between
  the two. The cost is a race: two people can both pass the pre-check on a
  one-use code and only one can pass the redemption. That resolves at the
  redemption, which is atomic (`redeemInviteTx` guards in the `WHERE` clause,
  not in a read-then-write).
- **Redemption and user creation are one transaction.** Split apart, both
  orderings fail silently: redeem-then-insert burns a code when the insert
  fails; insert-then-redeem admits a user for free when the redemption fails.
  Do not split them.
- **`users.invited_with` is provenance, never permission.** Nothing reads it to
  decide what a user may do, and its FK is `ON DELETE SET NULL` so retiring a
  spent code can never delete or block a user.
- **Codes are stored in the clear**, unlike session tokens (migration 0018).
  The reasoning is written out in migration 0021: an invite code does not
  authenticate as anybody — redeeming one still requires a full Spotify OAuth
  grant — so a code read out of a backup is worth one signup slot, not one
  account. Against that, the operator has to be able to answer "which code did
  I give to whom" and re-send one lost in a spam folder.
- **`invite_required` in `/api/site-info` is a rendering hint.** It tells a
  client whether to draw a text box. A client that gets it wrong shows or hides
  a field; it can never admit anybody, because the gate is in the callback.

---

## 5. What this does not do

- No waitlist, no queue, no automatic admission — see §2 for why.
- No per-day admission cap in code. The operator's minting rate is the cap.
- Nothing revokes an already-admitted user. `DELETE /api/me/account` and
  `DELETE /api/me/spotify-connection` are the existing exits and are unchanged.
- It does not bound how much an *admitted* user can spend. That is what
  `RATE_CAP_TM_PER_USER_DAILY` and `user_location_visits` (migration 0020) are
  for, and they were already there.
