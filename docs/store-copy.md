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
REPLACE_ME
```

## Subtitle

App Store Connect → **App Information → Localizable Information → Subtitle**.

<!-- field: subtitle limit: 30 -->
```
REPLACE_ME
```

## Keywords

App Store Connect → **the version page** (not App Information). Comma-separated,
no space after the commas. Do not repeat words already in the name or subtitle;
Apple indexes those already.

<!-- field: keywords limit: 100 -->
```
REPLACE_ME
```

## Promotional text

App Store Connect → the version page. The one field that can change without a
new submission.

<!-- field: promotional_text limit: 170 -->
```
REPLACE_ME
```

## Description

App Store Connect → the version page.

<!-- field: description limit: 4000 -->
```
REPLACE_ME
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
