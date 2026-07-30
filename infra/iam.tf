# GitHub Actions OIDC provider — one per AWS account. Reuses the existing
# provider if you've already created it manually (see aws-deploy.md §4a).
resource "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"

  client_id_list = ["sts.amazonaws.com"]

  # Thumbprints for GitHub's OIDC provider. AWS actually ignores these for
  # github.com since ~2023 but the field is still required.
  thumbprint_list = [
    "6938fd4d98bab03faadb97b34396831e3780aea1",
    "1c58a3a8518e8759bf075b76b750d4f2df264fcd",
  ]
}

data "aws_iam_policy_document" "github_deploy_trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    # Pin to this repo + branch only. Any other ref is rejected.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${var.github_repo}:ref:refs/heads/${var.github_branch}"]
    }
  }
}

resource "aws_iam_role" "github_deploy" {
  name               = "GitHubActionsConcertFinderDeploy"
  assume_role_policy = data.aws_iam_policy_document.github_deploy_trust.json
}

data "aws_iam_policy_document" "github_deploy_permissions" {
  # Just enough to run one SSM command against the app instance and read its
  # output. No IAM, no S3, no EC2 write.
  statement {
    effect = "Allow"
    actions = [
      "ssm:SendCommand",
      "ssm:ListCommands",
      "ssm:GetCommandInvocation",
    ]
    resources = [
      aws_instance.app.arn,
      "arn:aws:ssm:${data.aws_region.current.name}::document/AWS-RunShellScript",
      "arn:aws:ssm:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:*",
    ]
  }
}

resource "aws_iam_role_policy" "github_deploy" {
  name   = "Deploy"
  role   = aws_iam_role.github_deploy.id
  policy = data.aws_iam_policy_document.github_deploy_permissions.json
}

# SES SMTP user. Its access key becomes the SMTP username; the SES-specific
# password derives from the secret access key via a documented HMAC transform,
# which the provider computes for us as ses_smtp_password_v4.
resource "aws_iam_user" "ses_smtp" {
  name = "concertfinder-ses-smtp"
}

data "aws_iam_policy_document" "ses_send" {
  statement {
    effect = "Allow"
    actions = [
      "ses:SendRawEmail",
      "ses:SendEmail",
    ]
    resources = ["*"]

    # Restrict to sending only from our domain.
    condition {
      test     = "StringEquals"
      variable = "ses:FromAddress"
      values   = ["${var.ses_notification_local_part}@${var.domain}"]
    }
  }
}

resource "aws_iam_user_policy" "ses_smtp" {
  name   = "SendConcertFinderEmails"
  user   = aws_iam_user.ses_smtp.name
  policy = data.aws_iam_policy_document.ses_send.json
}

resource "aws_iam_access_key" "ses_smtp" {
  user = aws_iam_user.ses_smtp.name
}
