# AWS deploy (EC2 + RDS, free tier)

One-time setup for pushing to `main` → automatic deploy on an EC2 t4g.small +
RDS db.t4g.micro in us-east-1. Everything below is free tier for the first
12 months of the AWS account, then ~$18/mo (EC2 $5 + RDS $13) starting
year 2. The single ongoing cost that's not free is Route 53 (~$0.50/mo) and
the domain itself (~$10/yr at Cloudflare or Porkbun).

Do the setup steps once, in order. After that, `git push origin main`
deploys automatically via the workflow in `.github/workflows/deploy.yml`.

## 0. Prerequisites

- New AWS account (12-month free tier active)
- A domain you control (needed for Spotify's redirect URI — Spotify rejects
  `nip.io`, `duckdns.org`, and other free dynamic-DNS domains for OAuth).
- AWS CLI installed and logged in as an admin locally (only for setup).
- GitHub repo pushed (`git@github.com:peter3605/concertFinder.git`).

## 1. RDS: managed Postgres

AWS Console → RDS → Create database.

- Engine: **PostgreSQL 16**
- Templates: **Free tier**
- Instance identifier: `concertfinder`
- Master username: `concertfinder`
- Master password: generate one and stash in a password manager
- Instance class: `db.t4g.micro` (free tier eligible)
- Storage: 20 GiB gp3
- Public access: **No**
- VPC security group: create new, name it `concertfinder-rds-sg`
- Initial database name: `concertfinder`
- Backup retention: 7 days (free tier limit)

Note the endpoint hostname after creation:
`concertfinder.xxxxxxxxxxxx.us-east-1.rds.amazonaws.com`.

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

After the instance is running, edit `concertfinder-rds-sg` (from step 1):
add an inbound rule allowing `TCP 5432` from `concertfinder-ec2-sg`. This
makes RDS reachable from the EC2 box only.

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
Ticketmaster key, RDS `DATABASE_URL`, encryption key,
`SITE_DOMAIN` for Caddy). Use `/etc/environment`-style syntax; owner
`concertfinder`, mode `600`. Example:

```
DATABASE_URL=postgres://concertfinder:<rds-password>@concertfinder.xxxxx.us-east-1.rds.amazonaws.com:5432/concertfinder?sslmode=require
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
SMTP_FROM=ConcertFinder <notify@your-domain.com>
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
- Route 53 → Create hosted zone for your domain. Cost: $0.50/mo.
- In the domain registrar's control panel, change the nameservers to the
  four Route 53 NS records.
- Route 53 → Hosted zone → Create record:
  - Type A, name `@` (apex), value your EC2 Elastic IP
- Update Spotify Developer Dashboard: redirect URI → `https://your-domain.com/api/auth/callback`.
  It must be this exact path. The handler is mounted at `/api/auth/callback`
  (chi: `/api` → `/auth` → `auth.Mount`), and a redirect to `/callback`
  falls through to the SPA's catch-all — so the browser lands on the app
  looking logged out, with no error anywhere to say the URI was wrong.

Caddy handles the TLS cert automatically the first time a request lands on
port 443 for your domain.

## 7. First automated deploy

```
git commit --allow-empty -m "chore: trigger first CI deploy"
git push origin main
```

Watch the Actions tab. The `test` job runs, then `deploy` fires off an SSM
command against your instance; you'll see stdout printed in the workflow
output when it finishes.

## Ongoing cost

| Item | Year 1 (free tier) | Year 2+ |
|---|---|---|
| EC2 t4g.small | $0 (750 hrs/mo free) | ~$5/mo |
| RDS db.t4g.micro | $0 (750 hrs/mo free) | ~$13/mo |
| RDS storage 20 GiB | $0 (20 GiB free) | ~$3/mo |
| Route 53 hosted zone | ~$0.50/mo | ~$0.50/mo |
| Data transfer | $0 (100 GB out free/mo) | free at low usage |
| Domain | ~$10/yr | ~$10/yr |
| **Total** | **~$16 (domain + Route 53)** | **~$22/mo** |

## Rolling back

The workflow resets the instance to `origin/main`, builds, brings containers
up with `--wait`, and then runs `scripts/verify-deploy.sh`. The normal rollback
is therefore just:

```
git revert <bad-commit> && git push
```

Or manually via SSM. **Keep `build` and `up` as separate commands** — `up -d
--build` tears the running containers down as part of the same command, so a
build that fails or runs the 2 GB box out of memory takes the site with it.
That is the last thing you want during a rollback, which is by definition a
moment when the site is already unhappy:

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
- **No CloudWatch dashboards, and no alerting on the alarms that do exist.**
  `infra/cloudwatch.tf` defines three metric alarms (EC2 status check, RDS free
  storage, estimated billing), but none of them is wired to an SNS topic — the
  state is visible in the CloudWatch console and nowhere else. Nothing pages
  you. Application logs are slog to Docker logs; `docker compose logs -f` over
  SSM when you need them.
- **No secrets manager.** The `.env` file on the box holds credentials. If
  the box is compromised, so are the creds. AWS Secrets Manager costs
  $0.40/mo per secret; migrate later if you care.
- **No blue/green.** Deploy briefly stops-and-restarts the api container.
  Real downtime is 2–5 seconds. Acceptable for this scale.
