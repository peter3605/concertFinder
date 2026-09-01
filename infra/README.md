# Infrastructure (Terraform)

Terraform config for the ConcertFinder Phase 3 AWS deployment. Produces the
single-instance EC2 + Caddy + SES setup described in `docs/design.md` §11. This
replaces the manual walkthrough in `docs/aws-deploy.md`, which is kept only as a
fallback / reference.

**Postgres is not managed here.** It is a Neon project created once in the Neon
console, and its connection string is pasted into `/opt/concertfinder/.env` like
every other secret — see `docs/aws-deploy.md` §1, which also covers why the
free plan's compute-hour budget (not its storage cap) is the constraint that
binds. Keeping it out of state has a side benefit: the plaintext database
password is no longer sitting in `terraform.tfstate`. What this Terraform *does*
own for the database is the S3 bucket its nightly `pg_dump` lands in.

**DNS is not managed here.** The domain is registered at Cloudflare, which is
therefore already authoritative for it, so there is no Route 53 hosted zone and
no NS delegation. Terraform emits the records to publish; you create them in
the Cloudflare dashboard. See "DNS records" below.

## What this creates

- **EC2** t4g.small (Amazon Linux 2023 ARM64) with Docker + Docker Compose
  bootstrapped via user_data. Elastic IP attached.
- **Security groups** — `ec2-sg` open on 80/443 to the internet, all outbound.
  There is no `rds-sg`: the database is Neon, reached outbound over the public
  internet on 5432 with TLS. Port 22 is deliberately closed — access is via SSM.
- **S3** a private, versioned backup bucket for the nightly `pg_dump`
  (`scripts/backup-db.sh`), with a lifecycle rule expiring dumps after
  `backup_retention_days`. The instance role gets `s3:PutObject` and nothing
  else — no read, no delete, so a compromised box can neither exfiltrate old
  dumps of the encrypted Spotify refresh tokens nor erase the history.
- **IAM** — GitHub OIDC provider + deploy role scoped to
  `peter3605/concertFinder:refs/heads/main`; EC2 instance profile with
  `AmazonSSMManagedInstanceCore` plus the backup-bucket write policy; SES SMTP
  IAM user restricted to sending from your verified from-address.
- **SES** verified domain identity, DKIM, custom MAIL FROM subdomain, and a
  sandbox-verified recipient (for initial testing). The DNS records these
  need are emitted as the `dns_records` output, not created.
- **CloudWatch** two alarms: EC2 status check failed, and estimated monthly
  billing over threshold. Neither is wired to an SNS topic — alarm state is
  visible in the console only, so nothing pages you. There is deliberately no
  database alarm: Neon publishes no CloudWatch metrics, so its storage and
  compute-hour headroom can only be alerted on from the Neon console.

Deliberately not included (see design §11.3 for triggers to add them):
ALB, ECS Fargate, CloudFront/S3, Secrets Manager, SNS topics on alarms.

## Prerequisites

- Terraform ≥ 1.6.
- AWS CLI configured with admin credentials for your account
  (`aws configure`). The provider reads standard AWS SDK env vars / profiles.
- A registered apex domain whose DNS you control. Registering at Cloudflare
  and leaving DNS there is the assumed setup: the registrar is already the
  authoritative nameserver, so records go live in seconds and there is nothing
  to delegate. Any DNS host works — you just publish the same records.

## If the OIDC provider already exists in your account

`terraform apply` will fail on `aws_iam_openid_connect_provider.github` if
some other Terraform stack or manual `aws iam create-open-id-connect-provider`
call in this account has already created one for GitHub Actions. AWS only
allows one per URL. To adopt the existing provider instead of creating a new
one:

```bash
# Find the ARN
aws iam list-open-id-connect-providers

# Import it into this state
terraform import aws_iam_openid_connect_provider.github \
  arn:aws:iam::<ACCOUNT_ID>:oidc-provider/token.actions.githubusercontent.com
```

## First-time apply — two passes

The SES verification token does not exist until the identity is created, so
the record proving the domain cannot be published before the first apply. The
`ses_dns_records_created` variable makes that ordering explicit instead of
leaving it as a trap: while it is false, Terraform skips the verification wait.

**Pass 1 — build everything, collect the DNS records.**

```bash
cd infra
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars: set `domain` and `ses_verified_recipient`.
# Leave ses_dns_records_created at its default (false).

terraform init
terraform plan   -out plan.tfplan
terraform apply  plan.tfplan
terraform output dns_records
```

This takes 2–3 minutes now that there is no RDS instance to wait on.

**Pass 2 — publish DNS, then prove it.**

Create the six records from `dns_records` in Cloudflare (see below), then:

```bash
terraform apply -var ses_dns_records_created=true
```

That pass does nothing except wait for AWS to observe the verification TXT,
which should be near-instant. If it times out after ten minutes, the record is
wrong — check for a trailing dot or a proxied CNAME rather than re-running.

## DNS records

`terraform output dns_records` renders all six with their purpose:

| Type | Name | Purpose |
|---|---|---|
| A | `<domain>` | apex → the Elastic IP |
| TXT | `_amazonses.<domain>` | SES domain verification |
| CNAME ×3 | `<token>._domainkey.<domain>` | SES DKIM |
| MX | `mail.<domain>` | SES MAIL FROM bounces (priority 10) |
| TXT | `mail.<domain>` | SPF for MAIL FROM |

**Every one of these must be "DNS only" (grey cloud).** Cloudflare's proxy
answers CNAME lookups with its own addresses, so a proxied DKIM record makes
SES report the domain unverified with nothing explaining why, and a proxied MX
cannot receive mail at all.

That includes the apex `A` record, at least at first. Proxying it is
attractive — free CDN, DDoS protection, and the SPA bundle served from an edge
near the user instead of from Virginia — but it breaks the `/api/auth` rate
limiter as currently written. `Caddyfile` overwrites `True-Client-IP`,
`X-Real-IP` and `X-Forwarded-For` with `{remote_host}`, which behind the proxy
is a *Cloudflare edge address*: every client in the world collapses into a
handful of buckets, and one abusive user rate-limits everyone. Turning the
proxy on safely means reading `CF-Connecting-IP` instead **and** restricting
the EC2 security group to Cloudflare's published IP ranges, so the origin
cannot be reached directly and the header cannot be spoofed. Do that as its own
change, after the deploy works.

### After the first apply

1. **Verify the SES sandbox recipient.** Check the inbox for
   `ses_verified_recipient` and click the AWS verification link. Until this
   is done, SES will refuse to send to that address.
2. **Confirm DKIM.** The verification TXT and the DKIM CNAMEs are checked
   separately, and pass 2 only waits for the former. SES console → verified
   identities → your domain → DKIM should read "Successful" before you rely
   on production deliveries.
3. **Save GitHub secrets.** In repo Settings → Secrets and variables →
   Actions, set:
   - `AWS_DEPLOY_ROLE_ARN` = `terraform output -raw github_deploy_role_arn`
   - `EC2_INSTANCE_ID` = `terraform output -raw ec2_instance_id`
4. **Populate `/opt/concertfinder/.env` on the box.** Connect via SSM
   (EC2 → Instances → Connect → Session Manager), then:

   ```
   sudo -u concertfinder tee /opt/concertfinder/.env <<'ENV'
   # From the Neon console — the DIRECT endpoint, not the pooled one. River
   # picks jobs up via LISTEN/NOTIFY and Neon's pooled endpoint is PgBouncer in
   # transaction mode, which does not support it and does not say so.
   DATABASE_URL=postgres://neondb_owner:<pw>@ep-xxxx.us-east-1.aws.neon.tech/neondb?sslmode=require
   BACKUP_S3_BUCKET=<terraform output backup_bucket>
   ENCRYPTION_KEY=<openssl rand -hex 32>
   SPOTIFY_CLIENT_ID=<from developer.spotify.com>
   # Must end in /api/auth/callback — that is where the handler is mounted.
   # Any other path lands on the SPA catch-all and login completes into a
   # logged-out page; config.Validate rejects it outright.
   SPOTIFY_REDIRECT_URI=https://your-domain.com/api/auth/callback
   TICKETMASTER_API_KEY=<from developer.ticketmaster.com>
   SESSION_COOKIE_DOMAIN=your-domain.com
   LISTEN_ADDR=:8080
   # Read by Caddy, not by the Go binary. Bare host, no scheme. Must match the
   # host in SITE_BASE_URL below.
   SITE_DOMAIN=your-domain.com
   # NOT optional in production despite having a default: unsubscribe links in
   # outgoing mail and the MusicBrainz/Nominatim User-Agent are built from it.
   # Left unset it falls back to https://127.0.0.1:3000 and the server refuses
   # to start behind a real SESSION_COOKIE_DOMAIN.
   SITE_BASE_URL=https://your-domain.com
   CONTACT_EMAIL=you@your-domain.com
   USER_LATITUDE=40.7128
   USER_LONGITUDE=-74.0060
   USER_RADIUS_MILES=50
   # Stays in 'log' mode (nothing is sent) until SES is out of sandbox.
   EMAIL_DELIVERY_MODE=log
   SMTP_HOST=<paste ses_smtp_endpoint>
   SMTP_PORT=587
   SMTP_USERNAME=<paste ses_smtp_username>
   SMTP_PASSWORD=<paste ses_smtp_password>
   # Must be exactly `terraform output ses_from_address`. The SES IAM policy
   # conditions on ses:FromAddress, so any other local part is denied at send
   # time rather than rejected here.
   SMTP_FROM=<paste ses_from_address>
   ENV
   sudo chmod 600 /opt/concertfinder/.env
   sudo chown concertfinder:concertfinder /opt/concertfinder/.env
   ```

   The server validates all of this at startup and reports every problem at
   once, so a bad `.env` costs one round trip rather than one restart per
   variable. `docker compose logs api` shows them.

   Retrieve sensitive outputs with:
   `terraform output -raw ses_smtp_username`,
   `terraform output -raw ses_smtp_password`.
   The database password is not among them — it comes from the Neon console.
5. **Clone the repo on the box.**

   ```
   sudo -u concertfinder git clone https://github.com/peter3605/concertFinder.git /opt/concertfinder
   ```
6. **First deploy.** Either push an empty commit to trigger GitHub Actions
   (`git commit --allow-empty -m "chore: first deploy" && git push`), or run
   the compose stack manually over SSM:

   ```
   sudo -u concertfinder bash -c 'cd /opt/concertfinder \
     && docker compose -f docker-compose.prod.yml build \
     && docker compose -f docker-compose.prod.yml up -d --wait --wait-timeout 240 \
     && ./scripts/verify-deploy.sh'
   ```

   Keep `build` and `up` as separate commands: `up -d --build` tears running
   containers down as part of the same command, so a build that fails or runs
   the 2 GB box out of memory takes the site with it. `--wait` blocks on the
   api container's healthcheck, and `verify-deploy.sh` then fetches
   `/api/healthz` through Caddy — without both, `up -d` returns 0 the moment a
   container is *started*, so a container that exits on a bad `.env` and
   crash-loops looks exactly like a successful deploy.

## State management

State is **local**, in `infra/terraform.tfstate` (gitignored). Contains
plaintext SES SMTP creds and the break-glass private key, so back it up
somewhere secure (1Password vault, encrypted external drive). It no longer
contains a database password — that moved to the Neon console when RDS went
away. If this ever grows to multiple contributors, migrate to an S3 backend +
DynamoDB lock table.

## Break-glass SSH

Terraform writes a private key to `infra/.secrets/concertfinder-breakglass.pem`
(gitignored, mode 0600). Never used in normal operation — SSM is the primary
admin channel. Only touch this if SSM Agent on the box has broken and you
need to fix it before it can be replaced.

## Common operations

**Rotate the database password:** not a Terraform operation any more. Reset the
role's password in the Neon console, then update `DATABASE_URL` in
`/opt/concertfinder/.env` and restart the api container:
`docker compose -f docker-compose.prod.yml up -d api`.

**Restore from a backup:** see `docs/aws-deploy.md` §7. The instance can only
write to the bucket, so restores run from your laptop with admin credentials.

**Pin `subnet_id`, once, on an existing deployment.** The instance used to take
its subnet from `data.aws_subnets.default.ids[0]`. That is index 0 of a *set*,
and a set is unordered — the value can come back different with nothing in this
config having changed. `subnet_id` forces replacement, so the consequence is
not a diff, it is Terraform destroying the box: `/opt/concertfinder`, the `.env`
`render-env.sh` rendered from SSM, and the caddy volumes holding the issued
certificates. Set `subnet_id` in `terraform.tfvars` to the subnet the instance
is *already* in:

```bash
aws ec2 describe-instances --filters Name=tag:Name,Values=concertfinder \
  --query 'Reservations[].Instances[].SubnetId' --output text
```

Left empty it falls back to the old lookup, so an existing gitignored
`terraform.tfvars` keeps working — but that is the state this exists to get out
of, not a supported configuration.

**The instance carries `lifecycle { prevent_destroy = true }`.** It is the
backstop for the paragraph above: any plan that would replace or destroy
`aws_instance.app` now fails at plan time naming the resource, instead of
succeeding. `ignore_changes` covers only `user_data` and `ami`; everything else
that forces replacement is caught here.

The trade is that a *deliberate* rebuild no longer just works. Two ways
through, both intentional friction:

- Comment the `prevent_destroy` line out for the single apply that rebuilds,
  then put it back. Simplest, and it leaves a diff someone has to write.
- `terraform state rm aws_instance.app`, make the change, then
  `terraform import aws_instance.app <instance-id>` — for the case where the
  real instance is fine and only the state's idea of it needs moving.

If a plan is refusing with "Instance cannot be destroyed", read *why* it wants
to replace before reaching for either. On this config the likely answer is a
drifting `subnet_id`, which wants pinning, not overriding.

**Add more SES-verified recipients (while in sandbox):**
Edit `ses.tf`, add another `aws_ses_email_identity` resource. Re-apply.
Or exit sandbox with an AWS support ticket and skip this.

**Tear it all down:**
```bash
# The backup bucket is versioned and destroy will not remove a non-empty
# bucket. Empty it first — and read the next paragraph before you do.
aws s3 rm "s3://$(terraform output -raw backup_bucket)" --recursive
terraform destroy
```

This will now stop on `aws_instance.app`: `prevent_destroy` does not
distinguish an accidental replacement from a teardown you meant. Remove the
`lifecycle` block's `prevent_destroy` line in `ec2.tf` first, and treat having
to do so as the last chance to notice you are destroying production.

`destroy` does not touch the database. Neon is outside this state entirely, so
the project and its data survive — delete it in the Neon console separately, and
note that emptying the backup bucket above throws away the only copy of the data
this Terraform ever had. `destroy` does not touch DNS either — those records
live in Cloudflare, outside this state. Delete them by hand, or the apex `A` record will point at an Elastic IP
that AWS has since reassigned to somebody else.
