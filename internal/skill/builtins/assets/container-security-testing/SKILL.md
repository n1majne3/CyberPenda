---
name: container-security-testing
description: Professional methodology and practical checks for container security testing.
version: 1.0.0
---

# Container Security Testing

## Overview

Container security testing is an important part of securing containerized applications. This skill provides methods, tools, and best practices for container security testing, covering Docker, Kubernetes, and related container technologies.

## Test Scope

### 1. Image security

**Check items:**
- Base image vulnerabilities
- Dependency package vulnerabilities
- Image configuration
- Sensitive information

### 2. Runtime security

**Check items:**
- Container permissions
- Resource limits
- Network isolation
- File system configuration

### 3. Orchestration security

**Check items:**
- Kubernetes configuration
- Service accounts
- RBAC
- Network policies

## Docker Security Testing

### Image scanning

**Use Trivy:**
```bash
# Scan an image
trivy image nginx:latest

# Scan a local image archive
trivy image --input nginx.tar

# Show only high-severity vulnerabilities
trivy image --severity HIGH,CRITICAL nginx:latest
```

**Use Clair:**
```bash
# Start Clair
docker run -d --name clair clair:latest

# Scan an image
clair-scanner --ip 192.168.1.100 nginx:latest
```

**Use Docker Bench:**
```bash
# Run Docker security benchmark checks
docker run --rm --net host --pid host --userns host --cap-add audit_control \
  -e DOCKER_CONTENT_TRUST=$DOCKER_CONTENT_TRUST \
  -v /etc:/etc:ro \
  -v /usr/bin/containerd:/usr/bin/containerd:ro \
  -v /usr/bin/runc:/usr/bin/runc:ro \
  -v /usr/lib/systemd:/usr/lib/systemd:ro \
  -v /var/lib:/var/lib:ro \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  --label docker_bench_security \
  docker/docker-bench-security
```

### Container configuration checks

**Check the Dockerfile:**
```dockerfile
# Examples of security issues
FROM ubuntu:latest  # Uses the latest tag
RUN apt-get update && apt-get install -y curl  # Version is not pinned
COPY . /app  # May include sensitive files
ENV PASSWORD=secret  # Hard-coded password
USER root  # Runs as root
```

**Security best practices:**
```dockerfile
# Use a specific version
FROM ubuntu:20.04

# Pin package versions
RUN apt-get update && apt-get install -y curl=7.68.0-1ubuntu2.7

# Use a non-root user
RUN useradd -m appuser
USER appuser

# Minimize the image
FROM alpine:3.15

# Multi-stage build
FROM golang:1.18 AS builder
WORKDIR /app
COPY . .
RUN go build -o app

FROM alpine:3.15
COPY --from=builder /app/app /app
```

### Runtime checks

**Check container permissions:**
```bash
# Check privileged containers
docker ps --filter "label=privileged=true"

# Check mounted host directories
docker inspect container_name | grep -A 10 Mounts

# Check container networks
docker network inspect network_name
```

**Check resource limits:**
```bash
# Check memory limits
docker stats container_name

# Check CPU limits
docker inspect container_name | grep -i cpu
```

## Kubernetes Security Testing

### Configuration checks

**Use kube-bench:**
```bash
# Run kube-bench
kube-bench run

# Check specific benchmark targets
kube-bench run --targets master,node,etcd
```

**Use kube-hunter:**
```bash
# Run kube-hunter
kube-hunter --remote target-ip

# Active mode
kube-hunter --active
```

### Pod security

**Check pod security policies:**
```yaml
# Insecure pod configuration
apiVersion: v1
kind: Pod
spec:
  containers:
  - name: app
    image: nginx
    securityContext:
      privileged: true  # Privileged mode
      runAsUser: 0  # Root user
```

**Secure configuration:**
```yaml
apiVersion: v1
kind: Pod
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    fsGroup: 2000
  containers:
  - name: app
    image: nginx
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop:
        - ALL
        add:
        - NET_BIND_SERVICE
```

### RBAC checks

**Check role permissions:**
```bash
# List all roles
kubectl get roles --all-namespaces

# Check role bindings
kubectl get rolebindings --all-namespaces

# Check cluster roles
kubectl get clusterroles

# Check user permissions
kubectl auth can-i --list --as=system:serviceaccount:default:sa-name
```

**Common issues:**
- Excessive permissions
- Unused roles
- Unused service accounts

### Network policies

**Check network policies:**
```bash
# List all network policies
kubectl get networkpolicies --all-namespaces

# Check network policy configuration
kubectl describe networkpolicy policy-name -n namespace
```

**Network policy example:**
```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
```

## Tool Usage

### Falco

**Runtime security monitoring:**
```bash
# Install Falco
helm repo add falcosecurity https://falcosecurity.github.io/charts
helm install falco falcosecurity/falco

# Check rules
falco -r /etc/falco/rules.d/
```

### Aqua Security

```bash
# Scan an image
aqua image scan nginx:latest

# Scan a Kubernetes cluster
aqua k8s scan
```

### Snyk

```bash
# Scan a Docker image
snyk test --docker nginx:latest

# Scan Kubernetes configuration
snyk iac test k8s/
```

## Test Checklist

### Image security
- [ ] Scan base image vulnerabilities
- [ ] Scan dependency package vulnerabilities
- [ ] Check Dockerfile configuration
- [ ] Check for sensitive information leaks

### Runtime security
- [ ] Check container permissions
- [ ] Check resource limits
- [ ] Check network isolation
- [ ] Check file-system mounts

### Orchestration security
- [ ] Check Kubernetes configuration
- [ ] Check RBAC configuration
- [ ] Check network policies
- [ ] Check pod security policies

## Common Security Issues

### 1. Image vulnerabilities

**Issue:**
- The base image contains vulnerabilities
- Dependency packages contain vulnerabilities
- Images are not updated promptly

**Fix:**
- Scan images regularly
- Update base images promptly
- Use minimal images

### 2. Excessive permissions

**Issue:**
- Containers run as root
- Privileged mode is enabled
- Sensitive directories are mounted

**Fix:**
- Use non-root users
- Disable privileged mode
- Restrict file-system access

### 3. Configuration mistakes

**Issue:**
- Default configuration is insecure
- Network policies are missing
- RBAC is misconfigured

**Fix:**
- Follow security best practices
- Implement network policies
- Configure RBAC correctly

### 4. Sensitive information disclosure

**Issue:**
- Images contain secrets
- Environment variables are exposed
- Configuration files are leaked

**Fix:**
- Use secret management
- Avoid hard coding
- Use Kubernetes Secret objects

## Best Practices

### 1. Image security

- Use official base images
- Update images regularly
- Scan images for vulnerabilities
- Minimize image size

### 2. Runtime security

- Use non-root users
- Restrict container permissions
- Implement resource limits
- Enable security contexts

### 3. Orchestration security

- Configure network policies
- Implement RBAC
- Use pod security policies
- Enable audit logs

## Notes

- Test only in authorized environments.
- Avoid impacting production environments.
- Account for differences between container platforms.
- Run security scans regularly.
