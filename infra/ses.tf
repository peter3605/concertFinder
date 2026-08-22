# DNS for this domain lives in Cloudflare, which is also the registrar — so
# there is no Route 53 hosted zone and no NS delegation step. Terraform creates
# the SES identities and *emits* the records to publish; you create them in
# Cloudflare. `terraform output dns_records` renders the full list.
#
# The trade is that the records are not managed as code. Managing them would
# mean the Cloudflare provider plus an API token, which is a bigger dependency
# than five records that change approximately never.

resource "aws_ses_domain_identity" "main" {
  domain = var.domain
}

resource "aws_ses_domain_dkim" "main" {
  domain = aws_ses_domain_identity.main.domain
}

# Custom MAIL FROM subdomain so bounces come back to us cleanly.
resource "aws_ses_domain_mail_from" "main" {
  domain           = aws_ses_domain_identity.main.domain
  mail_from_domain = "mail.${var.domain}"
}

# Blocks until AWS observes the verification TXT record in DNS.
#
# Gated on a variable because the records it waits for are created by hand,
# and the token to put in them does not exist until *after* this file's
# aws_ses_domain_identity is created. Ungated, the very first `terraform apply`
# would create the identity and then sit waiting for a record that cannot exist
# yet, failing the whole apply on timeout. So:
#
#   1. apply with this false — creates the identities, emits `dns_records`
#   2. publish those records in Cloudflare (seconds; Cloudflare is
#      authoritative for the domain already, so there is nothing to propagate)
#   3. set ses_dns_records_created = true and apply again
#
# The timeout is short for the same reason: with authoritative DNS updated
# in-place there is no propagation delay to wait out, so ten minutes failing is
# more useful than forty-five minutes hanging.
resource "aws_ses_domain_identity_verification" "main" {
  count  = var.ses_dns_records_created ? 1 : 0
  domain = aws_ses_domain_identity.main.id

  timeouts {
    create = "10m"
  }
}

# Sandbox-mode recipient verification. Until you exit sandbox, SES only sends
# to addresses that have clicked the verification link. Add more here as
# testers come on board, or open an AWS support ticket to leave sandbox.
resource "aws_ses_email_identity" "test_recipient" {
  email = var.ses_verified_recipient
}
