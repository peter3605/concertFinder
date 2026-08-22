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

output "rds_host" {
  description = "Postgres host for DATABASE_URL. Port is 5432."
  value       = aws_db_instance.main.address
}

output "rds_password" {
  description = "Generated Postgres password. Paste into DATABASE_URL in /opt/concertfinder/.env on the box."
  value       = random_password.rds.result
  sensitive   = true
}

output "database_url_template" {
  description = "Complete DATABASE_URL to paste into /opt/concertfinder/.env. Password not shown — retrieve with 'terraform output -raw rds_password'."
  value       = "postgres://concertfinder:<password>@${aws_db_instance.main.address}:${aws_db_instance.main.port}/concertfinder?sslmode=require"
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
