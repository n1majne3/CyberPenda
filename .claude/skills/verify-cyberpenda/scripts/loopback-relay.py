#!/usr/bin/env python3
"""Forward one WSL-only listener to the operator's fixed loopback gateway."""

import argparse
import ipaddress
import json
from pathlib import Path
import select
import socket
import socketserver

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("run", type=Path)
parser.add_argument("wsl_address")
parser.add_argument("wsl_subnet")
parser.add_argument("gateway_port", type=int)
args = parser.parse_args()
root = args.run.resolve()
owner = json.loads((root / "owner.json").read_text())
assert owner["contract"] == "verify-cyberpenda-v1" and owner["root"] == str(root)
network = ipaddress.ip_network(args.wsl_subnet)
address = ipaddress.ip_address(args.wsl_address)
assert address.is_private and not address.is_loopback and address in network
assert 1 <= args.gateway_port <= 65535


class Relay(socketserver.BaseRequestHandler):
    def handle(self):
        if ipaddress.ip_address(self.client_address[0]) not in network:
            return
        with socket.create_connection(("127.0.0.1", args.gateway_port), timeout=10) as upstream:
            upstream.settimeout(None)
            peers = [self.request, upstream]
            while True:
                ready, _, _ = select.select(peers, [], [], 180)
                if not ready:
                    return
                for source in ready:
                    data = source.recv(65536)
                    if not data:
                        return
                    target = upstream if source is self.request else self.request
                    target.sendall(data)


class Server(socketserver.ThreadingTCPServer):
    daemon_threads = True


with Server((str(address), 0), Relay) as server:
    server.timeout = 0.5
    status = {"base_url": f"http://{address}:{server.server_address[1]}/v1",
              "upstream": f"http://127.0.0.1:{args.gateway_port}/v1", "allowed_subnet": str(network)}
    with (root / "scratch" / "relay.json").open("x", encoding="utf-8") as output:
        json.dump(status, output, indent=2)
    print(json.dumps(status), flush=True)
    try:
        while not (root / "RELAY_STOP").exists():
            server.handle_request()
    finally:
        with (root / "evidence" / "relay-cleanup.json").open("x", encoding="utf-8") as output:
            json.dump({"status": "stopped", **status}, output, indent=2)
