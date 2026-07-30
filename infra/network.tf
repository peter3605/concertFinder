# Use the default VPC — one AZ, one instance, single-tenant. Splitting to a
# custom VPC is a §11.3 concern (ALB / multi-AZ / private subnets for the app).
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}
