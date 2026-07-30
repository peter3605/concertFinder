# Minimum-viable alarms per design §10.3 / §11. Not wired to a topic — the
# alarm state is visible in the CloudWatch console; add SNS + email
# subscription later if this gets noisier than "check once a week".

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

  dimensions = {
    InstanceId = aws_instance.app.id
  }
}

resource "aws_cloudwatch_metric_alarm" "rds_free_storage_low" {
  alarm_name          = "concertfinder-rds-free-storage-low"
  alarm_description   = "RDS free storage has dropped below the configured threshold."
  namespace           = "AWS/RDS"
  metric_name         = "FreeStorageSpace"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 3
  threshold           = var.rds_storage_alarm_threshold_gb * 1024 * 1024 * 1024
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    DBInstanceIdentifier = aws_db_instance.main.identifier
  }
}

# Billing alarm. Year-2 free-tier expiry can quietly balloon costs; this
# fires if total estimated monthly charges cross var.billing_alarm_threshold_usd.
# The EstimatedCharges metric is only published in us-east-1 regardless of
# your service region — provider alias declared inline for that reason.
provider "aws" {
  alias  = "billing"
  region = "us-east-1"
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

  dimensions = {
    Currency = "USD"
  }
}
