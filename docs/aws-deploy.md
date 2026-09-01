# AWS deploy (EC2 + Neon Postgres)

One-time setup for pushing to `main` → automatic deploy on an EC2 t4g.small in
us-east-1, with Postgres hosted on [Neon](https://neon.tech) rather than RDS.

## What this actually costs

**AWS replaced the 12-month free tier on 2025-07-15.** Accounts created after
that date get $100 in credits (up to $200 with onboarding tasks) on a *Free
plan* that lasts 6 months or until the credits run out — and when it ends, AWS
**closes the account and deletes the resources** after a 90-day grace period.
That is not a hosting plan. Run this on the **Paid (pay-as-you-go) plan**;
credits still apply against the bill, they just stop being a cliff.

Accounts created *before* 2025-07-15 keep the legacy 12-month free tier, under
which 750 hrs of public IPv4 are genuinely free. Check which you have in
Billing → Free tier before assuming either.

Steady-state monthly cost in us-east-1, excluding credits:

| Item | Now | From 2027-01-01 |
|---|---|---|
| EC2 t4g.small | $0 — [free trial, 750 hrs/mo, ends 2026-12-31](https://aws.amazon.com/ec2/instance-types/t4/) | ~$12.26 |
| Postgres (Neon free plan) | $0 — see the compute-hour note below | $0 |
| Public IPv4 (Elastic IP) | ~$3.65 (free on legacy accounts) | ~$3.65 |
| EBS 20 GiB gp3 | ~$1.60 | ~$1.60 |
| S3 nightly dumps | <$0.10 at this size | <$0.10 |
| DNS | $0 — Cloudflare, no Route 53 zone | $0 |
| SES, SSM, CloudWatch alarms, 100 GB egress | $0 at this scale | $0 |

Plus the domain itself (~$10–15/yr). The t4g.small trial expiring on
2026-12-31 is the one dated cliff worth a calendar reminder — it is not tied to
account age, so it ends on that date no matter when you signed up.

IPv6-only would remove the $3.65, but Spotify's redirect and Let's Encrypt both
need reliable v4 reach.

### Why Neon and not RDS, and not a container on the box

RDS db.t4g.micro + 20 GiB was ~$14/mo, the largest line on the bill once the
EC2 trial is accounted for. Two ways to remove it; they are not equivalent.

Running Postgres as a container on the EC2 box saves the same $14 but puts the
database in the same 2 GiB as everything else — and that box builds the Docker
image during every deploy. The bootstrap script in `infra/ec2.tf` allocates 2
GiB of swap specifically because the `npm ci` + Vite build has OOM headroom
problems there, and the workflow splits `build` from `up -d` because a failed
build used to take the site down with it. Adding Postgres to that box means a
bad deploy can take the *database* down too. It also puts you back in the
business of running your own backups on a host with one EBS volume.

Neon moves the database off that box entirely, which is the actual argument.
The cost saving is the same; the blast radius is smaller.

### The compute-hour budget is what binds

Neon markets scale-to-zero. **This app never scales to zero**, so ignore that
part of the pitch and do the arithmetic instead.

River polls. `FetchPollInterval` defaults to 1 second and runs as a fallback
*even when* LISTEN/NOTIFY is working, on top of leader-election maintenance. The
database therefore gets a query every second forever, and Neon's autosuspend
never fires.

So the free plan is a compute-hour question, not a storage one:

- Free plan is ~192 compute-hours (CU-hours)/month and 0.5 GB storage. **Verify
  the current numbers** — Neon has changed them before and this is the whole
  margin.
- A 24/7 compute at the 0.25 CU floor is ~730 h × 0.25 = **~183 CU-hours**. It
  fits, with roughly 5% headroom.
- **Pin the compute to min 0.25 CU *and* max 0.25 CU.** Leave autoscaling on and
  a single nightly `ScanConcerts` fanout spike puts you over. Nothing about
  going over is loud: Neon suspends the compute, and every request 500s.

If that is too tight, Neon's paid entry tier (~$5/mo at time of writing) is
still well under the $14 it replaces. Set a usage alert in the Neon console —
`infra/cloudwatch.tf` cannot see any of this, because Neon publishes nothing to
CloudWatch.

### What this gives up

- **Backup retention.** RDS had `backup_retention_period = 7` plus a final
  snapshot; Neon's free plan restore history is much shorter. A nightly
  `pg_dump` to S3 covers it — the timer is installed by `infra/ec2.tf`'s
  user_data and §7 has the details.
- **In-VPC isolation.** Database traffic now crosses the public internet with
  TLS instead of staying inside the security group. `?sslmode=require` on the
  connection string is what replaces the `rds.force_ssl=1` parameter group.
- **Sub-millisecond latency.** Same-region round trips go to single-digit ms.
  Not a threat here: the scan fan-out is already bounded by a semaphore of 10
  inside a 5-minute `ScanBudget`, and the SWR read path serves from a snapshot.

Do the setup steps once, in order. After that, `git push origin main`
deploys automatically via the workflow in `.github/workflows/deploy.yml`.

## 0. Prerequisites

- New AWS account (12-month free tier active)
- A domain you control (needed for Spotify's redirect URI — Spotify rejects
  `nip.io`, `duckdns.org`, and other free dynamic-DNS domains for OAuth).
- AWS CLI installed and logged in as an admin locally (only for setup).
- GitHub repo pushed (`git@github.com:peter3605/concertFinder.git`).

## 1. Neon: managed Postgres

Not Terraform. There is no Neon provider configured in `/infra` on purpose —
the database is one console object created once, and keeping it out of state
means the plaintext database password is no longer sitting in
`terraform.tfstate`.

[console.neon.tech](https://console.neon.tech) → Create project.

- Postgres version: whatever Neon's current default is. **Match `PG_IMAGE` in
  `scripts/backup-db.sh` to it** (see §7) — `pg_dump` refuses to run against a
  server newer than itself. The live project reports 18.6, so the script pins
  `postgres:18-alpine`; `SHOW server_version` is the check.
- Region: **AWS us-east-1**, the same region as the EC2 box. Cross-region here
  would add tens of milliseconds to every one of the scan worker's round trips.
- Database name: `concertfinder`
- Compute: **min 0.25 CU, max 0.25 CU.** Set both. This is the free plan's
  compute-hour budget, not a performance preference — see "The compute-hour
  budget is what binds" above.
- Autosuspend: leave it at the default. It will never fire (River polls every
  second), and turning it off changes nothing except removing your safety net if
  the app is ever stopped.

Then copy the connection string from Connection Details:

- Choose the **direct** connection, **not** "Pooled connection". River uses
  LISTEN/NOTIFY and Neon's pooled endpoint is PgBouncer in transaction mode,
  which does not support it and does not report that it does not. Job pickup
  degrades to the 1s poll fallback and leader resignations take ~5s, silently.
- Keep `?sslmode=require` on the end.

It should look like:

```
postgres://neondb_owner:<pw>@ep-xxxx-xxxx.us-east-1.aws.neon.tech/neondb?sslmode=require
```

`neondb_owner` and `neondb` are Neon's defaults. Nothing in the code cares what
either is called, so there is no reason to rename them to match the old RDS
`concertfinder`/`concertfinder`: the migrations create tables inside whatever
database the URL points at, and no migration contains `CREATE DATABASE` or
`CREATE SCHEMA`.

Run the migrations against it before the first deploy — nothing in the deploy
path does this for you. They are idempotent and applied by the server at
startup, so the simplest route is to point a local `go run ./cmd/server` at the
Neon URL once; it applies the 15 app migrations plus River's 6 and then serves.

Set a usage alert in the Neon console while you are here. Storage and
compute-hour headroom are visible nowhere else; `infra/cloudwatch.tf`
deliberately has no database alarm because Neon publishes no CloudWatch
metrics.

## 2. EC2: application host

AWS Console → EC2 → Launch instance.

- Name: `concertfinder`
- AMI: **Amazon Linux 2023** (ARM64)
- Instance type: **t4g.small** (free tier eligible for the first 12 months)
- Key pair: create one, download the `.pem` — you'll rarely use it since
  SSM is the primary access channel, but keep it as a break-glass.
- VPC: default VPC in us-east-1
- Auto-assign public IP: Enabled
- Security group: create new `concertfinder-ec2-sg`, allow inbound:
  - `TCP 80` from `0.0.0.0/0`
  - `TCP 443` from `0.0.0.0/0`
  - **Do not open 22.** SSM handles admin access.
- Storage: 20 GiB gp3 (free tier includes 30 GiB)
- **Advanced → IAM instance profile:** attach the role from step 4 below
  (create the role first, then edit this instance to attach it if you got
  here first).

There is no database security group to pair this with any more. Neon is reached
outbound over the public internet on 5432, which the `ec2-sg` all-outbound
egress rule already permits; nothing extra needs opening. Restricting egress to
Neon's addresses is not practical — they are not stable, and the same rule
carries Spotify, Ticketmaster, MusicBrainz, Nominatim, SES, and S3.

Give the EC2 instance a static Elastic IP (EC2 → Elastic IPs → Allocate,
then Associate). Free while attached to a running instance.

## 3. Bootstrap the EC2 box

Connect once via SSM (EC2 → Instances → Connect → Session Manager). Then:

```
# Docker + compose plugin (Amazon Linux 2023)
sudo dnf update -y
sudo dnf install -y docker git
sudo systemctl enable --now docker
sudo usermod -aG docker ec2-user

# docker compose v2 as a CLI plugin
DOCKER_CONFIG=/usr/local/lib/docker
sudo mkdir -p $DOCKER_CONFIG/cli-plugins
sudo curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-aarch64 \
     -o $DOCKER_CONFIG/cli-plugins/docker-compose
sudo chmod +x $DOCKER_CONFIG/cli-plugins/docker-compose

# app user + working directory
sudo useradd -m -s /bin/bash concertfinder
sudo usermod -aG docker concertfinder
sudo mkdir -p /opt/concertfinder
sudo chown concertfinder:concertfinder /opt/concertfinder

# clone the repo (as the app user)
sudo -u concertfinder git clone https://github.com/peter3605/concertFinder.git /opt/concertfinder
```

Create `/opt/concertfinder/.env` with production values (Spotify creds,
Ticketmaster key, the Neon `DATABASE_URL` from §1, encryption key,
`SITE_DOMAIN` for Caddy, `BACKUP_S3_BUCKET` from
`terraform output backup_bucket`). Use `/etc/environment`-style syntax; owner
`concertfinder`, mode `600`. Example:

```
# Neon, direct (unpooled) endpoint — the pooled one breaks River's LISTEN/NOTIFY.
DATABASE_URL=postgres://neondb_owner:<pw>@ep-xxxx-xxxx.us-east-1.aws.neon.tech/neondb?sslmode=require
BACKUP_S3_BUCKET=concertfinder-backups-<account-id>
ENCRYPTION_KEY=<openssl rand -hex 32>
SPOTIFY_CLIENT_ID=<from developer.spotify.com>
SPOTIFY_REDIRECT_URI=https://your-domain.com/api/auth/callback
TICKETMASTER_API_KEY=<from developer.ticketmaster.com>
SESSION_COOKIE_DOMAIN=your-domain.com
LISTEN_ADDR=:8080
# Read by Caddy, not by the Go binary — docker-compose.prod.yml hands this same
# file to the caddy container, whose site block is literally `{$SITE_DOMAIN} {`.
# Unset, that line collapses into a global options block and Caddy exits with
# "unrecognized global option: encode", which restart: unless-stopped turns into
# a crash loop beside a healthy-looking api container. Bare host, no scheme, and
# it must match the host in SITE_BASE_URL below.
SITE_DOMAIN=your-domain.com
USER_LATITUDE=40.7128
USER_LONGITUDE=-74.0060
USER_RADIUS_MILES=50

# Public base URL. NOT optional in production despite having a default: it is
# what unsubscribe links in outgoing email are built from, and it is half of
# the User-Agent sent to MusicBrainz and Nominatim. Left unset it falls back to
# https://127.0.0.1:3000, so recipients get unsubscribe links pointing at their
# own machine. The server refuses to start on that combination.
SITE_BASE_URL=https://your-domain.com
# Rendered on the public Privacy and Terms pages, and the address MusicBrainz
# and Nominatim would use to reach you before rate-limiting. Defaults to the
# author's personal address — set it.
CONTACT_EMAIL=you@your-domain.com

# Email. Stays in 'log' mode (messages go to slog, nothing is sent) until you
# set this to 'smtp'. SES starts in sandbox mode — see step 6.
EMAIL_DELIVERY_MODE=log
SMTP_HOST=email-smtp.us-east-1.amazonaws.com
SMTP_PORT=587
SMTP_USERNAME=<terraform output ses_smtp_username>
SMTP_PASSWORD=<terraform output ses_smtp_password>
# The address must be exactly `terraform output ses_from_address`
# (notifications@ by default). The SES IAM policy conditions on
# ses:FromAddress, so a different local part is denied at send time — SMTP
# accepts the session and the message bounces, which nothing here validates.
SMTP_FROM=ConcertFinder <notifications@your-domain.com>
```

The server validates all of this at startup and refuses to boot on anything
that would fail silently later — a malformed `ENCRYPTION_KEY`, a redirect URI
that isn't `/api/auth/callback`, a loopback `SITE_BASE_URL` behind a real
cookie domain, a missing `SITE_DOMAIN` or one that disagrees with
`SITE_BASE_URL`, `EMAIL_DELIVERY_MODE=smtp` with no relay. Every problem is
reported at once, so a bad `.env` costs one round trip rather than one restart
per variable. `docker compose logs api` shows them.

`SITE_DOMAIN` is checked here even though this binary never uses it, because
it is the one setting nothing else can see: CI's `scripts/check-deploy-config.sh`
validates the Caddyfile against a synthetic `.env` it writes itself, so it
proves the wiring but never reads this file. The api container does read it,
so a clear message in `docker compose logs api` beats Caddy dying over a
variable its error message doesn't mention.

Kick the first deploy manually to confirm it boots. Build and bring up as
separate commands, the same way the workflow does — `up -d --build` would tear
down running containers as part of the same command, so a failed or OOM-killed
build takes the site with it:

```
sudo -u concertfinder bash -c 'cd /opt/concertfinder \
  && docker compose -f docker-compose.prod.yml build \
  && docker compose -f docker-compose.prod.yml up -d --wait --wait-timeout 240 \
  && ./scripts/verify-deploy.sh'
```

`--wait` blocks on the api container's healthcheck (the binary probes its own
`/api/healthz`, since the distroless image has no shell or curl), and
`verify-deploy.sh` then fetches `https://<SITE_DOMAIN>/api/healthz` through
Caddy so the whole chain is proven: TLS, the proxy hop, the api, and Postgres.
Without both, `up -d` returns 0 the moment a container is *started* — so a
container that exits on a bad `.env` and crash-loops under
`restart: unless-stopped` looks exactly like a successful deploy.

## 4. IAM: OIDC identity provider for GitHub Actions

This lets the workflow assume an AWS role without any long-lived access keys
stored in GitHub secrets.

**a. Create the OIDC provider (once per AWS account).**

```
aws iam create-open-id-connect-provider \
  --url https://token.actions.githubusercontent.com \
  --client-id-list sts.amazonaws.com \
  --thumbprint-list 6938fd4d98bab03faadb97b34396831e3780aea1
```

**b. Create the deploy role.** Save this as `trust.json`:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {
      "Federated": "arn:aws:iam::<ACCOUNT_ID>:oidc-provider/token.actions.githubusercontent.com"
    },
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {
        "token.actions.githubusercontent.com:aud": "sts.amazonaws.com"
      },
      "StringLike": {
        "token.actions.githubusercontent.com:sub": "repo:peter3605/concertFinder:ref:refs/heads/main"
      }
    }
  }]
}
```

The `sub` condition pins the role to your repo + main branch. Any other
branch or repo attempting to assume this role will be denied.

```
aws iam create-role \
  --role-name GitHubActionsConcertFinderDeploy \
  --assume-role-policy-document file://trust.json
```

**c. Attach a minimal permissions policy.** Save as `deploy-policy.json`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ssm:SendCommand",
        "ssm:ListCommands",
        "ssm:GetCommandInvocation"
      ],
      "Resource": [
        "arn:aws:ec2:us-east-1:<ACCOUNT_ID>:instance/<EC2_INSTANCE_ID>",
        "arn:aws:ssm:us-east-1::document/AWS-RunShellScript",
        "arn:aws:ssm:us-east-1:<ACCOUNT_ID>:*"
      ]
    }
  ]
}
```

```
aws iam put-role-policy \
  --role-name GitHubActionsConcertFinderDeploy \
  --policy-name Deploy \
  --policy-document file://deploy-policy.json
```

Note the role ARN:
`arn:aws:iam::<ACCOUNT_ID>:role/GitHubActionsConcertFinderDeploy`.

**d. Give the EC2 instance profile SSM access.** Create/attach a role
named `concertfinder-ec2` to the EC2 instance with the managed policy
`AmazonSSMManagedInstanceCore`. (Step 2 said to attach this profile;
create it now if you didn't.)

## 5. GitHub repo secrets

Repo Settings → Secrets and variables → Actions → New repository secret:

- `AWS_DEPLOY_ROLE_ARN` — the role ARN from step 4.
- `EC2_INSTANCE_ID` — `i-0123456789abcdef0`.

That's the entire set of GH secrets. No AWS keys.

## 6. Domain + DNS

- Register a domain (Cloudflare Registrar or Porkbun are cheapest — ~$10/yr).
- Leave DNS with the registrar. Registering at Cloudflare means it is already
  the authoritative nameserver, so there is no hosted zone to create, nothing
  to delegate, and no propagation wait — records are live in seconds. This is
  why `/infra` has no Route 53 resources.
- Create the records from `terraform output dns_records`: the apex `A` → your
  Elastic IP, plus the SES verification TXT, three DKIM CNAMEs, and the MAIL
  FROM MX + SPF pair.
- **Set every record to "DNS only" (grey cloud).** Cloudflare's proxy answers
  CNAME lookups with its own addresses, which makes DKIM verification fail
  silently and MX delivery impossible. The apex `A` should stay unproxied too
  until the origin reads `CF-Connecting-IP` — see `infra/README.md`, "DNS
  records", for why proxying breaks the `/api/auth` rate limiter.
- Update Spotify Developer Dashboard: redirect URI → `https://your-domain.com/api/auth/callback`.
  It must be this exact path. The handler is mounted at `/api/auth/callback`
  (chi: `/api` → `/auth` → `auth.Mount`), and a redirect to `/callback`
  falls through to the SPA's catch-all — so the browser lands on the app
  looking logged out, with no error anywhere to say the URI was wrong.

Caddy handles the TLS cert automatically the first time a request lands on
port 443 for your domain.

## 7. Nightly database backups

Moving off RDS gave up `backup_retention_period = 7` and the final snapshot.
Neon's free plan keeps a much shorter restore history, so this replaces it.

Most of the schema does not need protecting — `concerts`, the three caches, and
`user_concert_snapshots` all rebuild on the next scan. What does not rebuild is
`users` (the AES-GCM-encrypted Spotify refresh tokens and email preferences),
`user_saved_concerts`, `user_subscribed_artists`, and `user_locations`. Losing
those means every user re-authorizes with Spotify and loses their stars and
bells.

`terraform apply` creates the bucket (`aws_s3_bucket.backups`) and grants the
instance role `s3:PutObject` on it — and nothing else. No `GetObject`, no
`DeleteObject`: restoring is a rare manual operation you do from your laptop
with admin credentials, and write-only means a compromised box cannot read back
every previous night's encrypted refresh tokens or delete the history on its way
out.

Put `BACKUP_S3_BUCKET` in `/opt/concertfinder/.env` (from
`terraform output backup_bucket`). That is the only step for a freshly built
instance: **the `concertfinder-backup.service` and `.timer` units are installed
and enabled by `infra/ec2.tf`'s user_data**, alongside the swapfile and the
docker plugins. They used to exist only as the copy-paste below, which meant a
rebuilt box came up with no backups and nothing anywhere said so — the kind of
failure you find out about on the night you need a dump.

Two caveats that follow from where they live now:

- **user_data does not re-run**, and `lifecycle { ignore_changes = [user_data] }`
  keeps an edit from replacing the instance. An instance that predates this
  change therefore still has no units; install them once by hand with the block
  below. `systemctl list-timers concertfinder-backup` says which case you are in.
- The units reference `/opt/concertfinder/scripts/backup-db.sh`, which does not
  exist until the clone in §3. That is fine — a systemd timer does not resolve
  its service's `ExecStart` until it fires, and the clone happens long before
  the first 03:00.

Optionally set `BACKUP_HEARTBEAT_URL` in the same `.env`. The script pings it
after a verified upload, and that ping is the only thing that catches the
failures which produce *no* output: a timer that was never installed, a box that
was rebuilt, a unit that quietly stopped firing. Everything else in the script
fails loudly into a journal nobody reads. A failed ping is logged and does not
fail the backup — the dump is already in S3 by then. Point it at
healthchecks.io, Better Stack, or an SNS HTTPS subscription, and treat the URL
as a credential.

The manual install, for an instance that predates user_data:

```
sudo tee /etc/systemd/system/concertfinder-backup.service >/dev/null <<'EOF'
[Unit]
Description=Nightly pg_dump of the ConcertFinder database to S3
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
User=concertfinder
ExecStart=/opt/concertfinder/scripts/backup-db.sh
EOF

sudo tee /etc/systemd/system/concertfinder-backup.timer >/dev/null <<'EOF'
[Unit]
Description=Run the ConcertFinder database backup nightly

[Timer]
# 03:00 UTC — clear of every DAILY_*_HOUR_UTC job (affinity 06, scan 07,
# digest 09, janitor 10), so the dump is not competing with a scan for the
# 0.25 CU Neon compute.
OnCalendar=*-*-* 03:00:00 UTC
Persistent=true

[Install]
WantedBy=timers.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now concertfinder-backup.timer
```

Verify it end to end now rather than discovering it at 03:00:

```
sudo systemctl start concertfinder-backup.service
journalctl -u concertfinder-backup.service -n 20 --no-pager
aws s3 ls s3://$(grep '^BACKUP_S3_BUCKET=' /opt/concertfinder/.env | cut -d= -f2)/pg/
```

The script dumps to a temp file, checks it is non-empty, and runs
`pg_restore --list` over it before uploading — a dump that dies partway still
produces bytes, and streaming those straight to S3 would leave a truncated
object under today's key that looks exactly like a backup.

**`PG_IMAGE` in the script pins `postgres:18-alpine`**, matching the 18.6 the
Neon project reports. `pg_dump` aborts outright against a server newer than
itself, so when Neon bumps the project's major version, bump this too. It is
pinned rather than `:latest` so that mismatch fails loudly on an ordinary night
rather than on the one where you need the dump.

To restore, from your laptop with admin credentials:

```
aws s3 cp s3://<bucket>/pg/concertfinder-<date>.dump .
pg_restore --dbname="$DATABASE_URL" --clean --if-exists concertfinder-<date>.dump
# or just the irreplaceable tables:
pg_restore --dbname="$DATABASE_URL" --data-only \
  -t users -t user_saved_concerts -t user_subscribed_artists -t user_locations \
  concertfinder-<date>.dump
```

## 7a. iOS client configuration (optional)

Skip this entirely if you are not shipping the iOS app — with one caveat at
the end, which will otherwise fail your next deploy.

Everything here is optional in the sense that unset means the web app behaves
exactly as before: `/api/auth/login?client=ios` returns 501, the
`apple-app-site-association` file 404s, and the push worker no-ops. What is
*not* allowed is a partial APNs set. `config.Validate` refuses to start on
one, because a half-configured push path wires up successfully and then drops
every notification silently — indistinguishable from nobody having opted in.

Terraform derives most of it from `var.domain` and `var.ios_bundle_id`
(`infra/variables.tf`), so the values you supply are:

```hcl
ios_bundle_id    = "com.example.concertfinder"  # must match the real App ID
ios_team_id      = "ABCDE12345"                 # Apple Developer team
apns_key_id      = "XYZ9876543"                 # the .p8's key ID
apns_environment = "sandbox,production"         # what the .p8 is authorized for, not a host
min_ios_build    = 0                            # oldest client build the server accepts
```

`MOBILE_CALLBACK_URL` and `IOS_APP_ID` are derived from those, so they cannot
drift from the domain the certificate is issued for — which matters because
iOS fetches the association file from the universal link's *own* host, and a
mismatch ends every mobile login in Safari with the app still waiting.

The `.p8` private key is an operator secret, not a Terraform value:

```bash
./scripts/set-secrets.sh     # offers APNS_P8_KEY; choose [f] and give the path
```

**The caveat, and it applies even if you never ship the app.** `APNS_P8_KEY`
is in `operator_secrets`, so `terraform apply` creates the parameter holding
the `REPLACE_ME` sentinel — and `render-env.sh` refuses to write the env file
while any parameter still holds it, which fails the deploy by design. On a
deployment with no Apple account, mark it unused once:

```bash
./scripts/set-secrets.sh     # choose [-] at the APNS_P8_KEY prompt
```

That writes a single space, which the Go side reads as "this integration is
off" — the same convention `SONGKICK_API_KEY` and `BRAVE_SEARCH_API_KEY` use.

Finally, on the client side: `ios/project.yml` carries the API origin per
configuration and `ios/ConcertFinder/Resources/ConcertFinder.entitlements`
carries `applinks:`. Both are already set to this deployment's domain; the
bundle identifier is the one value that still has to match your real App ID.
See `ios/README.md`.

---

## 8. First automated deploy

```
git commit --allow-empty -m "chore: trigger first CI deploy"
git push origin main
```

Watch the Actions tab. The `test` job runs, then `deploy` fires off an SSM
command against your instance; you'll see stdout printed in the workflow
output when it finishes.

## Rolling back

The workflow resets the instance to the exact commit that triggered it
(`github.sha`, not `origin/main` — two merges landing close together otherwise
mean the older run builds the newer run's tree, so what ships is a commit CI
never tested), builds, tags the image with that SHA, brings containers up with
`--wait`, and then runs `scripts/verify-deploy.sh`. The normal rollback is
therefore just:

```
git revert <bad-commit> && git push
```

**The fast rollback is a retag, not a rebuild.** Every deploy leaves its image
behind as `concertfinder-api:<sha>` and `scripts/prune-images.sh` keeps the last
three, so going back does not mean running `npm ci` and a Vite build on a 2 GiB
box at the moment the site is already unhappy:

```
sudo -u concertfinder bash -c 'cd /opt/concertfinder \
  && docker image ls concertfinder-api \
  && docker tag concertfinder-api:<good-sha> concertfinder-api:latest \
  && docker compose -f docker-compose.prod.yml up -d --wait --wait-timeout 240 \
  && ./scripts/verify-deploy.sh'
```

Note this rolls back the *image* only. The checkout under `/opt/concertfinder`
still points at the bad commit, so anything read from the working tree rather
than baked into the image — `Caddyfile`, `docker-compose.prod.yml`, the scripts
— is still the new version. If the bad change is in one of those, or if the SHA
you want is older than the three kept images, rebuild instead. **Keep `build`
and `up` as separate commands** — `up -d --build` tears the running containers
down as part of the same command, so a build that fails or runs the box out of
memory takes the site with it:

```
sudo -u concertfinder bash -c 'cd /opt/concertfinder \
  && git reset --hard <good-sha> \
  && docker compose -f docker-compose.prod.yml build \
  && docker compose -f docker-compose.prod.yml up -d --wait --wait-timeout 240 \
  && ./scripts/verify-deploy.sh'
```

If a deploy fails, the workflow prints the SSM output including
`docker compose ps` and the last 80 lines of container logs — `verify-deploy.sh`
dumps both on its way out. Config problems appear there in full:
`config.Validate` reports every bad variable at once rather than one per
restart.

## What's not in this setup

Deliberately kept out to keep the year-1 bill at ~$16:

- **No load balancer.** Traffic hits EC2 directly; Caddy terminates TLS.
  ALB would add $16/mo.
- **No auto-scaling.** Single-instance; if it dies, restart it. Fine for
  personal-project scale.
- **No CloudWatch dashboards, and no application-level alerting.**
  `infra/cloudwatch.tf` defines three metric alarms — EC2 status check, EC2
  *system* status check (whose action is `ec2:recover`, so it fixes rather than
  reports), and estimated billing — and all three now publish to an SNS topic
  with an email subscription on `var.alert_email`. Two things to know about
  that: an email subscription stays **PendingConfirmation** until you click the
  link AWS mails on the first apply, and Terraform reports the resource created
  either way, so confirm it once in the SNS console rather than assuming a green
  apply means alerts arrive. And these alarms watch the *instance*, not the app:
  a crash-looping api container behind a healthy EC2 host fires nothing.
  Application logs are slog to Docker logs (capped at 3 × 10 MB per container);
  `docker compose logs -f` over SSM when you need them.
- **Nothing on the AWS side can see the database.** Neon publishes no
  CloudWatch metrics, so storage and the compute-hour budget — the line that
  actually binds on the free plan — are visible only in the Neon console. Set
  usage alerts there; there is no way to fold them in here.
- **Nothing checks that the nightly backup ran, unless you opt in.** The timer
  logs to `journalctl` on the box and that is all, so a failed dump — or, worse,
  a timer that never fired — is silent until you need it. Setting
  `BACKUP_HEARTBEAT_URL` in the `.env` turns that around: the script pings it
  after a verified upload, and an external monitor alerts on the *absence* of
  the ping, which is the only signal that catches a run that never happened.
  Without it, `systemctl list-timers concertfinder-backup` and an occasional
  `aws s3 ls` are the answer.
- **The backup has never been restored.** Time one restore into a scratch Neon
  branch; the elapsed time is the real RTO, and `ENCRYPTION_KEY` needs to be
  escrowed somewhere that is not SSM — every dump of `users` is AES-GCM
  ciphertext, so losing that one parameter makes all of them unrecoverable.
- **No secrets manager.** The `.env` file on the box holds credentials. If
  the box is compromised, so are the creds. AWS Secrets Manager costs
  $0.40/mo per secret; migrate later if you care.
- **No blue/green.** Deploy briefly stops-and-restarts the api container.
  Real downtime is 2–5 seconds. Acceptable for this scale.
