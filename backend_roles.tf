#This role has full access to every environment
resource "aws_iam_role" "backend-all" {
  name               = "terraform-example"
  description        = "Allows access to all Terraform workspaces"
  assume_role_policy = data.aws_iam_policy_document.backend-assume-role-all.json
}

resource "aws_iam_role_policy" "backend-all" {
  name   = "terraform-example"
  policy = data.aws_iam_policy_document.iam-role-policy.json
  role   = "terraform-example"

  depends_on = [aws_iam_role.backend-all]
}

#These roles are limited to their specific workspace through the use of S3 resource permissions
 resource "aws_iam_role" "backend-restricted" {
  count       = length(var.workspaces)
  name        = "terraform-example-${element(var.workspaces, count.index)}"
  description = "Allows access to the ${element(var.workspaces, count.index)} Terraform worksapce"
  assume_role_policy = element(
    data.aws_iam_policy_document.backend-assume-role-restricted.*.json,
    count.index,
  )
}

resource "aws_iam_role_policy" "backend-restricted" {
  count = length(var.workspaces)
  name  = "terraform-example-${element(var.workspaces, count.index)}"
  policy = element(
    data.aws_iam_policy_document.iam-role-policy-restricted.*.json,
    count.index,
  )
  role = "terraform-${element(var.workspaces, count.index)}"

  depends_on = [aws_iam_role.backend-restricted]
} 
