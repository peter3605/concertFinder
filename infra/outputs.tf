output "ec2_instance_id" {
  description = "Paste this into the EC2_INSTANCE_ID GitHub Actions secret."
  value       = aws_instance.app.id
}

output "ec2_public_ip" {
  description = "Elastic IP attached to the app instance."
  value       = aws_eip.app.public_ip
}

# Every DNS record this deployment needs, ready to publish in Cloudflare.
# Rendered rather than managed: DNS lives with the registrar, not in this
# state. `terraform output -json dns_records` if you want to script it.
#
# The proxy column is not advisory. Cloudflare's proxy answers CNAME lookups
# with its own addresses, which makes DKIM verification fail and MX delivery
# impossible — SES would report the domain unverified with nothing explaining
# why. Only the apex A record is a candidate for proxying, and even that should
# stay "DNS only" until the origin is taught to read CF-Connecting-IP (see
# infra/README.md), because behind the proxy every client shares a handful of
# Cloudflare edge IPs and the /api/auth rate limiter buckets them together.
output "dns_records" {
  description = "DNS records to create in Cloudflare. All must be 'DNS only' (grey cloud)."
  value = concat(
    [
      {
        type    = "A"
        name    = var.domain
        value   = aws_eip.app.public_ip
        proxy   = "DNS only"
        purpose = "apex -> EC2 instance"
      },
      {
        type    = "TXT"
        name    = "_amazonses.${var.domain}"
        value   = aws_ses_domain_identity.main.verification_token
        proxy   = "n/a"
        purpose = "SES domain verification"
      },
    ],
    [for token in aws_ses_domain_dkim.main.dkim_tokens : {
      type    = "CNAME"
      name    = "${token}._domainkey.${var.domain}"
      value   = "${token}.dkim.amazonses.com"
      proxy   = "DNS only"
      purpose = "SES DKIM"
    }],
    [
      {
        type    = "MX"
        name    = aws_ses_domain_mail_from.main.mail_from_domain
        value   = "feedback-smtp.${var.region}.amazonses.com (priority 10)"
        proxy   = "n/a"
        purpose = "SES MAIL FROM bounce handling"
      },
      {
        type    = "TXT"
        name    = aws_ses_domain_mail_from.main.mail_from_domain
        value   = "v=spf1 include:amazonses.com ~all"
        proxy   = "n/a"
        purpose = "SPF for the MAIL FROM subdomain"
      },
    ]
  )
}

# No database outputs. Postgres is Neon and its connection string comes from
# the Neon console, not from this state — which is a small win on its own: the
# plaintext database password is no longer sitting in terraform.tfstate.
#
# Copy the **direct** (unpooled) connection string, not the pooled one. River
# uses LISTEN/NOTIFY, and Neon's pooled endpoint is PgBouncer in transaction
# mode, which silently does not support it: job pickup degrades to the 1s
# FetchPollInterval fallback and a leader resignation takes ~5s to notice, with
# no error anywhere. Keep `?sslmode=require` on the end — it is what replaces
# the `rds.force_ssl` parameter group this deployment used to carry.

output "backup_bucket" {
  description = "S3 bucket the nightly pg_dump lands in. Paste into BACKUP_S3_BUCKET in /opt/concertfinder/.env."
  value       = aws_s3_bucket.backups.id
}

output "github_deploy_role_arn" {
  description = "Paste this into the AWS_DEPLOY_ROLE_ARN GitHub Actions secret."
  value       = aws_iam_role.github_deploy.arn
}

output "ses_smtp_endpoint" {
  description = "SMTP server hostname for SES in this region. Port 587 (STARTTLS)."
  value       = "email-smtp.${var.region}.amazonaws.com"
}

output "ses_smtp_username" {
  description = "SES SMTP username (the IAM access key ID). Paste into /opt/concertfinder/.env as SMTP_USERNAME."
  value       = aws_iam_access_key.ses_smtp.id
  sensitive   = true
}

output "ses_smtp_password" {
  description = "SES SMTP password (derived from the secret access key). Paste into /opt/concertfinder/.env as SMTP_PASSWORD."
  value       = aws_iam_access_key.ses_smtp.ses_smtp_password_v4
  sensitive   = true
}

output "ses_from_address" {
  description = "Verified from-address for outgoing digest emails."
  value       = "${var.ses_notification_local_part}@${var.domain}"
}

output "breakglass_private_key_path" {
  description = "Local path to the SSH private key. Only used if SSM ever becomes unreachable."
  value       = local_sensitive_file.breakglass_private_key.filename
}

# The alarm topics. An email subscription sits in "PendingConfirmation" until
# the recipient clicks the link AWS mails on creation, and Terraform reports
# the resource created either way — so a green apply proves the topic exists,
# not that anything will ever arrive. Check these in the SNS console once.
output "alerts_topic_arn" {
  description = "SNS topic for the EC2 status-check alarms. Confirm the email subscription once, by hand."
  value       = aws_sns_topic.alerts.arn
}

output "billing_alerts_topic_arn" {
  description = "SNS topic for the billing alarm. Separate because the EstimatedCharges metric is only published in us-east-1, and an alarm can only publish to a topic in its own region."
  value       = aws_sns_topic.billing_alerts.arn
}
