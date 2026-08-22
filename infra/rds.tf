resource "random_password" "rds" {
  length  = 32
  special = true
  # Postgres accepts most special chars but keep it URL-safe for DATABASE_URL.
  override_special = "!@#%^*_-=+"
}

resource "aws_db_subnet_group" "main" {
  name       = "concertfinder-db-subnets"
  subnet_ids = data.aws_subnets.default.ids
}

resource "aws_db_parameter_group" "postgres16" {
  name   = "concertfinder-pg16"
  family = "postgres16"

  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
}

resource "aws_db_instance" "main" {
  identifier     = "concertfinder"
  engine         = "postgres"
  engine_version = "16"
  instance_class = var.rds_instance_class

  allocated_storage     = var.rds_allocated_storage_gb
  max_allocated_storage = 0 # disable autoscaling on free tier
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name  = "concertfinder"
  username = "concertfinder"
  password = random_password.rds.result
  port     = 5432

  db_subnet_group_name   = aws_db_subnet_group.main.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  parameter_group_name   = aws_db_parameter_group.postgres16.name
  publicly_accessible    = false

  backup_retention_period   = var.rds_backup_retention_days
  skip_final_snapshot       = false
  final_snapshot_identifier = "concertfinder-final-snapshot"

  auto_minor_version_upgrade = true
  deletion_protection        = true

  performance_insights_enabled = false # free tier friendliness
}
