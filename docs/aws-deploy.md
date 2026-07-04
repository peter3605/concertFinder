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
Ticketmaster key, Bandsintown ID, RDS `DATABASE_URL`, encryption key,
`SITE_DOMAIN` for Caddy). Use `/etc/environment`-style syntax; owner
`concertfinder`, mode `600`. Example:

```
DATABASE_URL=postgres://concertfinder:<rds-password>@concertfinder.xxxxx.us-east-1.rds.amazonaws.com:5432/concertfinder?sslmode=require
ENCRYPTION_KEY=<openssl rand -hex 32>
SPOTIFY_CLIENT_ID=<from developer.spotify.com>
SPOTIFY_REDIRECT_URI=https://your-domain.com/callback
TICKETMASTER_API_KEY=<from developer.ticketmaster.com>
BANDSINTOWN_APP_ID=concertfinder-prod
SESSION_COOKIE_DOMAIN=your-domain.com
LISTEN_ADDR=:8080
SITE_DOMAIN=your-domain.com
USER_LATITUDE=40.7128
USER_LONGITUDE=-74.0060
USER_RADIUS_MILES=50
```

Kick the first deploy manually to confirm it boots:

```
sudo -u concertfinder bash -c 'cd /opt/concertfinder && docker compose -f docker-compose.prod.yml up -d --build'
```

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
- Update Spotify Developer Dashboard: redirect URI → `https://your-domain.com/callback`.

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

The workflow just runs `git pull && docker compose up -d --build` on the
instance, so a bad deploy is fixed by:

```
git revert <bad-commit> && git push
```

Or manually via SSM:

```
sudo -u concertfinder bash -c 'cd /opt/concertfinder && git reset --hard <good-sha> && docker compose -f docker-compose.prod.yml up -d --build'
```

## What's not in this setup

Deliberately kept out to keep the year-1 bill at ~$16:

- **No load balancer.** Traffic hits EC2 directly; Caddy terminates TLS.
  ALB would add $16/mo.
- **No auto-scaling.** Single-instance; if it dies, restart it. Fine for
  personal-project scale.
- **No CloudWatch dashboards / alarms.** Slog output goes to Docker logs;
  `docker compose logs -f` over SSM when you need it.
- **No secrets manager.** The `.env` file on the box holds credentials. If
  the box is compromised, so are the creds. AWS Secrets Manager costs
  $0.40/mo per secret; migrate later if you care.
- **No blue/green.** Deploy briefly stops-and-restarts the api container.
  Real downtime is 2–5 seconds. Acceptable for this scale.
