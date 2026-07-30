output "ec2_instance_id" {
  description = "Paste this into the EC2_INSTANCE_ID GitHub Actions secret."
  value       = aws_instance.app.id
}

output "ec2_public_ip" {
  description = "Elastic IP attached to the app instance."
  value       = aws_eip.app.public_ip
}

output "route53_name_servers" {
  description = "Set these NS records at your domain registrar to delegate the zone to Route 53."
  value       = aws_route53_zone.main.name_servers
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
