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
