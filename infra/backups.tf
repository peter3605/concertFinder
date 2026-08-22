# Nightly logical backups of the Neon database, uploaded from the app instance
# by scripts/backup-db.sh (see the systemd timer in docs/aws-deploy.md §7).
#
# This exists because moving off RDS gave up `backup_retention_period = 7` and
# the final snapshot. Neon's free plan keeps a much shorter restore history,
# and most of this schema is genuinely disposable — concerts, caches, and
# snapshots all rebuild themselves on the next scan. What does not rebuild is
# `users` (the AES-GCM-encrypted Spotify refresh tokens and email prefs),
# `user_saved_concerts`, `user_subscribed_artists`, and `user_locations`.
# Losing those means every user re-authorizes with Spotify and loses their
# stars and bells, which is the one failure here worth paying to avoid.
#
# The dump is taken whole rather than table-by-table on purpose: the database
# is well under a gigabyte, and a table list in a backup script is a thing that
# silently fails to grow when a migration adds a table.

resource "aws_s3_bucket" "backups" {
  bucket = "concertfinder-backups-${data.aws_caller_identity.current.account_id}"
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket                  = aws_s3_bucket.backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Versioning is not about accidental deletes — the instance has no
# s3:DeleteObject. It is about overwrites: the dump key is dated to the day, so
# a job that runs twice (a retry, a manual invocation) overwrites that day's
# object, and a dump that failed halfway would otherwise replace a good one.
resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  # Versioning without an expiry on noncurrent versions is an unbounded bill,
  # so both halves are set here.
  rule {
    id     = "expire-dumps"
    status = "Enabled"

    filter {}

    expiration {
      days = var.backup_retention_days
    }

    noncurrent_version_expiration {
      noncurrent_days = 7
    }

    # Nothing else cleans these up, and a multipart upload that dies partway
    # (the instance rebooting mid-dump) leaves billable parts with no object.
    abort_incomplete_multipart_upload {
      days_after_initiation = 3
    }
  }

  depends_on = [aws_s3_bucket_versioning.backups]
}

# The instance can write backups and nothing else. Deliberately no GetObject
# and no DeleteObject: restoring is a rare, manual, operator-laptop operation
# done with admin credentials, and write-only means a compromised box cannot
# read back the encrypted refresh tokens in every previous night's dump or
# delete the history on its way out.
data "aws_iam_policy_document" "backups_write" {
  statement {
    effect    = "Allow"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.backups.arn}/*"]
  }
}

resource "aws_iam_role_policy" "ec2_backups" {
  name   = "WriteDatabaseBackups"
  role   = aws_iam_role.ec2.id
  policy = data.aws_iam_policy_document.backups_write.json
}
