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

    # buildx has to come from the same place as compose, and this is not
    # optional: `docker compose build` is a thin wrapper over buildx, and
    # compose v5 refuses to run against buildx older than 0.17.0
    # ("compose build requires buildx 0.17.0 or later"). AL2023's docker
    # package ships 0.12.1 in /usr/libexec/docker/cli-plugins, so installing
    # compose from `latest` while leaving buildx to the distro pairs a 2025
    # compose with a 2024 builder. That combination fails at the *build* step
    # of a deploy -- after `git reset --hard` has already moved the checkout,
    # and with an error that names buildx while nothing in this file mentions
    # it.
    #
    # /usr/local/lib/docker/cli-plugins takes precedence over /usr/libexec,
    # so this shadows the distro copy rather than fighting the package manager.
    #
    # buildx release assets embed the version in the filename, so unlike
    # compose there is no version-less `latest/download/` URL to grab; the tag
    # has to be resolved first.
    #
    # The API response is captured into a variable rather than piped straight
    # into grep. `grep -m1` exits at the first match and closes the pipe, curl
    # then dies writing to it ("curl: (23) Failure writing output"), and under
    # `set -o pipefail` that fails the whole bootstrap -- for a command that
    # actually succeeded.
    BUILDX_JSON=$(curl -fsSL https://api.github.com/repos/docker/buildx/releases/latest)
    BUILDX_VER=$(printf '%s' "$BUILDX_JSON" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
    curl -fsSL "https://github.com/docker/buildx/releases/download/$BUILDX_VER/buildx-$BUILDX_VER.linux-arm64" \
      -o $DOCKER_CONFIG/cli-plugins/docker-buildx
    chmod +x $DOCKER_CONFIG/cli-plugins/docker-buildx

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

    # The nightly backup timer. This used to exist only as copy-paste in
    # docs/aws-deploy.md §7, which meant a rebuilt instance came up with no
    # backups and nothing anywhere said so -- the failure is invisible until
    # the night someone goes looking for a dump that was never taken. It
    # belongs with the rest of the bootstrap for the same reason the swapfile
    # does.
    #
    # Enabling the timer does not require /opt/concertfinder/scripts to exist
    # yet: the clone in aws-deploy.md §3 lands well before the first 03:00
    # firing, and a systemd timer does not validate its service's ExecStart
    # until it triggers.
    cat > /etc/systemd/system/concertfinder-backup.service <<'UNIT'
    [Unit]
    Description=Nightly pg_dump of the ConcertFinder database to S3
    After=docker.service
    Requires=docker.service

    [Service]
    Type=oneshot
    User=concertfinder
    ExecStart=/opt/concertfinder/scripts/backup-db.sh
    UNIT

    cat > /etc/systemd/system/concertfinder-backup.timer <<'UNIT'
    [Unit]
    Description=Run the ConcertFinder database backup nightly

    [Timer]
    # 03:00 UTC -- clear of every DAILY_*_HOUR_UTC job (affinity 06, scan 07,
    # digest 09, janitor 10), so the dump is not competing with a scan for the
    # 0.25 CU Neon compute.
    OnCalendar=*-*-* 03:00:00 UTC
    Persistent=true

    [Install]
    WantedBy=timers.target
    UNIT

    # The heredocs above are indented to match this file; systemd unit files
    # tolerate leading whitespace on directives but not on section headers, so
    # strip it rather than trusting that.
    sed -i 's/^[[:space:]]*//' /etc/systemd/system/concertfinder-backup.service \
      /etc/systemd/system/concertfinder-backup.timer

    systemctl daemon-reload
    systemctl enable --now concertfinder-backup.timer
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
