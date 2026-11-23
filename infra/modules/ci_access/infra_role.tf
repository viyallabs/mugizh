# Infrastructure-apply role (S-094): used only by the manual tofu-plan-<env> and tofu-apply-<env> CI jobs
# (tools/ci/tofu.sh). A separate role from the deploy role, so that a code deploy can never change
# infrastructure. Same OIDC trust as the deploy role of the environment:
#   staging    : project_path:<path>:ref_type:branch:ref:main only
#   production : the protected release tags v0* to v9* only
# AdministratorAccess is accepted ONLY because (1) the organisation SCP (infra/org: region lock R1, and the
# denies on audit and key-protection actions) is attached to the OU of this account, and (2) every
# assumption of this role raises an alert (alerts.tf). Both are mandatory preconditions: apply infra/org
# first (infra/README.md, bootstrap step 2). [CONFIRM] with the founder; narrow the policy when the
# resource set is stable.
# Residual risk: anyone who can run a pipeline on the trusted ref (GitLab protection is the control, see
# docs/ops/production-readiness.md "Deploying") can obtain administrator rights in the account.

resource "aws_iam_role" "infra" {
  name                 = "${var.name}-gitlab-infra-apply"
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 3600
}

resource "aws_iam_role_policy_attachment" "infra_admin" {
  role       = aws_iam_role.infra.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

output "infra_role_arn" { value = aws_iam_role.infra.arn }
