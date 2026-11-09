# The public manual-picks path

**Status:** design, CF-26. Nothing here is built yet; the build is CF-27 to
CF-39 in [§9](#9-build-order). Written 2026-11-08.

**The decision this designs for.** ConcertFinder stays Spotify-based for the
operator and up to four friends. Everyone else signs in with Apple or an email
link and picks artists or genres by hand, and those picks go straight into
concert matching with no Spotify taste analysis.

**Why there is a second path at all.** Spotify's quota page caps a Development
Mode app at "up to 5 authenticated Spotify users", and says that "as of May
15th 2025, Spotify only accepts applications from organizations (not
individuals)", alongside a requirement of "at least 250k MAUs" for Extended
Quota. An individual's app with a handful of users cannot qualify, so CF-09 is
re-scoped rather than waited on.

**The short version of what the evidence says:**

| Question | Answer |
|---|---|
| Can people find their artists? | Yes. All 38 test artists are in Ticketmaster, including small club acts. 33 match on the first page; the other 5 need the ranking in §1. There is no typo tolerance. |
| Do genre-near-a-city queries work? | Top-level genres yes, sub-genres no. Dense markets need a cap because New York jazz alone is 1,590 listings. |
| How many manual users fit? | About 60 to 120 active users at 20 artists each on documented per-artist calls, alongside the five Spotify users. Batched IDs raise that past 1,000, but Ticketmaster does not document them. |
| What is the worst thing found? | Shared `concerts` rows carry Spotify artist IDs and genres, so a manual user would be served Spotify-derived data. CF-28 must land first. |
| New monthly cost? | SES only: about $0.37 a month at 120 users. |

---

## 1. Ticketmaster coverage

Tested with real Discovery API calls on the existing key on 2026-11-08: 98
calls in total, against the 5,000/day limit that the response headers confirm.

### 1.1 Finding an artist

The 38 artists span arena acts to small club acts, plus names chosen to be
awkward. Each query used exactly what `ResolveAttraction` sends today
(`keyword`, `classificationName=Music`, `size=10`).

| Tier | Artists tested | Exact match on first page |
|---|---|---|
| Arena (Taylor Swift, Metallica, Bad Bunny…) | 5 | 5 |
| Theatre (Phoebe Bridgers, Khruangbin, Turnstile…) | 8 | 8 |
| Club (Geese, MJ Lenderman, Horsegirl, Cola…) | 8 | 8 |
| Latin, DJ, jazz (Peso Pluma, Four Tet, Samara Joy…) | 6 | 6 |
| Awkward names (see below) | 11 | 6 |
| **Total** | **38** | **33** |

The five that missed all exist, under conditions a picker can handle:

| Query | What came back | Fix |
|---|---|---|
| `Beyonce` | `Beyoncé` as result 2, no exact match | Compare with diacritics folded |
| `Sunn O)))` | `Sunn O )))` as the only result | Compare under `concerts.Normalize` |
| `!!!` | `!!! (chk chk chk)` as result 2 | The person picks it from the list |
| `Ghost` | 309 results; `GHOST` absent from the first 10, present at `size=200` | Fetch 200 and promote exact matches |
| `Low` | 127 results; `Low` absent from the first 10, present at `size=200` | Same |

Three other behaviours shape the picker:

- **Tribute acts crowd popular names.** The top three results for `Taylor
  Swift` are all tribute acts, and the real entry appears lower. Results need
  exact matches first, then tribute-like names demoted.
- **Duplicates exist.** `Disclosure` has two exact entries, one with 2 upcoming
  events and genre Dance/Electronic and one with 0 and genre Rock.
  `ResolveAttraction` takes the first exact match blindly; a picker shows
  genre and upcoming count and lets the person choose.
- **Most artists have nothing on sale at any moment.** Of the 33 exact
  matches, 19 had zero upcoming events. A pick must be allowed anyway and kept,
  so that a tour announced later is found.

Prefix and typo behaviour, which decides the interaction:

| Query | `attractions.json` | `suggest` |
|---|---|---|
| `phoe` (prefix) | 0 results | Phoebe Bridgers first |
| `khruang` (prefix) | 0 results | Khruangbin |
| `Kruangbin` (typo) | 0 results | 0 results |
| `Phoebe Bridges` (typo) | 0 results | not tested |
| `Ghost` | 309, exact not in first 10 | exact not in first 5 |

`suggest` rejected `size=8` with a 400 and accepted 5, so it cannot rescue the
common-word names.

**Decision.** Search on submit, not on keystroke, using `attractions.json` at
`size=200`, one call per submitted query. Rank server-side: folded exact
matches first, then by upcoming events, tribute-like names last. Return ten
with name, image, genre and upcoming count. Cache a query for 24 hours in
process. A typo returns nothing, and the screen says to check the spelling;
Spotify search is not available as a fuzzy fallback for these users (§7).

### 1.2 Genres near a city

The Music segment has 24 genres, from Rock (127 sub-genres) down to several
with one. Queries used the same parameters as `eventsQuery`, radius 50 miles,
plus `genreId`. Each cell is total events, with pages at 100 per page in
parentheses.

| Genre | New York | Austin | Denver | Boise |
|---|---|---|---|---|
| All music | 3,654 (37) | 314 (4) | 716 (8) | 100 (1) |
| Rock | 596 (6) | 73 (1) | 143 (2) | 29 (1) |
| Alternative | 87 (1) | 13 (1) | 27 (1) | 13 (1) |
| Hip-Hop/Rap | 76 (1) | 12 (1) | 20 (1) | 3 (1) |
| Country | 74 (1) | 31 (1) | 47 (1) | 12 (1) |
| Dance/Electronic | 66 (1) | 13 (1) | 87 (1) | 9 (1) |
| Jazz | 1,590 (16) | 2 (1) | 5 (1) | 1 (1) |

Results were real shows: filtered samples were 96 to 100% the requested genre
in most cells, and tribute acts were rare. What needs handling:

- **Sub-genres are too sparse to offer.** In New York: Indie Rock 2 events,
  American Punk 0, Bebop 0, Acid House 0. Only Alternative Rock (149) was
  useful. Offer top-level genres only.
- **Genre labels are loose.** Khruangbin is filed under Rock, Japanese
  Breakfast under Pop, Bad Bunny under Hip-Hop/Rap, Zach Bryan under Rock and
  Grupo Frontera under World. A genre pick is a broad net, and the UI should
  present it as one.
- **New York jazz is residencies.** Its first 100 rows held 29 distinct acts.
  It stays at 1,028 events (11 pages) even inside a 60-day window, so a date
  window alone does not bound it.
- **Some attractions are not acts.** Denver returned `Club Level Seating: Ball
  Arena` as an attraction. These need excluding.
- **Many events have no attraction.** 30 of 74 New York country events and 22
  of 47 Denver country events carried none, so the feed has to fall back to
  the event name for those.

**Decision.** Fetch date ascending, stop at 3 pages, and record the date of the
last row as the listing's horizon. The feed says "next 300 shows" rather than
implying it holds everything.

This is a deliberate, written exception to the rule that a truncated fetch is
not cached. A date-sorted prefix is correct up to its horizon, while the rows
that rule protects against are arbitrary fragments.

### 1.3 A defect this measurement found

The query above is exactly what `SeedDiscoverWorker` sends at radius 50 for
New York: 37 pages. `pagedEvents` stops at `MaxEventPages` (10) and reports
`complete=false`, and the worker does not cache an incomplete fetch. So New
York, the coordinate the web discover page is hardcoded to, appears to spend up
to 10 calls a day and write nothing. Filed as CF-B23. It was not confirmed
against the production table.

---

## 2. Call budget

### 2.1 What each user costs

A manual pick already holds its attraction ID from the picker, so the two-stage
resolution that makes a Spotify cold scan cost 2 calls per artist disappears.

| User | Cost per scan | Per day |
|---|---|---|
| Manual, N artists | N (one events call each) | N for the nightly scan, up to 2N if a read arrives after the 12h cache expires |
| Manual, N artists, batched IDs | 1 call per 30 artists | 1 to 2 for N ≤ 30 |
| Genre, G genres, unique location | at most 3 per genre | 3G to 6G at most, usually G |
| Genre, shared location | shared by everyone in the cell | close to 0 per extra user |
| Picker, once at signup | about 2.5 searches per pick | about 50 for 20 picks, one time only |
| Spotify user | 200 warm, 400 cold | up to 400 |

Cache behaviour is what limits the "up to 2N" case. `DefaultCacheTTL` is 12h
against a 6h staleness window, so a stale read is normally cache-served.

`concert_cache` keys include coordinates to four decimals, so **two users share
a row only if their coordinates are identical**. A city typed into the location
box geocodes to one point, but a device fix does not. Genre listings therefore
snap to a 0.25° grid and fetch with a 15-mile pad, then filter with
`concerts.Near`, the pattern `/api/discover` already uses. Without snapping, the
sharing in the table above does not happen.

### 2.2 How many fit in 5,000

| Fixed daily spend | Calls |
|---|---|
| Five Spotify users, worst-case day (5 × 400) | 2,000 |
| Discover seed (8 cities × up to 10 pages) | 80 |
| Onboarding allowance (10 signups × 50) | 500 |
| **Left for manual users** | **2,420** |

The 2,000 is deliberately pessimistic. Five warm profiles spend about 1,000.

| Average picks | Documented per-artist calls | Batched IDs (§2.3) |
|---|---|---|
| 10 | 121 to 242 active users | 1,210 to 2,420 |
| 20 | 60 to 121 | 1,210 to 2,420 |
| 30 (the cap) | 40 to 80 | 1,210 to 2,420 |

Three genres in a unique location cost at most 18 calls a day, or about 134
users; in practice many users share a cell. "Active" means a session in the
last 14 days, because that is what `FanoutScanConcerts` selects on.

**Decision.** Cap picks at 30 artists and 5 genres. Plan around **60 to 120
active manual users**, and let the operator's rate of minting invites stay the
limiter (see `docs/admission-policy.md`).

### 2.3 Batched attraction IDs

`events.json` accepted a comma-separated `attractionId`. One call with five
artists across the US returned all five (Metallica 46, Sylvan Esso 20, Zach
Bryan 18, Kamasi Washington 9, Turnstile 1), and a 33-ID call returned without
error. Ticketmaster documents the parameter only as "Filter by attraction id",
so it could stop working, and that failure would look like artists with no
shows.

**Decision.** Ship per-artist calls first, because they are documented and
share `concert_cache` across users. Add batching as a separate change only when
`rate_ledger_account` shows the account above about 60% of its ceiling, and
pair it with a check that the returned attractions match what was requested.

### 2.4 Other limits

| Resource | Why it does not bind first |
|---|---|
| River's 5 workers | A 20-artist scan is about 20 calls at semaphore 10, seconds rather than the minutes a Spotify scan takes, and the fanout spreads over 60 minutes |
| API container (320m cap on the 2 GB box) | No new long-lived state beyond a small key cache and a query cache; genre feeds are capped at 300 rows |
| Shared RDS | Picks are a few rows per user. Genre rows land in tables the janitor already prunes |
| Ticketmaster's 5 requests/sec | Unchanged; each client call already goes through the existing retry path |

---

## 3. Accounts

### 3.1 The change

Today `users.spotify_user_id` is `NOT NULL UNIQUE`, and it is the login
identity. Sessions already reference the opaque `users.id`, so the session
model needs no change at all.

```sql
-- Migration 0023 (CF-27)
CREATE TABLE user_identities (
  provider      TEXT NOT NULL CHECK (provider IN ('spotify', 'apple', 'email')),
  subject       TEXT NOT NULL,   -- Spotify ID, Apple sub, or lowercased address
  user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_login_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, subject),
  UNIQUE (user_id, provider)
);

INSERT INTO user_identities (provider, subject, user_id, created_at)
SELECT 'spotify', spotify_user_id, id, created_at FROM users;

ALTER TABLE users ALTER COLUMN spotify_user_id DROP NOT NULL;
```

`spotify_user_id` stays on `users` and stays unique, because `-grant-admin` and
`UpsertUserWithAdmission` address accounts by it.

A manual user has `spotify_user_id` NULL and an empty refresh token and nonce.
That empty-credential state already exists for a disconnected user, and
`AccessTokenFor` already answers it with `ErrSpotifyDisconnected`, so no new
"has no Spotify" branch is needed in the auth code.

### 3.2 Existing users and sessions

Nothing about them changes. The backfill creates one `spotify` identity per
row, `sessions` is not touched, `token_hash` values keep working, and Spotify
sign-in continues to run through `UpsertUserWithAdmission` until a later change
routes it through the shared admission function.

Things that would break silently:

- **NULL into a Go string is a scan error.** Every query selecting
  `spotify_user_id` needs `COALESCE(…, '')`: `users.go`, `invites.go`,
  `admins.go`, and `sessionUserColumns` in `sessions.go`.
- **iOS decodes `spotifyUserId` as a non-optional `String`** (`Models.swift`).
  The field must stay present, as an empty string, since `/api/auth/me` is
  additive-only. Add `auth_methods` beside it rather than changing it.
- **Display name.** Web falls back to `spotify_user_id` when `display_name` is
  empty. Apple may send no name and email sign-in has none, so onboarding asks
  for one.

### 3.3 One admission function

`db.AdmitIdentity(provider, subject, profile, invite, requireInvite)` finds the
identity; if none exists, it applies the gate and creates the user and identity
in **one transaction**, the rule `docs/admission-policy.md` already sets for
redemption. All three providers go through it.

### 3.4 Linking

Accounts are **never auto-linked by matching email**. Spotify email addresses
are not proof of mailbox ownership, and linking on them would let someone
pre-register a victim's address.

Linking is an explicit step from a signed-in session in Settings. Verifying an
email address (§4.3) adds an `email` identity, which also gives a Spotify user
a way in that does not depend on Spotify.

### 3.5 Spotify users adding picks

Picks live in their own table (§5) and are independent of the Spotify identity.
The scan uses picks first, then fills to 200 from the affinity profile.

`DELETE /me/spotify-connection` leaves picks alone, so a disconnected user keeps
a working picks-only feed. `DELETE /me/account` cascades through
`user_identities` and the picks tables.

---

## 4. Sign-in and email

### 4.1 Sign in with Apple

Setup is in CF-34 and needs the Apple account holder: the capability on the App
ID, a Services ID grouped with it, a dedicated `.p8` key, and the return URL.
The key is kept separate from the APNs key in CF-14 so that rotating one cannot
break the other.

| | Web | iOS |
|---|---|---|
| Entry | Redirect to `appleid.apple.com/auth/authorize` | Native `ASAuthorizationAppleIDProvider` |
| Return | POST to `/api/auth/apple/callback` | Identity token to `POST /api/auth/apple/native` |
| `aud` | Services ID | Bundle ID |
| Session | Cookie | Bearer token, through the same one-time-code exchange |

Traps:

- **The handshake cookie will not arrive.** When name or email is requested,
  Apple posts back cross-site, and the handshake cookie is `SameSite=Lax`
  (`internal/auth/session.go`), which browsers withhold on cross-site POST. The
  Apple path needs its own `SameSite=None; Secure` cookie, or every web sign-in
  fails with "missing handshake cookie".
- **Apple sends the name once.** Its docs say it shares this "the first time
  the user logs in", and to "immediately store it". The email does come back in
  later identity tokens.
- **Both audiences must be accepted**, each only on its own route.
- **Deletion revokes.** `DELETE /me/account` calls `POST
  https://appleid.apple.com/auth/revoke`, which needs a stored refresh token,
  held encrypted the same way as Spotify's and retried from a small queue table
  so a failed call cannot lose it.

Verification checks signature, `iss`, `aud`, `exp` and `nonce`, using Apple's
published keys. `internal/push` already signs ES256 JWTs with the standard
library, so no JWT dependency is needed for the client secret.

### 4.2 Email sign-in links

```
email_login_tokens(token_hash, code_hash, email, purpose, user_id, invite_code,
                   app_challenge, attempts, expires_at, consumed_at)
```

- `POST /api/auth/email/start` **always answers 202**, so it cannot be used to
  test which addresses have accounts.
- The email carries a link and a 6-digit code. The code handles opening the mail
  on another device, and it is how the app signs in without a universal link.
- Only SHA-256 hashes are stored, the same rule migration 0018 set for sessions.
  Tokens are single-use and expire in 15 minutes.
- **GET renders a confirmation page and only POST consumes**, because mail
  security gateways fetch every URL. Unsubscribe was built this way for the same
  reason.
- Limits are per IP (the existing `/api/auth` limiter), 3 per address per hour,
  5 code attempts per token, and a daily global ceiling that protects SES
  reputation.

iOS needs `/app/auth/email` added to the association file. `aasa_test` pins
today's single component and iOS caches the file, so ship it ahead of the app.

### 4.3 Invite codes and direct email collection

**Invites gate every signup.** The code is taken before the provider step, and
redemption happens in `AdmitIdentity`. On email it travels in the token row. On
Apple web it travels in the handshake. On Apple native an unknown user gets
`invite_required`, enters a code, and the identity token is resubmitted.

Logins never need one, as today.

**Every user's address is collected directly.** Apple supplies one, verified,
possibly a private relay address. Email sign-in has one by construction. Spotify
users are asked for one and confirm it through a link, because Spotify can no
longer be relied on to supply it. Digest and alert opt-ins stay off by default.

**Private relay.** "If you don't register all the source domains or emails that
you use, email sent to the private relay service will result in a bounce
message." The envelope domain must match a registered domain exactly, so
`concertfinder.app` and `mail.concertfinder.app` (SES's custom MAIL FROM) are
both registered in CF-34. Easy DKIM already signs for the From domain.

**SES is the dependency.** Until CF-08 is granted, mail reaches only the one
verified recipient, so neither sign-in links nor digests reach anyone else. The
Apple path does not depend on it for signing in.

### 4.4 Spotify's email field

**Status: removed per Spotify's documentation; not confirmed against a live
response.** The February 2026 changelog lists `email` as removed from `GET /me`
(with `country`, `explicit_content`, `followers` and `product`). The migration
guide states that "Existing Development Mode apps are migrated to new
restrictions" on March 9, 2026, and this app is in Development Mode.

A live check needs a real user's token, which this design session did not have.
CF-33 logs a boolean at Spotify login, `spotify_me_email_present`, and never the
value, so the answer appears in the logs on the next sign-in.

The design does not depend on the answer. If the field is gone:

| Path | Behaviour today | With this design |
|---|---|---|
| Login | Works; a missing field decodes to `""` | Same |
| Existing stored address | Kept: `COALESCE(EXCLUDED.email, users.email)` | Same |
| New Spotify user | No address stored | Asked directly |
| Opting into email | 400 "log in again to grant email access", which no re-login can ever fix | Replaced by the verify step |
| Digest and instant workers | Quietly skip users with no address | Same, but every user has one |

---

## 5. The manual users' feed

```sql
CREATE TABLE user_artist_picks (
  user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  attraction_id  TEXT NOT NULL,
  name           TEXT NOT NULL,
  genre          TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, attraction_id)
);
CREATE TABLE user_genre_picks (
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  genre_id   TEXT NOT NULL,
  name       TEXT NOT NULL,
  PRIMARY KEY (user_id, genre_id)
);
```

Everything in these tables comes from Ticketmaster.

**Matching.** `concerts.Search` currently takes `[]spotify.ScoredArtist`. CF-29
changes it to a `concerts.Target` so the package no longer imports
`internal/spotify`, which makes "the manual path cannot reach Spotify" something
the compiler enforces.

`ScanConcertsWorker` then builds targets per user:

| User | Targets | Permits reserved |
|---|---|---|
| Picks only | Picks; `ID = "tm:<attraction>"`, attraction known | N |
| Spotify only | Today's affinity profile | 2 × artists, as today |
| Spotify with picks | Picks first, then affinity to 200 | Sum of the two |
| Genre picks | One listing per genre and grid cell | Up to 3 per genre |

Details that matter:

- `artist_resolutions` is not read for a pick, because it is keyed by Spotify ID.
- A Spotify user's pick that matches a resolved profile artist is dropped from
  the profile half, so it costs one call rather than two.
- The fallback chain is not used for picks in this version, since a pick is by
  definition a Ticketmaster artist.
- **Zero picks must not enqueue.** The SWR handler enqueues on a missing
  snapshot, and a user with no picks can never produce one, so the 10-second
  poll would re-enqueue forever. The response carries `needs_picks: true`
  instead, additively.

**Digest, alerts, unsubscribe.** These read the snapshot and the sent-ledger,
neither of which knows where an artist came from:

| Feature | Change needed |
|---|---|
| Daily digest | None for artist picks. Genre feeds need a length cap: `RenderDigest` has no limit today and a New York rock pick could mail hundreds of rows |
| Instant alerts | None. `user_subscribed_artists` holds the `tm:` ID, and the bell works the same. The column's `spotify_artist_id` name is now a misnomer, which is left alone and commented |
| Push | None |
| One-click unsubscribe | None. It is keyed on the user |
| Sent-ledger | None. It is keyed on `dedup_key` and channel |
| `reason` line | Picks show "You picked them", applied per request like the others |

Genre shows take their act from the lineup, falling back to the event name where
there is no attraction (§1.2). Facets use Ticketmaster's genre, which means
`decodeEvents` has to start reading it.

---

## 6. iOS and the App Store

### 6.1 Guideline 4.8

`docs/ios-app-plan.md` §10.2 relied on the exemption for "a client for a
specific third-party service" where "users are required to sign in to their
mail, social media, or other third-party account directly to access their
content". That stops being true the moment Spotify is optional, so the exemption
is withdrawn.

The app keeps Spotify sign-in for its five users, so 4.8 applies. Sign in with
Apple meets its three requirements: it limits data to name and email, allows
hiding the address, and does not collect interactions for advertising.

**The Spotify button.** A reviewer who taps it gets a 403, which reads as a
broken feature. It sits in a secondary section labelled for invited accounts,
with Apple and email as the primary options, and the review notes explain the
limit.

**Contingency for 5.1.1(v).** The clause about tokens stored "off of the device"
remains the same judgment call for the five Spotify users. If a reviewer objects
to it, the App Store build drops Spotify sign-in altogether. Those five users
add an email identity on the web and sign in on iOS with that, while their
Spotify connection continues server-side. That also takes 4.8 out of play, and
it needs no redesign. The same clause says core functionality not tied to a
social network needs "another mechanism", which this path now is.

### 6.2 App Review access (CF-17)

Guideline 2.1 requires "an active demo account or fully-featured demo mode". A
reviewer cannot read a mailbox and cannot be on a five-person Spotify allowlist.

CF-38 adds one configured review address and one static code, both from SSM and
inactive when either is unset, on an account seeded with picks and a location.
Reviewers can also use Sign in with Apple with an invite code from the notes.

CF-17's listening-history work is no longer needed.

### 6.3 Privacy answers

| Data | App Privacy category | Change |
|---|---|---|
| Account ID | User ID | Reworded: no longer only a Spotify ID |
| Name | Name | Now also from Apple or typed in |
| Email address | Email Address | Now collected directly from everyone |
| Coarse location | Coarse Location | None |
| Push token | Device ID | None |
| Artist and genre picks | **Other User Content** | **New**: linked, app functionality |

Apple's App Privacy page does not mention Sign in with Apple specifically. Name
and email are declared because the server retains them, which is its definition
of collection.

`PrivacyInfo.xcprivacy` must agree exactly (CF-39 first, then CF-18). The
privacy policy currently lists the email address under "What we do not collect"
(`web/src/pages/privacy.tsx:52`). It also needs Apple and email sign-in, picks,
the private relay, and SES as a processor.

### 6.4 What changes in CF-14 to CF-21

| Story | Change |
|---|---|
| CF-14 APNs key | None. The Apple sign-in key is separate (CF-34) |
| CF-15 App record | None |
| CF-16 First TestFlight | None; it can still go early with the Spotify path |
| **CF-17** Demo account | **Re-scoped**: seeded manual-path account, no listening history. Blocked by CF-37 and CF-38 |
| **CF-18** Privacy labels | **Changed**: table above. Blocked by CF-39 |
| **CF-19** Screenshots | **Changed**: must show the manual path. Blocked by CF-37 |
| **CF-20** Review notes | **Changed**: §10.1.4's text is obsolete and the 4.8 exemption is withdrawn |
| **CF-21** Submit | **Changed**: no longer sequenced behind CF-09 |

---

## 7. Spotify's terms

Spotify's text has no clause addressed to people who never authenticated. The
closest provisions are data minimization (Developer Policy I.1.1), deletion on
disconnect (I.1.2), and the bar on building databases of Spotify Content
(Developer Terms IV.3.a.i). **The rule here is stricter by choice: no Spotify
data or Spotify search for anyone who did not sign in with Spotify.**

An audit of where Spotify-derived data could reach those users:

| Path | Risk | Resolution |
|---|---|---|
| **`concerts.data`** | **Real, today's shape.** `tmEventToConcert` writes the Spotify artist ID and Spotify genres into a row shared by `dedup_key`, and upserts are last-writer-wins | CF-28: the shared row holds only what the event source said |
| `GET /me/artists/search` | Calls Spotify search | CF-32: 403 without a Spotify identity; picks use Ticketmaster for everyone |
| `artist_resolutions` | Keyed by Spotify artist ID | Not read for picks |
| `affinity_profiles`, nightly refresh | Spotify-derived, per user | Never exists for a manual user; the fanout skips them |
| `externalLinks.spotify` in Ticketmaster responses | Ticketmaster's data, but a Spotify pointer | Not decoded or stored |
| "Powered by Spotify" | Would be false on a picks-only feed | Shown only where Spotify-derived data appears |

The `concerts.data` row is the serious one, and it fails in both directions.
Serve it as is and a manual user receives Spotify-derived fields. Let a manual
scan write it and a Spotify user's bell points at a `tm:` ID, with their genre
facets gone.

The fix moves per-user artist identity to `user_concert_snapshots.artist_refs`
and `user_saved_concerts.artist_ref`, applied per request alongside `saved` and
`subscribed`. Deploy the readers before stripping the existing rows.

**The allowlist stays at five.** `SPOTIFY_MAX_USERS` (default 5) refuses a new
Spotify signup before any invite is spent; returning users are unaffected. It
does not replace the Spotify dashboard, which remains the real gate. Development
Mode also requires the app owner to hold Premium: "If the owner's Premium
subscription lapses, the app will stop working."

---

## 8. Costs

| Item | Monthly | New? |
|---|---|---|
| SES at $0.10 per 1,000: 120 users × 30 digests + about 100 sign-in mails ≈ 3,700 | $0.37 | **Yes** |
| SES at 1,000 users, same assumption | about $3 | If it grows |
| Apple Developer Program, $99/year | $8.25 | No, already held |
| Spotify Premium (US Individual, required of the app owner) | $12.99 | No, already required |
| Sign in with Apple and the private relay | $0 | n/a |
| Ticketmaster Discovery API | $0 | n/a |
| Shared box and RDS | $0 extra | No new service |

Prices are public list prices as of this writing.

---

## 9. Build order

Each is one mergeable PR. A merge to `main` deploys, so each is the operator's
call and none should land during cutover.

| Story | Piece | Blocked by |
|---|---|---|
| CF-27 | Provider-neutral identities | this doc |
| CF-28 | Spotify-derived fields out of shared rows | this doc |
| CF-29 | Concert search independent of Spotify types | this doc |
| CF-30 | Artist picks, picker, scan | CF-27, CF-28, CF-29 |
| CF-31 | Genre picks, capped shared listing | CF-30 |
| CF-32 | Hold Spotify at five, gate Spotify-only paths | CF-27 |
| CF-33 | Email links, direct address capture | CF-27 |
| CF-34 | Apple console setup (operator) | this doc |
| CF-35 | Sign in with Apple, API and web | CF-27, CF-34 |
| CF-36 | Web sign-in choices and onboarding | CF-30, 31, 32, 33, 35 |
| CF-37 | iOS sign-in and picks | CF-30, 31, 32, 33, 35 |
| CF-38 | App Review sign-in | CF-33 |
| CF-39 | Privacy policy, terms, manifest | CF-30, 33, 35 |

Public availability also needs CF-08 (SES production access), which is filed and
waiting on AWS.

---

## 10. What was not verified

- **Spotify's email removal against a live `GET /me`.** Documentation only; see
  §4.4.
- **Production Ticketmaster spend.** The ledger was not read for this design, so
  §2.2 uses the code's bounds rather than measured daily usage.
- **Whether New York is really missing from `concert_cache`** (§1.3). Inferred
  from the measured page count and the code.
- **Genres other than the six in §1.2**, and the markets beyond the four tested.
- **Average picks per user.** No real users yet; §2.2 is a range for that reason.
- **Apple's token-verification article.** The claims listed in §4.1 follow
  Apple's documented flow, but that specific page could not be retrieved, so
  CF-35 must check them against it.

Sources: Spotify [quota modes](https://developer.spotify.com/documentation/web-api/concepts/quota-modes),
[February 2026 changes](https://developer.spotify.com/documentation/web-api/references/changes/february-2026),
[migration guide](https://developer.spotify.com/documentation/web-api/tutorials/february-2026-migration-guide),
[Developer Terms](https://developer.spotify.com/terms),
[Developer Policy](https://developer.spotify.com/policy);
Apple [App Review Guidelines](https://developer.apple.com/app-store/review/guidelines/),
[App Privacy details](https://developer.apple.com/app-store/app-privacy-details/),
[private email relay](https://developer.apple.com/help/account/capabilities/configure-private-email-relay-service/),
[token revocation](https://developer.apple.com/documentation/signinwithapplerestapi/revoke-tokens);
[Ticketmaster Discovery API](https://developer.ticketmaster.com/products-and-docs/apis/discovery-api/v2/);
[SES pricing](https://aws.amazon.com/ses/pricing/);
[Spotify Premium US price](https://www.macrumors.com/2026/01/15/spotify-increasing-subscription-prices/).
