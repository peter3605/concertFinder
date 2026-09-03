# Launch runbook — the human half of Phase 4

Every task in `docs/production-plan.md` Phase 4, in the order to do them, with
the commands. Phase 4 is the list; this is the walkthrough.

**Tick the boxes in `docs/production-plan.md`, not here.** That file is the
working state of the implementation run and the thing a cleared context reads
to know where it is. This one is a runbook and goes stale the day it is
finished.

The *why* behind each AWS procedure lives in `docs/aws-deploy.md` — "Rotating a
credential", "Escrowing `ENCRYPTION_KEY`", "Restore drills", "Break-glass
access", "Terraform state". Deliberately not restated here, so the two cannot
drift into disagreeing.

Written 2026-09-01, against `production-hardening`.

Every command below assumes:

```bash
cd ~/Repos/concertFinder
export AWS_PROFILE=budgetr
```

---

## Today, ~30 minutes total

### 1. Find out whether the backup is alive

First, because everything else assumes there is a restore point and there may
not be one. Two independent reasons it may never have run:

- The systemd timer is installed by `ec2.tf`'s `user_data`, which is new in
  `production-hardening` and **has not been applied** — and `user_data` does
  not re-run on an existing instance regardless. Unless someone installed the
  units by hand from `docs/aws-deploy.md` §7, nothing is scheduled.
- Even where it is scheduled, P2-10 found `APNS_P8_KEY` is written to `.env`
  unquoted and contains spaces, and `backup-db.sh` *sources* that file — so on
  any deployment with APNs configured the dump has been failing since the day
  APNs was set up.

```bash
./scripts/restore-drill.sh --check
```

**Done when** it prints a dump from the last day or two at a plausible size.

**If it fails**, do not debug the script — look at the timer:

```bash
aws ssm start-session --target "$(cd infra && terraform output -raw ec2_instance_id)"
# on the box:
systemctl list-timers concertfinder-backup
journalctl -u concertfinder-backup -n 50 --no-pager
```

A shell syntax error in that journal is the unquoted-`.env` bug. The fix is
already on `production-hardening`, which makes merging that branch the most
load-bearing thing in this document.

### 2. Apply the Terraform, then confirm the alarm emails

**These do not exist yet.** The SNS topics, the third alarm, the `ec2:recover`
action and the backup systemd units are all in `production-hardening` and have
never been applied — the implementation run was forbidden from applying
anything. Merging the PR deploys the *code*; the infrastructure half is this
separate step, and until it runs the alarms reach nobody.

```bash
cd infra
terraform plan          # read it: this is the first apply since Phase 0
terraform apply
```

Then confirm the email subscription, which is the half an apply cannot do for
you:

```bash
for t in alerts_topic_arn billing_alerts_topic_arn; do
  aws sns list-subscriptions-by-topic --topic-arn "$(terraform output -raw $t)" \
    --query 'Subscriptions[].[Endpoint,SubscriptionArn]' --output text
done
cd ..
```

**Done when** neither line says `PendingConfirmation`. AWS mails a confirmation
link on the first apply and **Terraform reports the subscription created either
way**, so an unclicked link is an alarm that fires into nothing with a green
apply behind it. The SNS console re-sends with *Request confirmation*.

**Two things about this apply.** `APNS_P8_KEY` is already set, so the ordering
trap in §8 does not bite here — but re-read it before applying if that ever
changes. And `user_data` **does not re-run**: the backup units it now installs
land on the *next* instance, not this one, so if `systemctl list-timers
concertfinder-backup` on the box comes back empty, install them by hand from
`docs/aws-deploy.md` §7. That is very likely the answer to §1.

### 3. Rotate the Ticketmaster and Songkick keys

Assume both are in log history. P0-1 fixed the leak; before it, Songkick's key
was interpolated into the error text on *every* unexpected status, not only on
transport errors.

Safe order: create the new credential upstream, leave the old one working, put
the new one in Parameter Store, deploy, verify, *then* revoke the old one.

```bash
./scripts/set-secrets.sh
```

It walks every parameter in a fixed order; Enter skips. Two prompts to be
careful at:

- **`ENCRYPTION_KEY`** — press Enter. Not `g`. A new key invalidates every
  stored Spotify refresh token and forces every user to reconnect.
- **`APNS_P8_KEY`** — press Enter unless you are also doing §8 in this sitting.

Then re-render the instance's `.env`, which is the step that actually applies
it:

```bash
gh workflow run deploy.yml --ref main
gh run watch
```

**Done when** the run is green — `verify-deploy.sh` fetches `/api/healthz`
through Caddy as part of it. Then revoke the old keys upstream.

**The trap:** Parameter Store is read exactly once per deploy, by
`render-env.sh`. A rotated parameter with no deploy behind it changes nothing,
and nothing warns you.

**Songkick specifically:** the key is issued by request, not self-service. If
you cannot hold two at once, rotation is a hard cutover — do it immediately
before the deploy, not hours earlier. Same question for Ticketmaster: if the
portal allows a second registered app, use it for a clean overlap.

### 4. Escrow `ENCRYPTION_KEY`

```bash
aws ssm get-parameter --name /concertfinder/ENCRYPTION_KEY \
  --with-decryption --query Parameter.Value --output text
```

Into 1Password (or equivalent), labelled *"decrypts users.spotify_refresh_token
in every ConcertFinder backup"*.

This key cannot be rotated — nothing re-encrypts the stored tokens — so it also
cannot be lost. Every nightly dump stores those tokens as ciphertext under it;
lose the parameter and the backups restore to a table of unreadable bytes,
which is the exact loss the backups exist to prevent. The value is now in your
shell scrollback; clear it if that matters.

---

## Also today, because it takes weeks

### 5. Submit the Spotify Extended Quota Mode application

The long pole. Nothing downstream shortens it, and until it is granted only
allowlisted accounts can sign in — **including App Review's own reviewer**.

[developer.spotify.com/dashboard](https://developer.spotify.com/dashboard) →
the app → Settings → request Extended Quota Mode.

The answers are already true, and the story is unusually strong. State it
plainly:

- **No raw Spotify Content is persisted.** Listening data is held in memory and
  discarded after profile construction; only the derived affinity profile
  (artist IDs + scores) is stored, with a 24-hour TTL.
- **No ML training, embeddings, or similarity learning** on Spotify data.
- **"Powered by Spotify" with the official logo** on every surface showing
  derived data.
- **A revoke path inside the app** — `DELETE /api/me/spotify-connection`,
  separate from account deletion.
- **What it does:** a personalized concert feed built by matching the user's own
  listening against ticketing APIs. US-only, single developer.

`CLAUDE.md`'s constraints section is the source for all of those if the form
wants detail.

---

## This week

### 6. The timed restore drill

§1 proved a file exists. This proves it restores, and produces the number
nobody has: the RTO.

1. [Neon console](https://console.neon.tech) → branch production → name it
   `restore-drill`.
2. Copy its **direct** connection string (no `-pooler`), ending in
   `?sslmode=require`.
3. Docker running, then:

```bash
./scripts/restore-drill.sh 'postgres://…restore-drill…?sslmode=require'
```

**Done when** it prints `DRILL PASSED` and a restore time. Record that number in
`docs/aws-deploy.md` under "Restore drills", then delete the branch.

The script refuses a target that already holds rows in `users`, so a mispasted
production URL is a refusal rather than an outage. It asserts the four
irreplaceable tables came back with rows — a restore that produces an empty
schema exits 0 and otherwise looks exactly like a pass.

**What the number excludes:** noticing, deciding, making the branch, pointing
`DATABASE_URL` at it, and deploying. The real RTO is that plus what the script
measures.

### 7. Move Terraform state to S3

Today it is a local file in a git checkout on one laptop, holding the SES SMTP
password and the break-glass private key in cleartext, with no locking.

```bash
cd infra/bootstrap
terraform init
terraform apply                      # creates the bucket; read the plan first

cd ..
cp backend.tf.example backend.tf
aws sts get-caller-identity --query Account --output text   # paste into backend.tf
terraform init -migrate-state        # answer "yes" to copying existing state

rm terraform.tfstate terraform.tfstate.backup
git add backend.tf && git commit -m "Terraform: adopt the S3 state backend"
```

**Done when** `terraform plan` runs clean against the remote state and shows no
changes.

**Do not skip the `rm`.** After migration those files are stale *and* still full
of live secrets; the backend does not make the old copy go away. Bump
`required_version` in `main.tf` to `>= 1.11.0` at the same time — locking is
S3-native `use_lockfile`, and on an older CLI it is silently absent rather than
an error.

---

## Before the first TestFlight upload

### 8. Reissue the APNs key as Sandbox & Production

The current key (`42KZTQHRRH`) was created Sandbox-only, hence
`apns_environment = "sandbox"`. A sandbox-only key cannot reach the production
host, and the failure mode is the bad one: the wrong host answers
`BadDeviceToken`, the server reads that as a dead token and **permanently
disables the device**. A TestFlight build against this key does not drop one
notification, it costs each tester every future one.

1. [Apple developer → Keys](https://developer.apple.com/account/resources/authkeys/list)
   → **+**, enable *Apple Push Notifications service (APNs)*, and choose the
   unrestricted environment option — not Sandbox.
2. **Download the `.p8` immediately.** Apple allows exactly one download.
3. Note the new Key ID.
4. In `infra/terraform.tfvars`:
   ```hcl
   apns_key_id      = "<new key id>"
   apns_environment = "sandbox,production"
   ```
5. `cd infra && terraform apply` — then, **in the same sitting**,
   `./scripts/set-secrets.sh` and answer `f` at the `APNS_P8_KEY` prompt with
   the path to the new `.p8`.
6. `gh workflow run deploy.yml --ref main`
7. Revoke the old key once push is confirmed working on a real device.

**The ordering trap** (`docs/aws-deploy.md` §7a): `APNS_P8_KEY` is an operator
secret, so an apply that touches it creates the parameter holding `REPLACE_ME`,
and `render-env.sh` refuses to write `.env` while any parameter holds that
sentinel. Applying without the key in hand breaks the next deploy.
`set-secrets.sh` also escapes the PEM's newlines on the way in — never paste the
key into the console by hand, or it truncates at `-----BEGIN PRIVATE KEY-----`,
passes `config.Validate`, and fails later at send time.

After this, `APNS_ENVIRONMENT` never changes again: it names what the key is
authorized for, and the server picks the host per device.

---

## Before submission

### 9. Build the demo account

App Review signs in with credentials you supply, and without Extended Quota
Mode that account must be on the allowlist or the reviewer gets a 403 and a
rejection.

1. Create a dedicated Spotify account with an email you control.
2. Give it a history the affinity scorer can use. The six signals are followed
   artists, top artists, saved albums, saved tracks, recently played, and owned
   playlists — so follow 20–30 artists, save albums and tracks, and actually
   play music on it across a few sessions so recently-played and the
   top-artist time ranges populate.
3. Dashboard → the app → User Management → add the account's email.
4. **Verify end to end on a clean device:** sign in, set a location, wait for
   the first scan to fill the feed, save a show, subscribe to an artist, and
   confirm push arrives.

Step 4 is the one that gets skipped. A demo account that half-works is a
rejection with a two-week round trip attached.

### 10. App Store Connect

`docs/ios-app-plan.md` §9 is the checklist; §10.1.4 has review-notes text ready
to paste.

- **Privacy nutrition labels** — Spotify user ID and display name, email,
  coarse location, device token, all *linked, app functionality*. They must
  agree with `NSPrivacyCollectedDataTypes` in
  `ios/ConcertFinder/Resources/PrivacyInfo.xcprivacy`; change both together.
- **Review notes** — why the app requires Spotify (it is a client for the
  user's own data: the 4.8 exemption), that no Spotify credentials are stored
  on device and no listening data is retained, and the demo credentials. Raise
  5.1.1(v) and 4.8 proactively rather than discovering them at review.
- **Screenshots** at whatever sizes App Store Connect currently requires — they
  change; check at upload.
- **A description that does not imply a Spotify partnership.**

**The failure that is invisible locally:** a bad privacy manifest does not fail
the upload. Processing appears to run, App Store Connect then emails ITMS-91053,
and the build never becomes available. Nothing in Xcode or CI sees it.

### 11. Decide what launch means

A product call, deliberately not decided in the implementation run. The
arithmetic that constrains it:

Ticketmaster's ceiling is 5000 calls/day per key. A cold scan costs roughly one
call per artist up to `spotify.MaxScoredArtists` (200). So ~25 cold scans/day in
the worst case, and considerably more in the realistic one, because
`concert_cache` (`DefaultCacheTTL`, 12h) makes the second user in a city nearly
free.

What changed since `docs/ios-app-plan.md` §3.3 was written is that exceeding it
is no longer silent: `rate_ledger_account` turns it into a `retry_after` — a
visible wait, with a reason, in both clients — rather than upstream 403s that
look exactly like artists with no shows. That is what makes a staged admission
survivable. It is not a reason to skip staging it: a waitlist with a per-day
admission cap is the honest version and doubles as capacity control, and it
wants to exist before Extended Quota Mode lands, not after.

---

## If you only do one thing today

§1, then §5. The first says whether there are backups at all. The second is the
only item where waiting costs weeks that cannot be recovered.

And note the shape of the dependency the first few steps share: merging PR #28
ships code, `terraform apply` ships infrastructure, and they are separate
actions with separate failure modes. The hardening branch fixes the `.env`
quoting that breaks the backup script; the apply installs the timer that runs
it. Neither alone gives you a working nightly backup.
