

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 3.0"
    }
  }
}


# Configure the AWS Provider

provider "aws" {
 
  access_key = "AKIAVLC7FJ6DDIS4Q2PB"
  secret_key = "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"
}

resource "aws_s3_bucket" "backend" {
  bucket = "${var.bucket_prefix}-terraform-backend"
  acl    = "private"

  versioning {
    enabled = true
  }

  server_side_encryption_configuration {
    rule {
      apply_server_side_encryption_by_default {
        sse_algorithm = var.bucket_sse_algorithm
      }
    }
  }
}

resource "aws_s3_bucket_public_access_block" "backend" {
  bucket = aws_s3_bucket.backend.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_dynamodb_table" "lock" {
  name           = "terraform-lock-example"
  read_capacity  = 1
  write_capacity = 1
  hash_key       = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }
}

terraform {
  backend "s3" {
    bucket = "pc-example-terraform-backend"
    key = "terraform.tfstate"
    access_key = "AKIAVLC7FJ6DDIS4Q2PB"
    secret_key = "GWYSCOwdvjGku4+Ci46cegdHJf7j7N0O1EV4Rk7j"
    
  }
}
