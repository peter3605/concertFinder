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

# The subnet is not a cosmetic attribute: changing it forces a *replacement*,
# which destroys /opt/concertfinder, the .env render-env.sh wrote from SSM, and
# the caddy volumes holding the issued certificates. `data.aws_subnets.default`
# returns a set, and a set has no order -- index 0 can come back as a different
# subnet with nothing in this config having changed, turning a routine plan
# into a rebuild of the box. Pin the real one in terraform.tfvars.
#
# The fallback to the old lookup exists because terraform.tfvars is gitignored
# and there is a working copy in use: making this variable required would break
# that copy on its next plan. Same pattern, and the same reason, as
# var.alert_email.
locals {
  subnet_id = var.subnet_id != "" ? var.subnet_id : data.aws_subnets.default.ids[0]
}

resource "aws_instance" "app" {
  ami                    = data.aws_ami.al2023_arm64.id
  instance_type          = var.ec2_instance_type
  key_name               = aws_key_pair.breakglass.key_name
  iam_instance_profile   = aws_iam_instance_profile.ec2.name
  vpc_security_group_ids = [aws_security_group.ec2.id]
  subnet_id              = local.subnet_id

  # IMDSv2-only, stated rather than assumed. AL2023 launches default to it, but
  # a default is a property of the launch and not of this config -- nothing in a
  # plan would show it changing, so "we're fine, it's the default" is a belief
  # this file cannot back up. It matters because this instance's role reads
  # every parameter under /concertfinder/*, i.e. every secret the app has: with
  # IMDSv1 answering, any SSRF that can issue a plain GET to 169.254.169.254
  # walks out with role credentials. Requiring the PUT-issued token closes that.
  #
  # hop_limit 1 keeps the response on the host, which is where the only
  # consumer is -- render-env.sh curls IMDS for the region, and it runs on the
  # box, not in a container. Nothing in /internal talks to AWS at all.
  # http_endpoint stays explicitly enabled for that same render-env.sh call.
  metadata_options {
    http_tokens                 = "required"
    http_endpoint               = "enabled"
    http_put_response_hop_limit = 1
  }

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

    # ignore_changes covers exactly the two attributes it names. This covers
    # the rest: any other force-replacement (subnet_id is the one that has
    # actually drifted on its own, but a mistyped `-var`, a stale state, or a
    # `terraform destroy` aimed at the wrong thing all land the same way) takes
    # /opt/concertfinder, the .env rendered from SSM and the caddy certificate
    # volumes with it. None of that is in this state or in any backup -- the
    # nightly pg_dump covers the database, and the database is not on this box.
    # prevent_destroy turns it into a plan-time error naming the resource.
    #
    # The cost is that a deliberate rebuild is now two steps: delete this line
    # for the one apply, or `terraform state rm aws_instance.app` and re-import
    # if the instance is fine and only the state needs moving. That friction is
    # the point -- and a plan refusing here is a reason to read *why* it wants
    # to replace, not to reach straight for the override. See infra/README.md.
    prevent_destroy = true
  }
}

resource "aws_eip" "app" {
  instance = aws_instance.app.id
  domain   = "vpc"
}
