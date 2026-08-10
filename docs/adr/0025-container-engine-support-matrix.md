# Container engine support matrix

## Status

Accepted

## Context

The **Sandbox Runner** launches Linux containers through a host container CLI.
Today the default CLI is Docker. Partial Podman CLI compatibility already
exists (for example named-volume subpath syntax). Operators also run engines
inside desktop VMs:

- local mac development commonly uses **OrbStack** (Docker-compatible CLI);
- Windows development commonly uses **Docker Desktop** or **Podman Desktop**,
  whose Linux containers run inside a **WSL2 machine / Linux VM**.

These environments differ in host-gateway aliases, device passthrough
(`/dev/net/tun`), rootless capability, path translation for bind mounts, and
socket paths. Treating them as one undocumented "docker works" path produces
silent launch failures.

## Decision

Container engines are supported in this order and with these baselines:

1. **Podman first-class on Linux and macOS**  
   Documented, preflight-checked, and CLI-compatible as a peer of Docker.
   Named volume subpath, network create, stop/rm, host gateway, and sandbox
   capability flags (including opt-in VPN TUN) must have explicit engine
   behavior.

2. **Windows: native Windows daemon + Linux runtime in Desktop WSL machine**  
   - **`pentestd` runs natively on Windows** (not inside WSL).  
   - **Sandbox Runtime** runs only as **Linux containers** via Docker Desktop
     or Podman Desktop; those containers execute in the engine's **WSL2 /
     Linux VM**, not as native Windows containers.  
   - Native Windows containers are out of scope.  
   - Host paths used for task data (`PENTEST_RUNTIME_ROOT`, bind mounts into
     `/task`) are **Windows paths** that the Desktop engine must share into the
     Linux VM. The runner must emit mount sources the Windows Docker/Podman
     CLI accepts (absolute Windows paths), and preflight must fail closed when
     the engine cannot mount them.  
   - `host.docker.internal` (or the engine's host-gateway equivalent) points at
     the **Windows host** where the daemon listens, so sandbox → daemon MCP
     traffic can reach the native Windows process.

3. **Local mac baseline is OrbStack**  
   OrbStack's Docker-compatible CLI is the primary mac development path.
   Features that work on OrbStack (including `--add-host=host.docker.internal:host-gateway`
   and `--device /dev/net/tun` for opt-in sandbox VPN TUN) define the mac
   acceptance bar. Docker Desktop on mac is secondary compatibility.

The daemon keeps a single `PENTEST_CONTAINER_CLI` / `-container-cli` setting.
Engine-specific differences are resolved by small adapters (CLI flag variants,
host alias, path form for mounts, capability preflight), not by separate
runner products.

**Sandbox VPN TUN** remains opt-in and stays mutually exclusive with
`host_proxy_only`. Preflight must report when the selected engine cannot
provide TUN or `NET_ADMIN` rather than failing only after OpenVPN starts.

## Consequences

- Product docs and preflight must name the supported engines and the mac
  (OrbStack) and Windows (native daemon + Desktop WSL runtime) baselines.
- Podman work is higher priority than Windows Desktop polish.
- Compose and CI may keep Docker as the default automation path until Podman
  smoke is added.
- Rootless Podman may fail closed for VPN TUN and some network modes; that is
  acceptable when preflight states the limit.
- Windows support does not mean building a Windows sandbox image.
- Windows support **does** mean the daemon owns Windows-native SQLite paths,
  HTTP listen, and Host runner processes, while the Sandbox Runner only talks
  to the Desktop CLI that manages Linux containers in WSL.
- Cross-boundary mounts (Windows filesystem → Linux container) are a first-
  class failure mode: prefer short absolute runtime roots on drives Desktop
  shares by default, and document File Sharing / WSL integration requirements.
