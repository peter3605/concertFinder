# Minimum-viable alarms per design §10.3 / §11.
#
# These used to have no `alarm_actions` at all, which made every one of them a
# console-only state change: an alarm nobody is subscribed to is a record that
# something went wrong, not a notification that it did. The topic below is what
# turns them into the second thing.
#
# Three alarms, all about AWS-side resources. The database is not one of them:
# see the note below the EC2 alarms.

# The address is the operator's, and it is the same one CONTACT_EMAIL already
# carries — an alert that arrives somewhere nobody reads is the failure mode
# this whole file exists to avoid, so it defaults to a known-good address
# rather than to empty.
locals {
  alert_email = var.alert_email != "" ? var.alert_email : var.ses_verified_recipient
}

resource "aws_sns_topic" "alerts" {
  name = "concertfinder-alerts"
}

# NOTE: an email subscription is *pending* until the recipient clicks the
# confirmation link AWS mails on creation. Terraform reports the resource as
# created either way, so a green apply is not proof that alerts are deliverable.
# Confirm it once, then check `terraform output alerts_topic_arn` in the SNS
# console shows Confirmed. See docs/aws-deploy.md §10.
resource "aws_sns_topic_subscription" "alerts_email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = local.alert_email

  # The confirmation is done by hand out of band and its timestamp comes back
  # in the state; without this every subsequent plan wants to replace the
  # subscription and start the confirmation over.
  lifecycle {
    ignore_changes = [confirmation_timeout_in_minutes]
  }
}

resource "aws_cloudwatch_metric_alarm" "ec2_status_check" {
  alarm_name          = "concertfinder-ec2-status-check-failed"
  alarm_description   = "EC2 instance status check has failed for two consecutive minutes."
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed"
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 2
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "breaching"

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]

  dimensions = {
    InstanceId = aws_instance.app.id
  }
}

# StatusCheckFailed above is the union of the instance check and the system
# check; this one isolates the system half, which is the half AWS can fix
# without us. `arn:aws:automate:<region>:ec2:recover` migrates the instance to
# healthy host hardware, keeping the instance ID, private IP, and EIP
# association. It is the one alarm here that does something rather than saying
# something, so it also notifies: a silent recovery is a restarted process and
# an empty in-memory river schedule, and that is worth knowing about.
#
# Two consecutive minutes rather than one, matching the alarm above — the
# system check flaps briefly during some host maintenance, and a recovery
# triggered by a blip costs a reboot to fix nothing.
resource "aws_cloudwatch_metric_alarm" "ec2_system_status_check" {
  alarm_name          = "concertfinder-ec2-system-status-check-failed"
  alarm_description   = "EC2 system status check has failed for two consecutive minutes; recovering the instance onto new host hardware."
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed_System"
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 2
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  alarm_actions = [
    "arn:aws:automate:${var.region}:ec2:recover",
    aws_sns_topic.alerts.arn,
  ]
  ok_actions = [aws_sns_topic.alerts.arn]

  dimensions = {
    InstanceId = aws_instance.app.id
  }
}

# There is deliberately no database alarm here any more. Postgres is Neon,
# which publishes nothing to CloudWatch — storage and compute-hour headroom are
# visible only in the Neon console, and the compute-hour budget is the line
# that actually binds (see docs/aws-deploy.md). Set the usage alerts there;
# nothing in this file can see them.

# Billing alarm. Year-2 free-tier expiry can quietly balloon costs; this
# fires if total estimated monthly charges cross var.billing_alarm_threshold_usd.
# The EstimatedCharges metric is only published in us-east-1 regardless of
# your service region — provider alias declared inline for that reason.
provider "aws" {
  alias  = "billing"
  region = "us-east-1"
}

# The alarm lives in us-east-1 because the metric does, so it needs a topic in
# us-east-1 too: an alarm can only publish to a topic in its own region. When
# var.region is already us-east-1 this is a second topic in the same region,
# which is harmless and keeps the file correct if the app ever moves.
resource "aws_sns_topic" "billing_alerts" {
  provider = aws.billing
  name     = "concertfinder-billing-alerts"
}

resource "aws_sns_topic_subscription" "billing_alerts_email" {
  provider  = aws.billing
  topic_arn = aws_sns_topic.billing_alerts.arn
  protocol  = "email"
  endpoint  = local.alert_email

  lifecycle {
    ignore_changes = [confirmation_timeout_in_minutes]
  }
}

resource "aws_cloudwatch_metric_alarm" "billing" {
  provider            = aws.billing
  alarm_name          = "concertfinder-billing"
  alarm_description   = "Estimated AWS charges exceeded threshold."
  namespace           = "AWS/Billing"
  metric_name         = "EstimatedCharges"
  statistic           = "Maximum"
  period              = 21600 # 6h — Billing metric only updates a few times a day
  evaluation_periods  = 1
  threshold           = var.billing_alarm_threshold_usd
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"

  alarm_actions = [aws_sns_topic.billing_alerts.arn]
  ok_actions    = [aws_sns_topic.billing_alerts.arn]

  dimensions = {
    Currency = "USD"
  }
}
