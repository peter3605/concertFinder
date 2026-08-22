variable "region" {
  description = "AWS region for all resources."
  type        = string
  default     = "us-east-1"
}

variable "domain" {
  description = "Apex domain (e.g. concertfinder.app). Route 53 hosted zone is created for this; SES verifies this domain; Caddy issues certs for it."
  type        = string
}

variable "github_repo" {
  description = "GitHub repo permitted to assume the deploy role via OIDC."
  type        = string
  default     = "peter3605/concertFinder"
}

variable "github_branch" {
  description = "Branch permitted to trigger deploys (main only in prod)."
  type        = string
  default     = "main"
}

variable "ec2_instance_type" {
  description = "EC2 instance type. t4g.small is free under a standalone AWS trial (750 hrs/mo) that ends 2026-12-31 — not tied to account age, and separate from the free tier. After that it is ~$12/mo."
  type        = string
  default     = "t4g.small"
}

variable "rds_instance_class" {
  description = "RDS instance class. db.t4g.micro is free for 12 months ONLY on accounts created before 2025-07-15; newer accounts get credits instead and this bills at ~$12/mo plus storage."
  type        = string
  default     = "db.t4g.micro"
}

variable "rds_allocated_storage_gb" {
  description = "RDS storage in GiB. The legacy free tier included 20 GiB; on a post-2025-07-15 account this bills at the gp3 rate."
  type        = number
  default     = 20
}

variable "rds_backup_retention_days" {
  description = "RDS backup retention window."
  type        = number
  default     = 7
}

variable "ses_notification_local_part" {
  description = "Local part of the SES from-address (e.g. 'notifications' for notifications@<domain>)."
  type        = string
  default     = "notifications"
}

variable "ses_verified_recipient" {
  description = "Email address to verify in SES sandbox for initial testing (typically the operator's personal address)."
  type        = string
}

variable "rds_storage_alarm_threshold_gb" {
  description = "CloudWatch alarm fires when RDS free storage drops below this many GiB."
  type        = number
  default     = 5
}

variable "billing_alarm_threshold_usd" {
  description = "Estimated monthly AWS charges above which the billing alarm fires. Free-tier headroom is generous in year 1; bump this to something like 40 for year 2."
  type        = number
  default     = 25
}
