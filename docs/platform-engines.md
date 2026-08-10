# Container engine platforms

Product decision: [ADR 0025](adr/0025-container-engine-support-matrix.md).

## Matrix

| Host | Daemon | Container CLI | Sandbox Runtime |
| --- | --- | --- | --- |
| macOS | native | Docker CLI via **OrbStack** (default) | Linux container in OrbStack VM |
| macOS / Linux | native | `podman` (`PENTEST_CONTAINER_CLI=podman`) | Linux container (prefer rootful for VPN TUN) |
| Windows | **native Windows** | Docker Desktop / Podman Desktop CLI | Linux container in Desktop **WSL/Linux VM** |

Native Windows containers are out of scope. The sandbox image is always Linux (Kali).

## macOS (OrbStack)

```sh
# OrbStack provides `docker` on PATH
./pentestd
# or
make dev
```

Opt-in OpenVPN inside the sandbox:

1. Launch Task with **Sandbox network** = Default bridge  
2. Enable **Sandbox VPN TUN**  
3. Connect with the task `.ovpn` under `/task/workdir`

## Podman (Linux / macOS)

```sh
export PENTEST_CONTAINER_CLI=podman
./pentestd
```

- Named volume subpaths use Podman `subpath=` syntax.
- Host gateway includes both `host.docker.internal` and `host.containers.internal`.
- **Rootless Podman**: preflight fails **Sandbox VPN TUN** (no durable `NET_ADMIN` + `/dev/net/tun`).
- `host_proxy_only` and **Sandbox VPN TUN** are mutually exclusive.

## Windows (native daemon + Desktop WSL runtime)

```text
Windows host                         Desktop WSL Linux VM
─────────────────                    ────────────────────
pentestd.exe  ──docker.exe/podman──►  sandbox container
  SQLite, UI, API, MCP                 /task bind from Windows path
  Host runner (optional)               optional /dev/net/tun
```

### Setup

1. Install Docker Desktop or Podman Desktop with **Linux containers** (WSL2 backend).  
2. Run **native** `pentestd.exe` (not inside WSL unless you choose to).  
3. Set runtime root on a shared drive, for example:

```bat
set PENTEST_RUNTIME_ROOT=C:\CyberPenda\runs
set PENTEST_CONTAINER_CLI=docker
pentestd.exe
```

4. In Desktop settings, enable **File Sharing** (or WSL integration) for that drive.  
5. Preflight checks:
   - `container_engine` — CLI reaches Desktop  
   - `sandbox_runtime_root` — path exists, is writable, shows normalized bind source `C:/...`  
   - `sandbox_vpn_tun` — only when requested; needs non-rootless engine with TUN  

### Mount form

Sandbox create uses:

```text
--mount type=bind,src=C:/CyberPenda/runs/<task>,dst=/task
```

not `-v C:\...:/task` (drive colon breaks `-v` parsing).

### Compose note

`docker-compose.yaml` mounts `/var/run/docker.sock` and is aimed at Linux hosts (or Linux VMs). On Windows Desktop, prefer the native Windows daemon with the Desktop CLI rather than compose-in-WSL unless you deliberately run the whole stack under Linux.

## Health

`GET /api/health` reports:

```json
"runner": {
  "container_cli": "docker",
  "engine_kind": "docker",
  "engine_name": "OrbStack"
}
```
