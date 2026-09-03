# State backend bootstrap. Run ONCE, before the root module in ../ adopts the
# S3 backend. See ../backend.tf.example for the second half and
# docs/aws-deploy.md, "Terraform state", for the whole procedure.
#
# Why this is a separate root module rather than a resource in ../: a backend
# cannot store its own state in a bucket it has not created yet. Terraform has
# no ordering for that. So this module creates the bucket and keeps its own
# state LOCAL — permanently, on purpose. That is safe here in a way it is not
# in ../, because nothing this module manages has a secret in it: it is one
# bucket and its settings. The state that matters is the one in ../, which
# holds the SES SMTP password (ses.tf) and the break-glass ED25519 private key
# (ec2.tf) in cleartext, sitting in a git checkout on a laptop.
#
# Losing this module's local state is a non-event: `terraform import` the bucket
# back, or delete this directory and manage the bucket by hand. Losing ../'s is
# an afternoon of imports against live infrastructure.

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.60"
    }
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "concertfinder"
      Environment = "prod"
      ManagedBy   = "terraform"
    }
  }
}

data "aws_caller_identity" "current" {}

variable "region" {
  description = "Region for the state bucket. Keep it the same as the root module's region — a backend in another region is one more thing to be wrong about during an incident."
  type        = string
  default     = "us-east-1"
}

# Account-scoped name, matching the backups bucket. S3 bucket names are globally
# unique across every AWS account, so an unqualified "concertfinder-tfstate" is
# a name someone else may already hold — and the failure arrives at `apply` as a
# 409 with no explanation of why.
resource "aws_s3_bucket" "state" {
  bucket = "concertfinder-tfstate-${data.aws_caller_identity.current.account_id}"

  # This bucket is the reason the root module can be recovered at all. An
  # accidental `terraform destroy` in this directory would take it, and with it
  # every other module's ability to know what it owns.
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Versioning is not optional for a state bucket, and not for the usual reason.
# A state file is overwritten on every apply, so the previous version IS the
# backup: a half-written state, a mid-apply crash, or a `terraform state rm`
# that took too much are all recovered by fetching the prior object version.
# Without this there is nothing to fetch.
resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id

  versioning_configuration {
    status = "Enabled"
  }
}

# Noncurrent versions expire after 90 days rather than never. Long enough that
# an old state is still there when someone works out what went wrong last
# month; bounded, so this is not an unbounded bill for a file that changes on
# every apply.
resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    id     = "expire-old-state-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 90
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 3
    }
  }

  depends_on = [aws_s3_bucket_versioning.state]
}

# Deny any request that is not over TLS. The bucket holds an SMTP password and
# a private key; "it is private, so the transport does not matter" is exactly
# the assumption that makes an accidental public read worse than it needed to
# be. This costs nothing — every AWS SDK and Terraform itself use HTTPS.
data "aws_iam_policy_document" "state_tls_only" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.state.arn, "${aws_s3_bucket.state.arn}/*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = data.aws_iam_policy_document.state_tls_only.json

  # Order matters: attaching a policy before public access is blocked leaves a
  # window, and BlockPublicPolicy can reject a policy attached after it in the
  # same apply if Terraform picks the wrong order.
  depends_on = [aws_s3_bucket_public_access_block.state]
}

output "state_bucket" {
  description = "Bucket name to put in ../backend.tf's `bucket =` line."
  value       = aws_s3_bucket.state.id
}
