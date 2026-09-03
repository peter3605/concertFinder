variable "region" {
  description = "AWS region for all resources."
  type        = string
  default     = "us-east-1"
}

variable "domain" {
  description = "Apex domain (e.g. concertfinder.app). SES verifies this domain and Caddy issues certificates for it. DNS is NOT managed here — the records to publish come out of `terraform output dns_records`."
  type        = string
}

variable "ses_dns_records_created" {
  description = "Set true only after the records from `terraform output dns_records` exist in DNS. While false, Terraform skips the SES verification wait — necessary because the verification token does not exist until after the first apply, so an ungated wait would block the very apply that produces the record it is waiting for."
  type        = bool
  default     = false
}

variable "github_repo" {
  description = "GitHub repo permitted to assume the deploy role via OIDC."
  type        = string
  default     = "peter3605/concertFinder"
}

variable "github_branch" {
  description = "Branch permitted to trigger deploys (main only in prod)."
  type        = string
  default     = "main"
}

variable "ec2_instance_type" {
  description = "EC2 instance type. t4g.small is free under a standalone AWS trial (750 hrs/mo) that ends 2026-12-31 — not tied to account age, and separate from the free tier. After that it is ~$12/mo."
  type        = string
  default     = "t4g.small"
}

# Postgres is Neon, not RDS, and is not managed by this Terraform at all —
# there is no Neon provider configured here on purpose. The database is created
# once in the Neon console and its connection string is pasted into
# /opt/concertfinder/.env like every other secret. See docs/aws-deploy.md §1.
# What this Terraform *does* own for the database is the backup bucket below,
# because Neon's free-plan restore window is far shorter than the 7-day RDS
# retention it replaces.

variable "subnet_id" {
  description = "Subnet the app instance lives in, e.g. subnet-0123456789abcdef0. Set this to the subnet the CURRENT instance is already in. Leave empty and it falls back to the first ID of the default VPC's subnet list, which is a *set* — an unordered one, so the value at index 0 can change with no change to this config, and a changed subnet_id forces Terraform to REPLACE the instance: /opt/concertfinder, the rendered .env and the docker volumes all go with it. Find the value with: aws ec2 describe-instances --filters Name=tag:Name,Values=concertfinder --query 'Reservations[].Instances[].SubnetId' --output text"
  type        = string
  default     = ""
}

variable "backup_retention_days" {
  description = "How long nightly pg_dump artifacts live in S3 before the lifecycle rule expires them. Covers the gap left by Neon's short free-plan restore history."
  type        = number
  default     = 30
}

variable "ses_notification_local_part" {
  description = "Local part of the SES from-address (e.g. 'notifications' for notifications@<domain>)."
  type        = string
  default     = "notifications"
}

variable "ses_verified_recipient" {
  description = "Email address to verify in SES sandbox for initial testing (typically the operator's personal address)."
  type        = string
}

variable "alert_email" {
  description = "Where CloudWatch alarm notifications go. Leave empty to reuse ses_verified_recipient, which is the same address CONTACT_EMAIL already uses. The subscription is confirmed by clicking a link in a mail AWS sends on the first apply; until that is done the topic accepts publishes and delivers nothing."
  type        = string
  default     = ""
}

variable "billing_alarm_threshold_usd" {
  description = "Estimated monthly AWS charges above which the billing alarm fires. Dropping RDS takes ~$14/mo off the bill, so this is lower than it was; bump it when the t4g.small trial ends on 2026-12-31."
  type        = number
  default     = 15
}

variable "email_delivery_mode" {
  description = "How the app sends mail: 'log' writes to slog instead of sending (safe while SES is unverified or in sandbox), 'smtp' actually delivers via SES. Kept a variable rather than hardcoded so flipping it is a Terraform apply, not a hand-edit on the box."
  type        = string
  default     = "log"

  validation {
    condition     = contains(["log", "smtp"], var.email_delivery_mode)
    error_message = "email_delivery_mode must be \"log\" or \"smtp\"."
  }
}

# --- iOS client (docs/ios-app-plan.md Appendix A) -------------------------
#
# All optional. Left at their defaults the deployment serves the web app
# unchanged: mobile login is refused, apple-app-site-association 404s, and the
# push worker no-ops. What must not happen is a partial APNs set, which wires
# up successfully and then drops every notification silently --
# config.Validate rejects that at startup.

variable "ios_bundle_id" {
  description = "iOS app bundle identifier, e.g. com.example.concertfinder. Empty disables the mobile auth flow and push."
  type        = string
  default     = ""
}

variable "ios_team_id" {
  description = "Apple Developer team ID. Combined with ios_bundle_id to form IOS_APP_ID for apple-app-site-association."
  type        = string
  default     = ""
}

variable "apns_key_id" {
  description = "APNs auth key ID (the .p8's key ID). The key itself is an operator secret, set out of band."
  type        = string
  default     = ""
}

variable "apns_environment" {
  description = "Which APNs environments the .p8 auth key is authorized for. \"sandbox\", \"production\", or \"sandbox,production\" for a key issued as Sandbox & Production. Not a choice of host -- the server routes each device to its own."
  type        = string
  default     = "production"

  validation {
    # A list, because an auth key issued as "Sandbox & Production" can serve
    # both and a deployment holding one should: that is what lets a single
    # server reach TestFlight builds and a developer's debug build at once,
    # instead of a flip that silently cut off whichever half was not selected.
    condition = length(setsubtract(
      [for e in split(",", var.apns_environment) : trimspace(e)],
      ["sandbox", "production"]
    )) == 0
    error_message = "apns_environment must be \"sandbox\", \"production\", or \"sandbox,production\" -- it names what the key is authorized for, and a device in an unserved environment is skipped."
  }
}

variable "min_ios_build" {
  description = "Oldest iOS build the server supports, returned by /api/site-info. 0 means no floor."
  type        = number
  default     = 0
}

# Break-glass SSH. Empty by default, which is the deployed state: port 22 is
# closed and the key pair in ec2.tf is attached but unreachable. SSM Session
# Manager is the access path (`aws ssm start-session --target <id>`), and it is
# the one the deploy already depends on.
#
# This variable exists because the alternative to a documented lever is an
# undocumented emergency: the failure this covers is SSM itself being down or
# the SSM agent being wedged, and that is the worst moment to be discovering
# that the security group has no rule and the key has never been tried. Set it
# to ["<your-ip>/32"], apply, do the work, set it back to [] and apply again.
#
# A list rather than a bool: an emergency rule scoped to one address is a
# different thing from 0.0.0.0/0, and the type is what keeps the difference
# visible in the diff. Never leave a value in here between incidents — an
# always-open port 22 on a box whose only other ingress is Caddy is the largest
# attack surface this account has.
variable "ssh_ingress_cidrs" {
  description = "CIDRs allowed to reach port 22 on the app instance. Empty (the default and the intended steady state) means no SSH ingress at all; use SSM Session Manager. Set to your own address for break-glass access, then set it back."
  type        = list(string)
  default     = []

  validation {
    condition     = !contains(var.ssh_ingress_cidrs, "0.0.0.0/0")
    error_message = "Refusing to open port 22 to the whole internet. Use a /32, or use SSM."
  }
}
