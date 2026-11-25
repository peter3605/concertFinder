# App Store copy

The fenced blocks below are the **exact text** for the App Store Connect
listing. Paste each one as-is. App Store fields are plain text: no bold, no
headings and no links survive the paste. What is in the block is what a
customer sees.

Each block carries a marker naming its field and Apple's character limit.
`scripts/release/check-store-copy.sh` reads the markers and fails if a field is
over its limit, still holds placeholder text, or has Markdown in a plain-text
field. Run it after any edit.

---

## Name

App Store Connect → **App Information → Localizable Information → Name**.
This must be the exact name reserved in App Store Connect. A tagline appended to
it is a different, unreserved name.

The reserved name is "ConcertFinder: Live Shows", tagline included, because
"ConcertFinder" alone was already taken on the App Store (confirmed 2026-11-25,
CF-20). The app's display name on the home screen stays "ConcertFinder"; only
the store listing carries the longer name.

<!-- field: name limit: 30 -->
```
ConcertFinder: Live Shows
```

## Subtitle

App Store Connect → **App Information → Localizable Information → Subtitle**.

<!-- field: subtitle limit: 30 -->
```
Gigs by the artists you love
```

## Keywords

App Store Connect → **the version page** (not App Information). Comma-separated,
no space after the commas. Do not repeat words already in the name or subtitle;
Apple indexes those already.

<!-- field: keywords limit: 100 -->
```
concerts,music,tour dates,tickets,bands,festivals,events,venues,nearby,alerts,local,indie
```

## Promotional text

App Store Connect → the version page. The one field that can change without a
new submission.

<!-- field: promotional_text limit: 170 -->
```
Pick the artists and genres you care about, set your city, and see every upcoming show near you in one list. Get an alert when an artist you follow adds a date.
```

## Description

App Store Connect → the version page.

<!-- field: description limit: 4000 -->
```
ConcertFinder shows you the concerts worth leaving the house for: upcoming shows near you, by the artists and genres you actually listen to.

Tell it what you like
Search for artists and add them to your picks, or follow whole genres like rock, jazz or hip-hop. Your picks decide what ConcertFinder looks for, so your feed is about your taste, not whatever is being promoted this week.

Set your city
Choose a city or use your current location once, and pick how far you are willing to travel. ConcertFinder searches that area and keeps the list up to date in the background.

One list, sorted by date
Every show is on one list, grouped by month. A festival or a multi-act bill appears once, with every artist you care about listed on the card. Filter by genre, venue, date range, or weeknights versus weekends, and tap through to buy tickets from the seller.

Never miss an announcement
Subscribe to an artist to get a notification when they add a show near you. Save the shows you are thinking about so they are in one place when you are ready to decide. Opt in to a weekly email that rounds up what is new.

Sign in your way
Sign in with Apple or with your email address. No password to remember.

ConcertFinder currently covers shows in the United States.
```

## Privacy policy URL

App Store Connect → **App Privacy → Privacy Policy URL** (set during CF-18; this
block is the record of what it must say).

<!-- field: privacy_policy_url limit: 255 -->
```
https://concertfinder.app/privacy
```

## App Review: sign-in information

App Store Connect → the version page → **App Review Information → Sign-In
Information**. Tick "Sign-in required", then fill the two fields **from SSM, at
paste time** - they are a working credential and never belong in this repo:

- User name: SSM `/concertfinder/REVIEW_LOGIN_EMAIL`
- Password: SSM `/concertfinder/REVIEW_LOGIN_CODE` (SecureString)

```
AWS_PROFILE=apps-admin aws ssm get-parameter --name /concertfinder/REVIEW_LOGIN_EMAIL --query Parameter.Value --output text
AWS_PROFILE=apps-admin aws ssm get-parameter --name /concertfinder/REVIEW_LOGIN_CODE --with-decryption --query Parameter.Value --output text
```

The notes below refer to those two fields rather than repeating the values.

## App Review: notes

App Store Connect → the version page → **App Review Information → Notes**.

<!-- field: review_notes limit: 4000 -->
```
Thank you for reviewing ConcertFinder.

SIGNING IN
Use the address and code in the Sign-In Information fields. The app signs in with email and a one-time code, and no mailbox is needed for this account:
1. On the first screen, stay on the "Sign in" tab.
2. Enter the review address in "Email address" and tap "Email me a sign-in link". No email is actually sent to this address.
3. On "Check your email", type the review code into "6-digit code" and tap "Sign in".
The same code works every time you sign in to this account. Each request is valid for 15 minutes and allows 5 attempts; if it expires, tap "Use a different address" and request again. The address accepts 3 requests per hour, so if a request is refused, please wait up to an hour.

The account is already set up with a location (New York) and followed genres (Rock, Pop, Alternative), so the concert feed is populated straight away. You can then:
- Save a show (bookmark on a concert card) and find it under Saved.
- Open a concert and subscribe to an artist (bell) to get a notification when they announce a nearby show.
- Add artists or genres, change the location, and filter the feed.
- In Settings: push and email notification preferences, the privacy policy and terms, Sign out, and Delete account.

ACCOUNTS AND SIGN-IN OPTIONS
Sign in with Apple and email sign-in are the primary options and work for anyone. ConcertFinder is in early access, so creating a new account needs an invite code; signing in to an existing account never does. The review account above already exists, so no code is needed.

SPOTIFY SIGN-IN IS LIMITED TO INVITED ACCOUNTS
The "Continue with Spotify" button is in a secondary section labelled "For accounts that were invited to sign in with Spotify." Spotify's developer terms restrict our app to a small allowlist of accounts that we add by hand in Spotify's dashboard, and Spotify does not grant more to individual developers. A Spotify account that is not on that list cannot complete Spotify sign-in, so please use the review account above. Spotify is optional: every feature of the app works without it. ConcertFinder is not affiliated with or endorsed by Spotify.

THIRD-PARTY TOKENS (GUIDELINE 5.1.1(v))
For the few invited Spotify users, the Spotify refresh token is held on our server, not on the device, because the server builds their concert feed in the background. It is encrypted at rest (AES-256-GCM) and never sent to the app. Users can revoke it in the app at any time: Settings > Disconnect Spotify (shown only to Spotify users, so not on the review account) deletes the stored credential and the profile built from it, and Settings > Delete account removes the whole account, including any Spotify or Apple connection, immediately. Deleting an account also revokes its Sign in with Apple token with Apple.

ACCOUNT DELETION
Settings > Delete account is real and permanent. If you delete the review account, it cannot be used again, because a new account needs an invite code; please contact us and we will restore it.

LOCATION AND NOTIFICATIONS
Location is used only to search for concerts near you, and you can type a city instead of granting access. Push notifications are optional and are used only for alerts about artists you subscribe to.

Concert listings come from Ticketmaster and cover the United States.
```

## What's New

App Store Connect → the version page. **Absent on a 1.0** - there is no field
until the second version. Add `whats_new` to `STORE_COPY_FIELDS` in
`release.conf` from then on, and rewrite this every release: the check can
count it, but it cannot tell whether it still describes the last one.

<!-- field: whats_new limit: 4000 -->
```
REPLACE_ME
```
