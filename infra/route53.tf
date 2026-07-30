resource "aws_route53_zone" "main" {
  name    = var.domain
  comment = "concertfinder apex"
}

resource "aws_route53_record" "apex" {
  zone_id = aws_route53_zone.main.zone_id
  name    = var.domain
  type    = "A"
  ttl     = 300
  records = [aws_eip.app.public_ip]
}
