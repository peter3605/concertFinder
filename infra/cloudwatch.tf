# Minimum-viable alarms per design §10.3 / §11.
#
# These used to have no `alarm_actions` at all, which made every one of them a
# console-only state change: an alarm nobody is subscribed to is a record that
# something went wrong, not a notification that it did. The topic below is what
# turns them into the second thing.
#
# Four alarms. Three watch AWS-side resources; the fourth watches the
# application, and it is the only one here that could not be built out of a
# metric AWS publishes on its own. The database is not among them: see the note
# below the EC2 alarms.

# The address is the operator's, and it is the same one CONTACT_EMAIL already
# carries — an alert that arrives somewhere nobody reads is the failure mode
# this whole file exists to avoid, so it defaults to a known-good address
# rather than to empty.
locals {
  alert_email = var.alert_email != "" ? var.alert_email : var.ses_verified_recipient

  # Shared with scripts/watchdog.sh, which publishes into it. The two strings
  # must agree and nothing at apply time can tell that they do -- a metric
  # published into an unwatched namespace produces an alarm with no data, which
  # reads as whatever treat_missing_data says rather than as an error.
  # scripts/check-deploy-config.sh compares them, the way it pins PG_IMAGE
  # across the backup and restore-drill scripts.
  app_metric_namespace = "ConcertFinder/App"
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

# The application alarm. Everything above watches the instance, and the gap
# that leaves is not a corner case -- it is the failure this deployment
# actually has. `restart: unless-stopped` means a container that exits on
# every start crash-loops forever, and from the host's point of view nothing
# is wrong: both EC2 status checks pass, the instance is up, and the box is
# busy rather than broken. config.Validate exits hard on a bad .env by design,
# so one wrong variable produced exactly that -- a crash-looping api container,
# an SSM "Success" and a green workflow, with the site down throughout.
#
# scripts/verify-deploy.sh catches it at deploy time. Nothing caught it after,
# which is where an OOM kill, a rotated credential or a suspended Neon compute
# land. scripts/watchdog.sh is the minute-by-minute half, and this is what
# turns its number into mail.
#
# treat_missing_data = "breaching" is the load-bearing line. The watchdog runs
# on the box it is watching, so the states that stop it reporting are the same
# states worth reporting: a wedged docker daemon, a disabled timer, a rebuilt
# instance that never got the unit, an IAM change that silently revoked
# PutMetricData. Treating absent data as healthy would make the monitoring's
# own failure the one thing it cannot report -- which is the bug this alarm
# exists to close, reproduced one level up. The EC2 status-check alarm above
# made the same choice for the same reason -- note its *system* counterpart did
# not, because a flapping system check triggers an ec2:recover and a recovery
# for a blip costs a reboot to fix nothing. Absent data means different things
# in the two cases, which is why the setting is per alarm rather than a default.
# The cost here is that tearing the watchdog down deliberately mails somebody;
# that is the correct direction to be wrong in.
#
# Three consecutive minutes rather than one. A deploy recreates both
# containers, and the api's healthcheck start_period is 60s -- the watchdog
# already declines to count a container that is still inside it, so this is
# slack for the recreate itself rather than for the startup. It also means a
# single dropped datapoint cannot alarm on its own.
resource "aws_cloudwatch_metric_alarm" "app_services_unhealthy" {
  alarm_name          = "concertfinder-app-services-unhealthy"
  alarm_description   = "One or more containers in docker-compose.prod.yml have been down, unhealthy or crash-looping for three minutes -- or the watchdog that reports on them has stopped. SSM to the box and run `docker compose -f docker-compose.prod.yml ps` and `journalctl -u concertfinder-watchdog.service -n 50`."
  namespace           = local.app_metric_namespace
  metric_name         = "ServicesUnhealthy"
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 3
  datapoints_to_alarm = 3
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "breaching"

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]

  dimensions = {
    InstanceId = aws_instance.app.id
  }
}

# The instance can publish this one namespace and nothing else. PutMetricData
# takes no resource ARN -- "*" is the only accepted value -- so the namespace
# condition is the entire scope of this grant, and without it the role could
# write into AWS/EC2 and friends and corrupt the metrics the alarms above read.
data "aws_iam_policy_document" "publish_app_metrics" {
  statement {
    effect    = "Allow"
    actions   = ["cloudwatch:PutMetricData"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "cloudwatch:namespace"
      values   = [local.app_metric_namespace]
    }
  }
}

resource "aws_iam_role_policy" "ec2_publish_app_metrics" {
  name   = "PublishAppMetrics"
  role   = aws_iam_role.ec2.id
  policy = data.aws_iam_policy_document.publish_app_metrics.json
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
