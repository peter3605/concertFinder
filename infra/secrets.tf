# Application configuration and secrets, in SSM Parameter Store.
#
# The problem this solves: `.env` lived only on the instance, so changing any
# value meant either an interactive session on the box or an SSM send-command
# carrying the value in its parameters -- and send-command parameters are
# retained ~30 days and readable by anyone with SSM access. That is how a
# database password, a Ticketmaster key and an SMTP password leaked into
# command history during the initial deploy. Parameter Store inverts the
# direction: the instance *fetches*, so no secret ever appears in a command.
#
# Parameter Store rather than Secrets Manager: identical KMS encryption and
# IAM model, free at the standard tier instead of $0.40/secret/month, and
# Secrets Manager's one real advantage -- scheduled rotation -- only drives
# AWS-native databases. It cannot rotate a Neon password, a Spotify key, or an
# SES credential, which is all of these.
#
# Two populations, deliberately handled differently:
#
#   - Config (String, values here): tunables and anything Terraform can derive
#     from the domain or from other resources. Managed in git, changed by
#     editing this file and applying.
#   - Secrets (SecureString, placeholder + ignore_changes): Terraform creates
#     the parameter but never learns the value. Set with
#     `aws ssm put-parameter --value file://...`, which keeps it out of shell
#     history, and CloudTrail redacts SecureString values.
#
# scripts/render-env.sh renders both into /opt/concertfinder/.env at deploy
# time, and refuses to write the file if any secret still holds its
# placeholder -- otherwise a forgotten value reaches the container as the
# literal string "REPLACE_ME" and config.Validate's message would be about
# format, not about the parameter nobody set.

locals {
  # Everything Terraform can work out for itself. Deriving these rather than
  # asking an operator to retype them is what keeps SITE_DOMAIN and
  # SITE_BASE_URL from disagreeing -- a mismatch that provisions a certificate
  # for one name while every emailed link points at another, and which
  # config.Validate exists to reject at startup.
  derived_config = {
    SITE_DOMAIN           = var.domain
    SITE_BASE_URL         = "https://${var.domain}"
    SESSION_COOKIE_DOMAIN = var.domain
    SPOTIFY_REDIRECT_URI  = "https://${var.domain}/api/auth/callback"
    SMTP_HOST             = "email-smtp.${var.region}.amazonaws.com"
    SMTP_FROM             = "${var.ses_notification_local_part}@${var.domain}"
    BACKUP_S3_BUCKET      = aws_s3_bucket.backups.id
  }

  # Tunables. Defaults mirror .env.example; the comments there explain why each
  # number is what it is. Changing one is an edit here plus an apply, so the
  # value and its history stay in git rather than in somebody's shell.
  tunable_config = {
    LISTEN_ADDR = ":8080"

    # Fallback location for users with no saved row. The frontend asks the
    # browser on first login, so this only applies to someone who declines.
    USER_LATITUDE     = "38.8951"
    USER_LONGITUDE    = "-77.0364"
    USER_RADIUS_MILES = "50"

    PHASE2_FALLBACKS_ENABLED       = "true"
    PHASE2_MIN_SCORE               = "2.0"
    PHASE2_FALLBACK_BUDGET_SECONDS = "120"
    PHASE2_FALLBACK_CONCURRENCY    = "1"

    SNAPSHOT_STALE_AFTER_HOURS = "6"
    CONCERT_CACHE_TTL_HOURS    = "12"

    DAILY_AFFINITY_HOUR_UTC = "6"
    DAILY_SCAN_HOUR_UTC     = "7"
    DAILY_DIGEST_HOUR_UTC   = "9"
    DAILY_JANITOR_HOUR_UTC  = "10"

    # Sized for a COLD scan: TM resolution is two-stage, so a first-ever scan
    # costs ~2 calls per artist against 200 artists. See .env.example.
    RATE_CAP_TM_PER_USER_DAILY       = "500"
    RATE_CAP_SONGKICK_PER_USER_DAILY = "100"

    EMAIL_DELIVERY_MODE = var.email_delivery_mode
    SMTP_PORT           = "587"
    CONTACT_EMAIL       = var.ses_verified_recipient
  }

  # Operator-supplied. Terraform creates the parameter and never reads it.
  #
  # SPOTIFY_CLIENT_ID is not actually secret -- PKCE exists so public clients
  # need no secret -- but it is operator-supplied and travels the same path,
  # and a uniform mechanism beats a special case that someone has to remember.
  operator_secrets = [
    "DATABASE_URL",
    "ENCRYPTION_KEY",
    "SPOTIFY_CLIENT_ID",
    "TICKETMASTER_API_KEY",
    "SONGKICK_API_KEY",
    "BRAVE_SEARCH_API_KEY",
  ]

  # Sentinel written at create time. render-env.sh treats it as fatal.
  secret_placeholder = "REPLACE_ME"
}

resource "aws_ssm_parameter" "config" {
  for_each = merge(local.derived_config, local.tunable_config)

  name  = "/concertfinder/${each.key}"
  type  = "String"
  value = each.value
  tier  = "Standard"
}

resource "aws_ssm_parameter" "secret" {
  for_each = toset(local.operator_secrets)

  name  = "/concertfinder/${each.key}"
  type  = "SecureString"
  value = local.secret_placeholder
  tier  = "Standard"

  lifecycle {
    # The whole point: Terraform provisions the slot, the operator fills it,
    # and no apply ever reads the value back or resets it. Without this every
    # plan would show a diff against REPLACE_ME and eventually overwrite a
    # live credential with the placeholder.
    ignore_changes = [value]
  }
}

# SES SMTP credentials are the exception: Terraform already owns them, so
# asking an operator to copy them by hand would be inventing a manual step and
# a chance to paste the wrong one. They are already in tfstate either way.
resource "aws_ssm_parameter" "smtp_username" {
  name  = "/concertfinder/SMTP_USERNAME"
  type  = "SecureString"
  value = aws_iam_access_key.ses_smtp.id
  tier  = "Standard"
}

resource "aws_ssm_parameter" "smtp_password" {
  name  = "/concertfinder/SMTP_PASSWORD"
  type  = "SecureString"
  value = aws_iam_access_key.ses_smtp.ses_smtp_password_v4
  tier  = "Standard"
}

# The instance may read this path and decrypt it, and nothing else. No write:
# a compromised box must not be able to rewrite the config it boots from, and
# updates are an operator action from outside.
data "aws_iam_policy_document" "read_parameters" {
  statement {
    effect = "Allow"
    actions = [
      "ssm:GetParameter",
      "ssm:GetParameters",
      "ssm:GetParametersByPath",
    ]
    resources = [
      "arn:aws:ssm:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:parameter/concertfinder/*",
    ]
  }

  # SecureString is KMS-encrypted; reading with --with-decryption needs this.
  # Scoped to the account's default SSM key, which is what aws_ssm_parameter
  # uses when no key_id is set.
  statement {
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${data.aws_region.current.name}.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "ec2_read_parameters" {
  name   = "ReadConcertFinderParameters"
  role   = aws_iam_role.ec2.id
  policy = data.aws_iam_policy_document.read_parameters.json
}
