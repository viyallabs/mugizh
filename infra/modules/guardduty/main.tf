# GuardDuty threat detection in the provider's region. Instantiate once per India region
# (ap-south-1 and ap-south-2). Findings stay in the region (R3). The detector is a free-standing
# security control: do not add a switch that turns it off (CLAUDE.md section 5 rule 5 in spirit).

resource "aws_guardduty_detector" "this" {
  enable                       = true
  finding_publishing_frequency = "FIFTEEN_MINUTES"
}

resource "aws_guardduty_detector_feature" "s3" {
  detector_id = aws_guardduty_detector.this.id
  name        = "S3_DATA_EVENTS"
  status      = "ENABLED"
}

resource "aws_guardduty_detector_feature" "rds" {
  detector_id = aws_guardduty_detector.this.id
  name        = "RDS_LOGIN_EVENTS"
  status      = "ENABLED"
}

output "detector_id" { value = aws_guardduty_detector.this.id }
