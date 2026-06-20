---
name: api-security-testing
description: Professional methodology and practical checks for API security testing.
version: 1.0.0
---

# API Security Testing

## Overview

API security testing is an important part of ensuring that API interfaces are safe to expose. This skill provides methods, tools, and best practices for testing API security.

## Test Scope

### 1. Authentication and authorization

**Test items:**
- Token validity checks
- Token expiration handling
- Access control
- Role permission validation

### 2. Input validation

**Test items:**
- Parameter type validation
- Data length limits
- Special character handling
- SQL injection protection
- XSS protection

### 3. Business logic

**Test items:**
- Workflow validation
- State transitions
- Concurrency control
- Business rules

### 4. Error handling

**Test items:**
- Error message disclosure
- Stack traces
- Sensitive information exposure

## Testing Methods

### 1. API discovery

**Identify API endpoints:**
```bash
# Use directory scanning
gobuster dir -u https://target.com -w api-wordlist.txt

# Use Burp Suite passive scanning
# Browse the application and observe API calls

# Analyze JavaScript files
# Look for API endpoint definitions
```

### 2. Authentication testing

**Token testing:**
```http
# Test an invalid token
GET /api/user
Authorization: Bearer invalid_token

# Test an expired token
GET /api/user
Authorization: Bearer expired_token

# Test without a token
GET /api/user
```

**JWT testing:**
```bash
# Use jwt_tool
python jwt_tool.py <JWT_TOKEN>

# Test algorithm confusion
python jwt_tool.py <JWT_TOKEN> -X a

# Test key brute forcing
python jwt_tool.py <JWT_TOKEN> -C -d wordlist.txt
```

### 3. Authorization testing

**Horizontal authorization:**
```http
# User A accesses User B's resource
GET /api/user/123
Authorization: Bearer user_a_token

# Expected result: 403
```

**Vertical authorization:**
```http
# A normal user accesses an administrator endpoint
GET /api/admin/users
Authorization: Bearer user_token

# Expected result: 403
```

### 4. Input validation testing

**SQL injection:**
```http
POST /api/search
{
  "query": "test' OR '1'='1"
}
```

**Command injection:**
```http
POST /api/execute
{
  "command": "ping; id"
}
```

**XXE:**
```http
POST /api/parse
Content-Type: application/xml

<?xml version="1.0"?>
<!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<foo>&xxe;</foo>
```

### 5. Rate-limit testing

**Test rate limiting:**
```python
import requests

for i in range(1000):
    response = requests.get('https://target.com/api/endpoint')
    print(f"Request {i}: {response.status_code}")
```

## Tool Usage

### Postman

**Create a test collection:**
1. Import the API documentation
2. Configure authentication
3. Create test cases
4. Run automated tests

### Burp Suite

**API scanning:**
1. Configure API endpoints
2. Configure authentication
3. Run active scanning
4. Analyze the results

### OWASP ZAP

```bash
# API scan
zap-cli quick-scan --self-contained \
  --start-options '-config api.disablekey=true' \
  http://target.com/api
```

### REST-Attacker

```bash
# Scan an OpenAPI specification
rest-attacker scan openapi.yaml
```

## Common Vulnerabilities

### 1. Authentication bypass

**Token validation flaws:**
- Weak token generation
- Predictable tokens
- Token signatures are not verified

### 2. Privilege escalation

**IDOR:**
- Direct object references
- Resource ownership is not verified

### 3. Information disclosure

**Error information:**
- Detailed error messages
- Stack traces
- Sensitive data

### 4. Injection vulnerabilities

**Common injection classes:**
- SQL injection
- NoSQL injection
- Command injection
- XXE

### 5. Business logic

**Logic flaws:**
- Price manipulation
- Quantity limit bypass
- State modification

## Test Checklist

### Authentication testing
- [ ] Token validity validation
- [ ] Token expiration handling
- [ ] Weak token detection
- [ ] Token replay attacks

### Authorization testing
- [ ] Horizontal authorization testing
- [ ] Vertical authorization testing
- [ ] Role permission validation
- [ ] Resource access control

### Input validation
- [ ] SQL injection testing
- [ ] XSS testing
- [ ] Command injection testing
- [ ] XXE testing
- [ ] Parameter pollution

### Business logic
- [ ] Workflow validation
- [ ] State transitions
- [ ] Concurrency control
- [ ] Business rules

### Error handling
- [ ] Error message disclosure
- [ ] Stack traces
- [ ] Sensitive information exposure

## Defensive Measures

### Recommended approaches

1. **Authentication**
   - Use strong tokens
   - Implement token refresh
   - Verify token signatures

2. **Authorization**
   - Role-based access control
   - Resource ownership validation
   - Principle of least privilege

3. **Input validation**
   - Parameter type validation
   - Data length limits
   - Allowlist validation

4. **Error handling**
   - Unified error responses
   - Do not disclose detailed information
   - Log errors

5. **Rate limiting**
   - Implement API rate limiting
   - Prevent brute-force attempts
   - Monitor abnormal requests

## Notes

- Test only in authorized environments.
- Avoid impacting the API.
- Pay attention to differences between API versions.
- Control request frequency during testing.
