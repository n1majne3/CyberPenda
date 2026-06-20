---
name: cloud-security-audit
description: Professional methodology and practical checks for cloud security audits.
version: 1.0.0
---

# Cloud Security Audit

## Overview

A cloud security audit evaluates the security posture of a cloud environment. This skill provides audit methods, tools, and best practices for major cloud platforms including AWS, Azure, and GCP.

## Audit Scope

### 1. Identity and access management

**Check items:**
- IAM policy configuration
- User permissions
- Role permissions
- Access key management

### 2. Network security

**Check items:**
- Security group configuration
- Network ACLs
- VPC configuration
- Traffic encryption

### 3. Data security

**Check items:**
- Data encryption
- Key management
- Backup strategy
- Data classification

### 4. Compliance

**Check items:**
- Compliance frameworks
- Audit logs
- Monitoring and alerts
- Incident response

## AWS Security Audit

### IAM audit

**Check IAM policies:**
```bash
# List all IAM users
aws iam list-users

# List all IAM policies
aws iam list-policies

# Check user permissions
aws iam list-user-policies --user-name username
aws iam list-attached-user-policies --user-name username

# Check role permissions
aws iam list-role-policies --role-name rolename
```

**Common issues:**
- Excessive permissions
- Unused access keys
- Weak password policy
- MFA is not enabled

### S3 security audit

**Check S3 buckets:**
```bash
# List all buckets
aws s3 ls

# Check bucket policy
aws s3api get-bucket-policy --bucket bucketname

# Check bucket ACL
aws s3api get-bucket-acl --bucket bucketname

# Check bucket encryption
aws s3api get-bucket-encryption --bucket bucketname
```

**Common issues:**
- Public access
- No encryption
- Versioning is not enabled
- Logging is not enabled

### Security group audit

**Check security groups:**
```bash
# List all security groups
aws ec2 describe-security-groups

# Check open ports
aws ec2 describe-security-groups --group-ids sg-xxx
```

**Common issues:**
- Open to 0.0.0.0/0
- Unnecessary open ports
- Overly permissive rules

### CloudTrail audit

**Check audit logs:**
```bash
# List all trails
aws cloudtrail describe-trails

# Check log file integrity
aws cloudtrail get-trail-status --name trailname
```

## Azure Security Audit

### Subscriptions and resource groups

**Check subscriptions:**
```bash
# List all subscriptions
az account list

# Check resource groups
az group list
```

### Network security groups

**Check NSGs:**
```bash
# List all NSGs
az network nsg list

# Check NSG rules
az network nsg rule list --nsg-name nsgname --resource-group rgname
```

### Storage accounts

**Check storage accounts:**
```bash
# List all storage accounts
az storage account list

# Check access policies
az storage account show --name accountname --resource-group rgname
```

## GCP Security Audit

### Projects and organizations

**Check projects:**
```bash
# List all projects
gcloud projects list

# Check IAM policy
gcloud projects get-iam-policy project-id
```

### Compute Engine

**Check instances:**
```bash
# List all instances
gcloud compute instances list

# Check firewall rules
gcloud compute firewall-rules list
```

### Storage

**Check buckets:**
```bash
# List all buckets
gsutil ls

# Check bucket permissions
gsutil iam get gs://bucketname
```

## Automation Tools

### Scout Suite

```bash
# AWS audit
scout aws

# Azure audit
scout azure

# GCP audit
scout gcp
```

### Prowler

```bash
# AWS security audit
prowler -c check11,check12,check13

# Complete audit
prowler
```

### CloudSploit

```bash
# Scan an AWS account
cloudsploit scan aws

# Scan an Azure subscription
cloudsploit scan azure
```

### Pacu

```bash
# AWS penetration testing framework
pacu
```

## Audit Checklist

### IAM security
- [ ] Check user permissions
- [ ] Check role permissions
- [ ] Check access keys
- [ ] Check password policy
- [ ] Check whether MFA is enabled

### Network security
- [ ] Check security group / NSG rules
- [ ] Check VPC configuration
- [ ] Check network ACLs
- [ ] Check traffic encryption

### Data security
- [ ] Check data encryption
- [ ] Check key management
- [ ] Check backup strategy
- [ ] Check data classification

### Compliance
- [ ] Check audit logs
- [ ] Check monitoring and alerts
- [ ] Check incident response
- [ ] Check compliance frameworks

## Common Security Issues

### 1. Excessive permissions

**Issue:**
- IAM policies are too permissive
- Users have administrator permissions
- Roles have excessive permissions

**Fix:**
- Apply the principle of least privilege
- Review permissions regularly
- Use IAM policy simulation

### 2. Public resources

**Issue:**
- S3 buckets are public
- Security groups are open to 0.0.0.0/0
- Databases allow public access

**Fix:**
- Restrict the access scope
- Use private networks
- Enable access controls

### 3. Unencrypted data

**Issue:**
- Storage is not encrypted
- Transport is not encrypted
- Keys are managed improperly

**Fix:**
- Enable encryption
- Use TLS/SSL
- Use a key management service

### 4. Missing logs

**Issue:**
- Audit logs are not enabled
- Logs are not retained
- Logs are not monitored

**Fix:**
- Enable CloudTrail / Azure Monitor
- Set log retention policies
- Configure monitoring alerts

## Best Practices

### 1. Least privilege

- Grant only necessary permissions
- Review permissions regularly
- Use IAM policy simulation

### 2. Defense in depth

- Network-layer protection
- Application-layer protection
- Data-layer protection

### 3. Monitoring and alerting

- Enable audit logs
- Configure monitoring alerts
- Establish an incident response process

### 4. Compliance

- Follow compliance frameworks
- Perform regular security audits
- Document security policies

## Notes

- Audit only authorized environments.
- Avoid impacting production environments.
- Account for differences between cloud platforms.
- Perform security audits regularly.
