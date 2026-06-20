---
name: mobile-app-security-testing
description: Professional methodology and practical checks for mobile application security testing.
version: 1.0.0
---

# Mobile Application Security Testing

## Overview

Mobile application security testing is an important part of ensuring mobile app security. This skill provides methods, tools, and best practices for testing Android and iOS applications.

## Test Scope

### 1. Application security

**Check items:**
- Code obfuscation
- Anti-decompilation protections
- Anti-debugging protections
- Certificate pinning

### 2. Data security

**Check items:**
- Data encryption
- Key management
- Sensitive data storage
- Data transmission

### 3. Authentication and authorization

**Check items:**
- Authentication mechanisms
- Token management
- Biometrics
- Session management

### 4. Communication security

**Check items:**
- TLS/SSL configuration
- Certificate validation
- API security
- Man-in-the-middle protection

## Android Security Testing

### Static analysis

**Use APKTool:**
```bash
# Decompile an APK
apktool d app.apk

# View AndroidManifest.xml
cat app/AndroidManifest.xml

# View Smali code
find app/smali -name "*.smali"
```

**Use Jadx:**
```bash
# Decompile an APK
jadx -d output app.apk

# View Java source
find output -name "*.java"
```

**Use MobSF:**
```bash
# Start MobSF
docker run -it -p 8000:8000 opensecurity/mobsf

# Upload the APK for analysis
# Visit http://localhost:8000
```

### Dynamic analysis

**Use Frida:**
```javascript
// Hook a function
Java.perform(function() {
    var MainActivity = Java.use("com.example.MainActivity");
    MainActivity.onCreate.implementation = function(savedInstanceState) {
        console.log("[*] onCreate called");
        this.onCreate(savedInstanceState);
    };
});
```

**Use Objection:**
```bash
# Start Objection
objection -g com.example.app explore

# Hook a function
android hooking watch class_method com.example.MainActivity.onCreate
```

**Use Burp Suite:**
```bash
# Configure a proxy
# Set the Android proxy to point at Burp Suite
# Install the Burp certificate
```

### Common vulnerabilities

**Hard-coded secrets:**
```java
// Insecure code
String apiKey = "1234567890abcdef";
String password = "admin123";
```

**Insecure storage:**
```java
// SharedPreferences stores sensitive data
SharedPreferences prefs = getSharedPreferences("data", MODE_WORLD_READABLE);
prefs.edit().putString("password", password).apply();
```

**Certificate validation bypass:**
```java
// Certificate is not validated
TrustManager[] trustAllCerts = new TrustManager[] {
    new X509TrustManager() {
        public X509Certificate[] getAcceptedIssuers() { return null; }
        public void checkClientTrusted(X509Certificate[] certs, String authType) { }
        public void checkServerTrusted(X509Certificate[] certs, String authType) { }
    }
};
```

## iOS Security Testing

### Static analysis

**Use class-dump:**
```bash
# Export headers
class-dump app.ipa

# View headers
find app -name "*.h"
```

**Use Hopper:**
```bash
# Use Hopper to disassemble
# Open the app binary
# Analyze assembly code
```

**Use otool:**
```bash
# View Mach-O information
otool -L app

# View strings
strings app | grep -i "password\|key\|secret"
```

### Dynamic analysis

**Use Frida:**
```javascript
// Hook an Objective-C method
var className = ObjC.classes.ViewController;
var method = className['- login:password:'];
Interceptor.attach(method.implementation, {
    onEnter: function(args) {
        console.log("[*] Login called");
        console.log("Username: " + ObjC.Object(args[2]).toString());
        console.log("Password: " + ObjC.Object(args[3]).toString());
    }
});
```

**Use Cycript:**
```bash
# Attach to a process
cycript -p app

# Execute commands
[UIApplication sharedApplication]
```

### Common vulnerabilities

**Hard-coded secrets:**
```objective-c
// Insecure code
NSString *apiKey = @"1234567890abcdef";
NSString *password = @"admin123";
```

**Insecure storage:**
```objective-c
// Improper Keychain/storage use
NSUserDefaults *defaults = [NSUserDefaults standardUserDefaults];
[defaults setObject:password forKey:@"password"];
```

**Certificate validation bypass:**
```objective-c
// Certificate is not validated
- (void)connection:(NSURLConnection *)connection
didReceiveAuthenticationChallenge:(NSURLAuthenticationChallenge *)challenge {
    [challenge.sender useCredential:[NSURLCredential credentialForTrust:challenge.protectionSpace.serverTrust]
          forAuthenticationChallenge:challenge];
}
```

## Tool Usage

### MobSF

```bash
# Start MobSF
docker run -it -p 8000:8000 opensecurity/mobsf

# Upload an application for analysis
# Supports Android and iOS
```

### Frida

```bash
# Install Frida
pip install frida-tools

# Run a script
frida -U -f com.example.app -l script.js
```

### Objection

```bash
# Install Objection
pip install objection

# Start Objection
objection -g com.example.app explore
```

### Burp Suite

**Configure a proxy:**
1. Configure a Burp Suite listener.
2. Set the mobile device proxy.
3. Install the Burp certificate.
4. Intercept and analyze traffic.

## Test Checklist

### Application security
- [ ] Check code obfuscation
- [ ] Check anti-decompilation protections
- [ ] Check anti-debugging protections
- [ ] Check certificate pinning

### Data security
- [ ] Check data encryption
- [ ] Check key management
- [ ] Check sensitive data storage
- [ ] Check data transmission security

### Authentication and authorization
- [ ] Test authentication mechanisms
- [ ] Check token management
- [ ] Check session management
- [ ] Check biometrics

### Communication security
- [ ] Check TLS/SSL configuration
- [ ] Check certificate validation
- [ ] Test API security
- [ ] Check man-in-the-middle protection

## Common Security Issues

### 1. Hard-coded secrets

**Issue:**
- API keys are hard coded.
- Passwords are hard coded.
- Encryption keys are hard coded.

**Fix:**
- Use a key management service.
- Use environment variables where appropriate.
- Use secure storage.

### 2. Insecure storage

**Issue:**
- Sensitive data is stored in cleartext.
- Insecure storage mechanisms are used.
- Data is not encrypted.

**Fix:**
- Use encrypted storage.
- Use Keychain / Keystore.
- Implement data encryption.

### 3. Certificate validation bypass

**Issue:**
- SSL certificates are not validated.
- Self-signed certificates are accepted.
- Certificate pinning is not implemented.

**Fix:**
- Implement certificate pinning.
- Validate the certificate chain.
- Use the system certificate store.

### 4. Debug information disclosure

**Issue:**
- Logs contain sensitive information.
- Error messages disclose details.
- Debug mode is not disabled.

**Fix:**
- Remove debug code.
- Restrict log output.
- Disable debugging in production.

## Best Practices

### 1. Code security

- Implement code obfuscation.
- Disable debugging features.
- Implement anti-debugging protections.
- Use certificate pinning.

### 2. Data security

- Encrypt sensitive data.
- Use secure storage.
- Implement key management.
- Restrict data access.

### 3. Communication security

- Use TLS/SSL.
- Implement certificate pinning.
- Validate server certificates.
- Use secure APIs.

### 4. Authentication security

- Implement strong authentication.
- Manage tokens securely.
- Implement session management.
- Use biometrics where appropriate.

## Notes

- Test only in authorized environments.
- Follow applicable laws and regulations.
- Account for differences between platforms.
- Protect user privacy.
