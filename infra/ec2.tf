# Break-glass SSH key. SSM is the primary access channel; this exists so the
# instance is recoverable if SSM ever fails. Private key is written locally,
# gitignored, and never uploaded anywhere.
resource "tls_private_key" "breakglass" {
  algorithm = "ED25519"
}

resource "aws_key_pair" "breakglass" {
  key_name   = "concertfinder-breakglass"
  public_key = tls_private_key.breakglass.public_key_openssh
}

resource "local_sensitive_file" "breakglass_private_key" {
  content         = tls_private_key.breakglass.private_key_openssh
  filename        = "${path.module}/.secrets/concertfinder-breakglass.pem"
  file_permission = "0600"
}

data "aws_ami" "al2023_arm64" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-arm64"]
  }
  filter {
    name   = "architecture"
    values = ["arm64"]
  }
}

# SSM-managed instance profile — grants the box permission to be reached by
# the deploy SSM command from GitHub Actions.
resource "aws_iam_role" "ec2" {
  name = "concertfinder-ec2"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ec2_ssm" {
  role       = aws_iam_role.ec2.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "ec2" {
  name = "concertfinder-ec2"
  role = aws_iam_role.ec2.name
}

resource "aws_instance" "app" {
  ami                    = data.aws_ami.al2023_arm64.id
  instance_type          = var.ec2_instance_type
  key_name               = aws_key_pair.breakglass.key_name
  iam_instance_profile   = aws_iam_instance_profile.ec2.name
  vpc_security_group_ids = [aws_security_group.ec2.id]
  subnet_id              = data.aws_subnets.default.ids[0]

  root_block_device {
    volume_size = 20
    volume_type = "gp3"
    encrypted   = true
  }

  # Idempotent bootstrap. The heavy lifting (git clone, first deploy) still
  # lives in aws-deploy.md §3 because it needs credentials this Terraform
  # deliberately doesn't own.
  user_data = <<-EOT
    #!/bin/bash
    set -euo pipefail
    dnf update -y
    dnf install -y docker git
    systemctl enable --now docker
    usermod -aG docker ec2-user

    DOCKER_CONFIG=/usr/local/lib/docker
    mkdir -p $DOCKER_CONFIG/cli-plugins
    curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-aarch64 \
      -o $DOCKER_CONFIG/cli-plugins/docker-compose
    chmod +x $DOCKER_CONFIG/cli-plugins/docker-compose

    id -u concertfinder &>/dev/null || useradd -m -s /bin/bash concertfinder
    usermod -aG docker concertfinder
    mkdir -p /opt/concertfinder
    chown concertfinder:concertfinder /opt/concertfinder

    # Swap. The deploy builds the image on this box, and the web stage runs
    # `npm ci` + a Vite production build inside a 2 GiB instance alongside a
    # running Postgres-less but non-trivial api + Caddy. A build OOM here is
    # not a clean failure: the OOM killer takes whatever it likes, and the
    # workflow's split build/up steps only protect against the build exiting
    # non-zero, not against it dragging the box down. 2 GiB of swap makes the
    # build slow instead of fatal.
    if [ ! -f /swapfile ]; then
      dd if=/dev/zero of=/swapfile bs=1M count=2048 status=none
      chmod 600 /swapfile
      mkswap /swapfile
      swapon /swapfile
      grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
    fi
  EOT

  tags = {
    Name = "concertfinder"
  }

  lifecycle {
    # user_data changes force replacement; block that so we don't wipe /opt on
    # a bootstrap-script edit. Re-run bootstrap manually over SSM if needed.
    ignore_changes = [user_data, ami]
  }
}

resource "aws_eip" "app" {
  instance = aws_instance.app.id
  domain   = "vpc"
}
