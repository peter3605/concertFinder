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

## What's New

App Store Connect → the version page. **Absent on a 1.0** - there is no field
until the second version. Add `whats_new` to `STORE_COPY_FIELDS` in
`release.conf` from then on, and rewrite this every release: the check can
count it, but it cannot tell whether it still describes the last one.

<!-- field: whats_new limit: 4000 -->
```
REPLACE_ME
```
