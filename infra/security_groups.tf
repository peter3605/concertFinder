# The `description` here is deliberately left as-is even though the SSH rule
# below made it slightly incomplete. A security group's description is
# immutable in AWS, so editing that one string forces Terraform to REPLACE the
# group and re-attach the instance to a new one — a real outage window in
# exchange for a comment. The rule's own description carries the nuance.
resource "aws_security_group" "ec2" {
  name        = "concertfinder-ec2-sg"
  description = "Allow HTTP/HTTPS to the app instance. SSH is deliberately closed; use SSM."
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "HTTP (Caddy redirects to HTTPS)"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  # Break-glass SSH, off unless var.ssh_ingress_cidrs says otherwise. The
  # dynamic block produces no rule at all for the default empty list, so the
  # steady state is byte-identical to having no ingress stanza here — this is a
  # lever, not a change in posture.
  #
  # The key pair in ec2.tf has been attached to the instance since the first
  # apply and has never been usable, because nothing ever opened the port. That
  # is a fine default and a bad surprise: the moment you need it is the moment
  # SSM is unavailable, which is not when you want to be writing a security
  # group rule for the first time. See docs/aws-deploy.md, "Break-glass access".
  dynamic "ingress" {
    for_each = length(var.ssh_ingress_cidrs) > 0 ? [1] : []

    content {
      description = "Break-glass SSH (temporary; steady state is no rule at all)"
      from_port   = 22
      to_port     = 22
      protocol    = "tcp"
      cidr_blocks = var.ssh_ingress_cidrs
    }
  }

  # Postgres is no longer in this VPC — it is Neon, reached over the public
  # internet on 5432 with TLS. That traffic leaves through this rule, along
  # with Spotify/Ticketmaster/MusicBrainz/Nominatim, SES, and the nightly
  # backup upload to S3. There is no longer an `rds-sg` to pair with.
  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}
