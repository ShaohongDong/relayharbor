#!/usr/bin/env python3
"""Real GOST routing tests on loopback; no root or systemd required."""
import argparse
import asyncio
import copy
import contextlib
import hashlib
import json
import os
from pathlib import Path
import struct
import sys
import tarfile
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from gost_deploy import model, native, protocol
from gost_deploy.lifecycle import initial_config, PINNED
from gost_deploy.runtime import Runtime, close, free_addr, private_write


async def echo(reader, writer):
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            writer.write(data)
            await writer.drain()
    except OSError:
        pass
    finally:
        await close(writer)


async def http_target(reader, writer):
    try:
        request = await reader.readuntil(b"\r\n\r\n")
        code = b"503 Unavailable" if b"/unavailable " in request else b"200 OK"
        writer.write(b"HTTP/1.1 " + code + b"\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
        await writer.drain()
    except (OSError, asyncio.IncompleteReadError):
        pass
    finally:
        await close(writer)


async def rpc(directory, command, **value):
    reader, writer = await asyncio.open_unix_connection(str(directory / "control.sock"))
    try:
        writer.write(json.dumps({"command": command, **value}).encode() + b"\n")
        await writer.drain()
        response = json.loads(await asyncio.wait_for(reader.readline(), 30))
        assert "error" not in response, response
        return response["data"]
    finally:
        await close(writer)


async def connect(cfg, host, port, command=protocol.CONNECT):
    reader, writer = await asyncio.open_connection(*model.endpoint(cfg["listener"]["addr"]))
    try:
        await protocol.handshake(reader, writer, cfg["listener"]["users"][0])
        await protocol.request(reader, writer, command, protocol.address_bytes(host, port))
        return reader, writer
    except BaseException:
        await close(writer)
        raise


async def payload(reader, writer):
    writer.write(b"still-alive-through-profile-switch")
    await writer.drain()
    assert await asyncio.wait_for(reader.readexactly(34), 5) == b"still-alive-through-profile-switch"


async def campaign(binary, directory):
    command = binary if isinstance(binary, list) else [str(binary)]
    auth = {"username": "upstream", "password": "test-upstream-password"}
    options = {"gateway": free_addr(), "auth": {"username": "internal", "password": "test-internal-password"}}
    cfg = model.normalize(initial_config(free_addr(), True))
    echo_server = await asyncio.start_server(echo, "127.0.0.1", 0)
    http_server = await asyncio.start_server(http_target, "127.0.0.1", 0)
    echo_port = echo_server.sockets[0].getsockname()[1]
    http_port = http_server.sockets[0].getsockname()[1]
    upstreams = []
    processes = []
    logs = []
    runtime = None
    long_writer = None
    periodic = None
    try:
        for kind in ("http", "socks5"):
            addr = free_addr()
            upstream = native.service(addr, [auth])
            upstream["services"][0]["handler"]["type"] = kind
            path = directory / (kind + "-upstream.json")
            private_write(path, upstream)
            log = open(directory / (kind + "-upstream.log"), "wb")
            logs.append(log)
            process = await asyncio.create_subprocess_exec(*command, "-C", str(path), stdout=log, stderr=log)
            processes.append(process)
            upstreams.append({"id": kind, "type": kind, "addr": addr, "auth": auth})
        await asyncio.sleep(0.3)
        cfg["profiles"].extend([
            {"name": "mixed", "type": "pool", "nodes": upstreams,
             "probe": {"type": "tcp", "addr": "127.0.0.1:{}".format(echo_port), "interval": 1, "timeout": 1}},
            {"name": "http-check", "type": "pool", "nodes": [upstreams[0]],
             "probe": {"type": "http", "url": "http://127.0.0.1:{}/ready".format(http_port), "interval": 30, "timeout": 1}}])
        # A public HTTP upstream demonstrates that optional upstream auth works.
        public_addr = free_addr()
        public = {"services": [{"name": "public-http", "addr": public_addr,
                  "handler": {"type": "http"}, "listener": {"type": "tcp"}}]}
        public_path = directory / "public-http.json"
        private_write(public_path, public)
        log = open(directory / "public-http.log", "wb")
        logs.append(log)
        processes.append(await asyncio.create_subprocess_exec(*command, "-C", str(public_path), stdout=log, stderr=log))
        await asyncio.sleep(0.2)
        cfg["profiles"].append({"name": "public-upstream", "type": "pool",
            "nodes": [{"id": "public", "type": "http", "addr": public_addr}],
            "probe": {"type": "http", "url": "http://127.0.0.1:{}/ready".format(http_port),
                      "interval": 30, "timeout": 1}})
        cfg = model.normalize(cfg)
        runtime = Runtime(cfg, options, binary, directory / "runtime", "initial")
        await runtime.start()
        periodic = asyncio.create_task(runtime.periodic())
        for _ in range(100):
            try:
                long_reader, long_writer = await connect(cfg, "localhost", echo_port)
                break
            except OSError:
                await asyncio.sleep(0.05)
        else:
            raise AssertionError("frontend did not start")
        await payload(long_reader, long_writer)
        frontend_pid = runtime.frontend.pid
        print("PASS real GOST direct TCP and domain forwarding", flush=True)
        # Check the existing external UDP client against the stable frontend.
        # exercise uses a test-only name configured in containers; use localhost here.
        import protocol as client_protocol
        # Dedicated UDP test avoids modifying system name resolution.
        def udp_echo():
            import socket
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as target:
                target.bind(("127.0.0.1", 0))
                target.settimeout(5)
                with client_protocol.auth_connect(*model.endpoint(cfg["listener"]["addr"]), cfg["listener"]["users"][0]) as control:
                    control.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00")
                    relay_host, relay_port = client_protocol.reply(control)
                    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
                        client.settimeout(5)
                        packet = b"\x00\x00\x00\x01\x7f\x00\x00\x01" + struct.pack("!H", target.getsockname()[1]) + b"udp-preserved"
                        client.sendto(packet, (relay_host, relay_port))
                        data, sender = target.recvfrom(65535)
                        target.sendto(data, sender)
                        assert client.recvfrom(65535)[0].endswith(b"udp-preserved")
        await asyncio.get_running_loop().run_in_executor(None, udp_echo)
        print("PASS direct UDP through internal GOST tunnel", flush=True)
        assert all(n["healthy"] for n in runtime.status()["nodes"]), runtime.status()
        cfg["activeProfile"] = "mixed"
        await rpc(runtime.directory, "prepare", config=cfg, revision="mixed-one")
        status = await rpc(runtime.directory, "commit", revision="mixed-one")
        assert status["revision"] == "mixed-one" and status["available"]
        assert runtime.frontend.pid == frontend_pid
        await payload(long_reader, long_writer)
        selected = []
        for _ in range(4):
            before = {key: w.connections for key, w in runtime.current.workers.items()}
            reader, writer = await connect(cfg, "localhost", echo_port)
            await payload(reader, writer)
            chosen = [key[1] for key, w in runtime.current.workers.items()
                      if key[0] == "mixed" and w.connections > before[key]]
            assert len(chosen) == 1, chosen
            selected.extend(chosen)
            await close(writer)
            await asyncio.sleep(0.05)
        assert selected == ["http", "socks5", "http", "socks5"], selected
        print("PASS mixed HTTP/SOCKS5 authenticated round robin and acknowledged hot switch", flush=True)
        # Keep a pool-backed connection across another profile change.
        pool_reader, pool_writer = await connect(cfg, "localhost", echo_port)
        cfg["activeProfile"] = "direct"
        await rpc(runtime.directory, "prepare", config=cfg, revision="direct-again")
        await rpc(runtime.directory, "commit", revision="direct-again")
        await payload(pool_reader, pool_writer)
        await close(pool_writer)
        await payload(long_reader, long_writer)
        cfg["activeProfile"] = "mixed"
        await rpc(runtime.directory, "prepare", config=cfg, revision="mixed-again")
        await rpc(runtime.directory, "commit", revision="mixed-again")
        # UDP is rejected while the active profile is a pool.
        try:
            _, denied = await connect(cfg, "0.0.0.0", 0, protocol.UDP_TUNNEL)
        except (model.Error, asyncio.IncompleteReadError):
            pass
        else:
            await close(denied)
            raise AssertionError("pool accepted UDP")
        print("PASS existing direct/pool TCP streams survive switches; pool UDP rejected", flush=True)
        # Kill the HTTP upstream; the next request retries the other node without replaying payload.
        processes[0].terminate()
        await processes[0].wait()
        runtime.current.rounds["mixed"] = 0
        reader, writer = await connect(cfg, "localhost", echo_port)
        await payload(reader, writer)
        await close(writer)
        assert not runtime.current.health[("mixed", "http")]["healthy"]
        processes[1].terminate()
        await processes[1].wait()
        try:
            _, failed = await connect(cfg, "localhost", echo_port)
        except (model.Error, asyncio.IncompleteReadError):
            pass
        else:
            await close(failed)
            raise AssertionError("all-dead pool fell back to direct")
        assert not runtime.status()["available"]
        await payload(long_reader, long_writer)
        print("PASS request-level failover and all-dead pool fails closed", flush=True)
        # Restart an upstream and wait for the real periodic probe to recover it.
        log = open(directory / "recovered.log", "wb")
        logs.append(log)
        process = await asyncio.create_subprocess_exec(*command, "-C", str(directory / "socks5-upstream.json"), stdout=log, stderr=log)
        processes.append(process)
        deadline = time.monotonic() + 15
        while not runtime.current.health[("mixed", "socks5")]["healthy"]:
            assert time.monotonic() < deadline, "periodic probe did not recover node"
            assert not periodic.done(), periodic.exception() if periodic.done() else None
            await asyncio.sleep(0.2)
        reader, writer = await connect(cfg, "localhost", echo_port)
        await payload(reader, writer)
        await close(writer)
        print("PASS cooldown and end-to-end periodic recovery", flush=True)
        # Invalid staged configuration leaves the selected generation unchanged.
        broken = copy.deepcopy(cfg)
        broken["activeProfile"] = "missing"
        try:
            await rpc(runtime.directory, "prepare", config=broken, revision="bad")
        except AssertionError:
            pass
        else:
            raise AssertionError("invalid prepare accepted")
        assert (await rpc(runtime.directory, "status"))["revision"] == "mixed-again"
        # Killing a local worker is supervised separately from upstream health.
        worker = runtime.current.workers[("mixed", "socks5")]
        old_pid = worker.process.pid
        worker.process.kill()
        await worker.process.wait()
        for _ in range(50):
            if worker.process.pid != old_pid and worker.process.returncode is None:
                break
            await asyncio.sleep(0.1)
        else:
            raise AssertionError("worker was not restarted")
        print("PASS invalid prepare preservation and worker supervision", flush=True)
        # An application-level HTTP failure must fail the probe, even with CONNECT working.
        cfg["profiles"][3]["probe"]["url"] = "http://127.0.0.1:{}/unavailable".format(http_port)
        cfg["activeProfile"] = "direct"
        await rpc(runtime.directory, "prepare", config=cfg, revision="http-failed")
        await rpc(runtime.directory, "commit", revision="http-failed")
        assert not runtime.current.health[("public-upstream", "public")]["healthy"]
        print("PASS unauthenticated HTTP upstream and response-status probe failure", flush=True)
        # Remove a worker from configuration while it still owns a live connection.
        cfg["activeProfile"] = "mixed"
        await rpc(runtime.directory, "prepare", config=cfg, revision="drain-source")
        await rpc(runtime.directory, "commit", revision="drain-source")
        drain_reader, drain_writer = await connect(cfg, "localhost", echo_port)
        await payload(drain_reader, drain_writer)
        worker = runtime.current.workers[("mixed", "socks5")]
        cfg["activeProfile"] = "direct"
        cfg["profiles"] = [cfg["profiles"][0]]
        await rpc(runtime.directory, "prepare", config=cfg, revision="drain-destination")
        await rpc(runtime.directory, "commit", revision="drain-destination")
        await asyncio.sleep(1)
        assert worker.process.returncode is None
        await payload(drain_reader, drain_writer)
        await close(drain_writer)
        for _ in range(50):
            if worker.process.returncode is not None:
                break
            await asyncio.sleep(0.1)
        else:
            raise AssertionError("unreferenced worker did not drain")
        print("PASS removed workers drain existing streams before exit", flush=True)
    finally:
        await close(long_writer)
        if periodic:
            periodic.cancel()
            await asyncio.gather(periodic, return_exceptions=True)
        if runtime:
            await runtime.stop()
        for process in processes:
            if process.returncode is None:
                process.terminate()
            await process.wait()
        for log in logs:
            log.close()
        echo_server.close()
        http_server.close()
        await echo_server.wait_closed()
        await http_server.wait_closed()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("archive", nargs="?", type=Path,
                        default=ROOT / ".cache/releases/gost_3.3.0_linux_amd64.tar.gz")
    parser.add_argument("--arch", choices=("amd64", "arm64"), default="amd64")
    parser.add_argument("--qemu")
    args = parser.parse_args()
    assert hashlib.sha256(args.archive.read_bytes()).hexdigest() == PINNED[args.arch]
    with tempfile.TemporaryDirectory(prefix="gost-routing-test-") as temporary:
        directory = Path(temporary)
        binary = directory / "gost"
        with tarfile.open(args.archive) as tar, tar.extractfile("gost") as source:
            binary.write_bytes(source.read())
        binary.chmod(0o755)
        command = [args.qemu, str(binary)] if args.qemu else [str(binary)]
        asyncio.run(campaign(command, directory))


if __name__ == "__main__":
    main()
