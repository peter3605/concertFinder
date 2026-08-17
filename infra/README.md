# Infrastructure (Terraform)

Terraform config for the ConcertFinder Phase 3 AWS deployment. Produces the
single-instance EC2 + RDS + Caddy + SES + Route 53 setup described in
`docs/design.md` §11. This replaces the manual walkthrough in
`docs/aws-deploy.md`, which is kept only as a fallback / reference.

## What this creates

- **RDS** PostgreSQL 16 on `db.t4g.micro`, private, encrypted, `force_ssl=1`.
- **EC2** t4g.small (Amazon Linux 2023 ARM64) with Docker + Docker Compose
  bootstrapped via user_data. Elastic IP attached.
- **Security groups** — `ec2-sg` open on 80/443 to the internet; `rds-sg`
  open on 5432 only from `ec2-sg`. Port 22 is deliberately closed on the
  EC2 SG — access is via SSM.
- **IAM** — GitHub OIDC provider + deploy role scoped to
  `peter3605/concertFinder:refs/heads/main`; EC2 instance profile with
  `AmazonSSMManagedInstanceCore`; SES SMTP IAM user restricted to sending
  from your verified from-address.
- **Route 53** hosted zone for your domain, apex `A` record → Elastic IP,
  plus DKIM CNAMEs, SES verification TXT, MAIL FROM MX + SPF for SES.
- **SES** verified domain identity, DKIM, custom MAIL FROM subdomain, and a
  sandbox-verified recipient (for initial testing).
- **CloudWatch** three alarms: EC2 status check failed, RDS free storage low,
  and estimated monthly billing over threshold. None is wired to an SNS topic
  — alarm state is visible in the console only, so nothing pages you.

Deliberately not included (see design §11.3 for triggers to add them):
ALB, ECS Fargate, CloudFront/S3, Secrets Manager, SNS topics on alarms.

## Prerequisites

- Terraform ≥ 1.6.
- AWS CLI configured with admin credentials for your account
  (`aws configure`). The provider reads standard AWS SDK env vars / profiles.
- A registered apex domain (Cloudflare Registrar / Porkbun / anywhere).
  Terraform creates the Route 53 hosted zone; you delegate to it after apply.

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

## First-time apply

```bash
cd infra
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars: set `domain` and `ses_verified_recipient`.

terraform init
terraform plan   -out plan.tfplan
terraform apply  plan.tfplan
```

The first apply takes 10–15 minutes because RDS creation is slow.

### After the first apply

1. **Delegate the domain.** `terraform output route53_name_servers` gives you
   four NS records. At your domain registrar, set the domain's nameservers
   to those four values. Propagation is usually under an hour.
2. **Verify the SES sandbox recipient.** Check the inbox for
   `ses_verified_recipient` and click the AWS verification link. Until this
   is done, SES will refuse to send to that address.
3. **Wait for DKIM verification.** Terraform's
   `aws_ses_domain_identity_verification` blocks apply until the domain
   verification TXT propagates, but DKIM records take another few minutes
   after that. Check SES console → verified identities → your domain →
   DKIM: should read "Successful" before you rely on prod deliveries.
4. **Save GitHub secrets.** In repo Settings → Secrets and variables →
   Actions, set:
   - `AWS_DEPLOY_ROLE_ARN` = `terraform output -raw github_deploy_role_arn`
   - `EC2_INSTANCE_ID` = `terraform output -raw ec2_instance_id`
5. **Populate `/opt/concertfinder/.env` on the box.** Connect via SSM
   (EC2 → Instances → Connect → Session Manager), then:

   ```
   sudo -u concertfinder tee /opt/concertfinder/.env <<'ENV'
   DATABASE_URL=postgres://concertfinder:<paste rds_password>@<paste rds host>:5432/concertfinder?sslmode=require
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
   SMTP_HOST=<paste ses_smtp_endpoint>
   SMTP_PORT=587
   SMTP_USERNAME=<paste ses_smtp_username>
   SMTP_PASSWORD=<paste ses_smtp_password>
   SMTP_FROM=<paste ses_from_address>
   ENV
   sudo chmod 600 /opt/concertfinder/.env
   sudo chown concertfinder:concertfinder /opt/concertfinder/.env
   ```

   Retrieve sensitive outputs with:
   `terraform output -raw rds_password`,
   `terraform output -raw ses_smtp_username`,
   `terraform output -raw ses_smtp_password`.
6. **Clone the repo on the box.**

   ```
   sudo -u concertfinder git clone https://github.com/peter3605/concertFinder.git /opt/concertfinder
   ```
7. **First deploy.** Either push an empty commit to trigger GitHub Actions
   (`git commit --allow-empty -m "chore: first deploy" && git push`), or run
   the compose stack manually over SSM:

   ```
   sudo -u concertfinder bash -c 'cd /opt/concertfinder && docker compose -f docker-compose.prod.yml up -d --build'
   ```

## State management

State is **local**, in `infra/terraform.tfstate` (gitignored). Contains
plaintext RDS password and SES SMTP creds, so back it up somewhere secure
(1Password vault, encrypted external drive). If this ever grows to multiple
contributors, migrate to an S3 backend + DynamoDB lock table.

## Break-glass SSH

Terraform writes a private key to `infra/.secrets/concertfinder-breakglass.pem`
(gitignored, mode 0600). Never used in normal operation — SSM is the primary
admin channel. Only touch this if SSM Agent on the box has broken and you
need to fix it before it can be replaced.

## Common operations

**Rotate the RDS password:**
```bash
terraform taint random_password.rds
terraform apply
# Then update DATABASE_URL in /opt/concertfinder/.env and restart the api
# container: docker compose -f docker-compose.prod.yml up -d api
```

**Add more SES-verified recipients (while in sandbox):**
Edit `ses.tf`, add another `aws_ses_email_identity` resource. Re-apply.
Or exit sandbox with an AWS support ticket and skip this.

**Tear it all down:**
```bash
# RDS has deletion_protection = true; disable first.
terraform apply -var-file=terraform.tfvars \
  -replace=aws_db_instance.main # then edit the resource to set deletion_protection=false
terraform destroy
```

Route 53 hosted zone charges are billed even when empty; the destroy will
remove it.
