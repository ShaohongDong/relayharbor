"""Unprivileged supervisor, generation-based scheduler and private control socket."""
import asyncio
import contextlib
import hashlib
import hmac
import json
import os
from pathlib import Path
import signal
import socket
import time

from . import native, protocol
from .model import Error, endpoint, normalize

MAX_BODY = 1024 * 1024
FAILURE = b"\x05\x01\x00\x01\x00\x00\x00\x00\x00\x00"


def dump(value):
    return (json.dumps(value, ensure_ascii=False) + "\n").encode()


def private_write(path, value):
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(dump(value))


def free_addr():
    with socket.socket() as stream:
        stream.bind(("127.0.0.1", 0))
        return "127.0.0.1:{}".format(stream.getsockname()[1])


async def close(writer):
    if writer:
        writer.close()
        with contextlib.suppress(OSError, asyncio.TimeoutError):
            await asyncio.wait_for(writer.wait_closed(), 1)


class Worker:
    def __init__(self, runtime, node):
        self.runtime = runtime
        self.node = node
        self.key = hashlib.sha256(dump(node)).hexdigest()
        self.addr = free_addr()
        self.process = None
        self.connections = 0
        self.lock = asyncio.Lock()
        self.config = runtime.directory / (self.key + ".json")
        self.log = runtime.directory / (self.key + ".log")

    async def start(self):
        async with self.lock:
            if self.process and self.process.returncode is None:
                return
            private_write(self.config, native.worker(self.addr, self.runtime.auth, self.node))
            fd = os.open(str(self.log), os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, "wb") as output:
                self.process = await asyncio.create_subprocess_exec(
                    *self.runtime.binary, "-C", str(self.config), stdout=output, stderr=output)
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                if self.process.returncode is not None:
                    raise Error("GOST worker 启动失败。")
                writer = None
                try:
                    reader, writer = await asyncio.wait_for(asyncio.open_connection(*endpoint(self.addr)), 0.2)
                    await asyncio.wait_for(protocol.handshake(reader, writer, self.runtime.auth), 0.5)
                    return
                except (OSError, Error, asyncio.TimeoutError, asyncio.IncompleteReadError):
                    await asyncio.sleep(0.05)
                finally:
                    await close(writer)
            await self.stop()
            raise Error("GOST worker 启动超时。")

    async def stop(self):
        if self.process and self.process.returncode is None:
            self.process.terminate()
            try:
                await asyncio.wait_for(self.process.wait(), 3)
            except asyncio.TimeoutError:
                self.process.kill()
                await self.process.wait()
        self.config.unlink(missing_ok=True)
        self.log.unlink(missing_ok=True)


class Generation:
    def __init__(self, cfg, revision, workers):
        self.cfg = cfg
        self.revision = revision
        self.workers = workers
        self.health = {}
        self.pending = 0
        self.rounds = {}
        self.next_probe = {}
        for p in cfg["profiles"]:
            for n in p.get("nodes", []):
                self.health[(p["name"], n["id"])] = {
                    "healthy": False, "lastProbe": None, "latencyMs": None,
                    "cooldownUntil": 0, "serial": 0, "failures": 0}

    def profile(self):
        return next(p for p in self.cfg["profiles"] if p["name"] == self.cfg["activeProfile"])

    def node_status(self):
        return [{"profile": p["name"], "id": n["id"], "type": n["type"], "addr": n["addr"],
                 **{k: v for k, v in self.health[(p["name"], n["id"])].items() if k not in ("cooldownUntil", "serial")}}
                for p in self.cfg["profiles"] for n in p.get("nodes", [])]


class Runtime:
    def __init__(self, cfg, options, binary, directory, revision):
        self.initial = normalize(cfg)
        self.options = options
        self.auth = options["auth"]
        self.binary = [str(v) for v in binary] if isinstance(binary, (list, tuple)) else [str(binary)]
        self.directory = Path(directory)
        self.initial_revision = revision
        self.workers = {}
        self.generations = set()
        self.current = None
        self.prepared = None
        self.frontend = None
        self.front_log = None
        self.servers = []
        self.tasks = set()
        self.probe_limit = asyncio.Semaphore(16)
        self.mutation_lock = asyncio.Lock()
        self.stopping = False

    async def get_worker(self, node):
        key = hashlib.sha256(dump(node)).hexdigest()
        worker = self.workers.get(key)
        if worker is None:
            worker = Worker(self, node)
            self.workers[key] = worker
        await worker.start()
        return worker

    async def prepare(self, cfg, revision):
        if not isinstance(revision, str) or not revision:
            raise Error("配置代次无效。")
        cfg = normalize(cfg)
        if self.current and cfg["listener"] != self.current.cfg["listener"]:
            raise Error("入口变更需要事务重启。")
        mapping = {}
        for p in cfg["profiles"]:
            if p["type"] == "direct":
                mapping[(p["name"], None)] = await self.get_worker(None)
            else:
                for n in p["nodes"]:
                    mapping[(p["name"], n["id"])] = await self.get_worker(n)
        generation = Generation(cfg, revision, mapping)
        # Retain health/cooldown for unchanged nodes and probe policy.
        if self.current:
            old_profiles = {p["name"]: p for p in self.current.cfg["profiles"]}
            for p in cfg["profiles"]:
                old = old_profiles.get(p["name"], {})
                if old.get("probe") == p.get("probe"):
                    for n in p.get("nodes", []):
                        key = (p["name"], n["id"])
                        if self.current.workers.get(key) is mapping[key] and key in self.current.health:
                            generation.health[key] = dict(self.current.health[key])
        self.generations.add(generation)
        self.prepared = generation
        await self.probe_generation(generation, only_unknown=True)
        return {"preparedRevision": revision}

    async def commit(self, revision):
        if not self.prepared or self.prepared.revision != revision:
            raise Error("待提交代次不匹配。")
        # No await between publishing the snapshot and returning its acknowledgement.
        self.current = self.prepared
        self.prepared = None
        return self.status()

    def status(self):
        if not self.current:
            return {"ready": False}
        profile = self.current.profile()
        count = sum(n["healthy"] for n in self.current.node_status() if n["profile"] == profile["name"])
        return {"ready": True, "revision": self.current.revision,
                "activeProfile": profile["name"], "available": profile["type"] == "direct" or count > 0,
                "healthyNodes": count, "nodes": self.current.node_status(),
                "connections": sum(w.connections for w in self.workers.values()),
                "frontendPid": self.frontend.pid if self.frontend else None}

    async def probe_one(self, generation, profile, node):
        key = (profile["name"], node["id"])
        state = generation.health[key]
        if time.monotonic() < state["cooldownUntil"]:
            return
        serial = state["serial"]
        worker = generation.workers[key]
        async with self.probe_limit:
            try:
                await worker.start()
                result = await asyncio.get_running_loop().run_in_executor(
                    None, protocol.probe, worker.addr, self.auth, profile["probe"])
            except (Error, OSError):
                result = {"healthy": False, "latencyMs": None}
        if state["serial"] != serial:
            return
        state.update(result, lastProbe=time.time(), serial=serial + 1)
        if result["healthy"]:
            state["failures"] = 0
        else:
            state["failures"] += 1
            state["cooldownUntil"] = time.monotonic() + 10

    async def probe_generation(self, generation, profile_name=None, only_unknown=False):
        jobs = []
        found = False
        for p in generation.cfg["profiles"]:
            if profile_name is not None and p["name"] != profile_name:
                continue
            found = True
            generation.next_probe[p["name"]] = time.monotonic() + p.get("probe", {}).get("interval", 30)
            for n in p.get("nodes", []):
                if not only_unknown or generation.health[(p["name"], n["id"])]["lastProbe"] is None:
                    jobs.append(self.probe_one(generation, p, n))
        if not found:
            raise Error("profile 不存在。")
        if jobs:
            await asyncio.gather(*jobs)
        return generation.node_status()

    async def periodic(self):
        while True:
            await asyncio.sleep(0.5)
            generation = self.current
            if generation:
                for p in generation.cfg["profiles"]:
                    if time.monotonic() >= generation.next_probe.get(p["name"], 0):
                        await self.probe_generation(generation, p["name"])
            if self.frontend and self.frontend.returncode is not None:
                raise Error("入口 GOST 意外退出。")
            async with self.mutation_lock:
                retained = set()
                for g in self.generations:
                    if g.pending > 0:
                        retained.update(g.workers.values())
                self.generations = {g for g in self.generations
                                    if g.pending > 0 or g in (self.current, self.prepared)}
                for g in (self.current, self.prepared):
                    if g:
                        retained.update(g.workers.values())
                for key, w in list(self.workers.items()):
                    if w not in retained and w.connections == 0:
                        await w.stop()
                        self.workers.pop(key, None)
                    elif w in retained and w.process and w.process.returncode is not None:
                        with contextlib.suppress(Error, OSError):
                            await w.start()

    async def authenticate(self, reader, writer):
        header = await reader.readexactly(2)
        if header[0] != 5:
            raise Error("内部协议无效。")
        methods = await reader.readexactly(header[1])
        if 2 not in methods:
            writer.write(b"\x05\xff")
            await writer.drain()
            raise Error("内部认证为必需。")
        writer.write(b"\x05\x02")
        await writer.drain()
        version, length = await reader.readexactly(2)
        user = await reader.readexactly(length)
        password = await reader.readexactly((await reader.readexactly(1))[0])
        valid = (version == 1 and hmac.compare_digest(user, self.auth["username"].encode()) and
                 hmac.compare_digest(password, self.auth["password"].encode()))
        writer.write(b"\x01\x00" if valid else b"\x01\x01")
        await writer.drain()
        if not valid:
            raise Error("内部认证失败。")

    async def establish(self, generation, command, address):
        profile = generation.profile()
        name = profile["name"]
        if command == protocol.UDP_TUNNEL:
            if profile["type"] != "direct" or not generation.cfg["listener"]["udp"]:
                raise Error("代理池不支持 UDP。")
            choices = [(name, None)]
        elif command == protocol.CONNECT:
            if profile["type"] == "direct":
                choices = [(name, None)]
            else:
                choices = [(name, n["id"]) for n in profile["nodes"]
                           if generation.health[(name, n["id"])]["healthy"]]
                if choices:
                    offset = generation.rounds.get(name, 0) % len(choices)
                    generation.rounds[name] = generation.rounds.get(name, 0) + 1
                    choices = choices[offset:] + choices[:offset]
        else:
            raise Error("不支持此代理命令。")
        for key in choices:
            worker = generation.workers[key]
            writer = None
            worker.connections += 1
            try:
                async def attempt():
                    nonlocal writer
                    reader, writer = await asyncio.open_connection(*endpoint(worker.addr))
                    await protocol.handshake(reader, writer, self.auth)
                    result = await protocol.request(reader, writer, command, address)
                    return reader, writer, result, worker
                return await asyncio.wait_for(attempt(), 5)
            except (OSError, Error, asyncio.TimeoutError, asyncio.IncompleteReadError):
                if key in generation.health:
                    state = generation.health[key]
                    state.update(healthy=False, latencyMs=None, cooldownUntil=time.monotonic() + 10,
                                 failures=state["failures"] + 1, serial=state["serial"] + 1)
                worker.connections -= 1
                await close(writer)
            except BaseException:
                worker.connections -= 1
                await close(writer)
                raise
        raise Error("当前 profile 无可用节点。")

    async def relay(self, reader, writer):
        while True:
            data = await reader.read(65536)
            if not data:
                if writer.can_write_eof():
                    writer.write_eof()
                    await writer.drain()
                return
            writer.write(data)
            await writer.drain()

    async def connection(self, reader, writer):
        worker = None
        upstream = None
        replied = False
        try:
            await asyncio.wait_for(self.authenticate(reader, writer), 5)
            header = await asyncio.wait_for(reader.readexactly(3), 5)
            if header[0] != 5 or header[2] != 0:
                raise Error("内部请求无效。")
            address = await asyncio.wait_for(protocol.read_address(reader), 5)
            generation = self.current
            if generation is None:
                raise Error("调度器尚未就绪。")
            generation.pending += 1
            try:
                upstream_reader, upstream, reply, worker = await asyncio.wait_for(
                    self.establish(generation, header[1], address), 15)
            finally:
                generation.pending -= 1
            writer.write(reply)
            await writer.drain()
            replied = True
            # Wait for both directions: a TCP half-close must not discard the response.
            tasks = [asyncio.create_task(self.relay(reader, upstream)),
                     asyncio.create_task(self.relay(upstream_reader, writer))]
            try:
                await asyncio.gather(*tasks)
            finally:
                for task in tasks:
                    task.cancel()
                await asyncio.gather(*tasks, return_exceptions=True)
        except (OSError, Error, asyncio.TimeoutError, asyncio.IncompleteReadError):
            if not replied:
                with contextlib.suppress(OSError):
                    writer.write(FAILURE)
                    await writer.drain()
        finally:
            if worker:
                worker.connections -= 1
            await close(upstream)
            await close(writer)

    def track(self, reader, writer):
        task = asyncio.create_task(self.connection(reader, writer))
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)

    async def control(self, reader, writer):
        try:
            line = await asyncio.wait_for(reader.readline(), 10)
            if not line or len(line) > MAX_BODY:
                raise Error("控制请求大小无效。")
            req = json.loads(line)
            command = req.get("command")
            if command == "status":
                result = self.status()
            else:
                async with self.mutation_lock:
                    if command == "prepare":
                        result = await self.prepare(req["config"], req["revision"])
                    elif command == "commit":
                        result = await self.commit(req["revision"])
                    elif command == "abort":
                        self.prepared = None
                        result = self.status()
                    elif command == "probe":
                        if not self.current:
                            raise Error("运行时尚未就绪。")
                        result = await self.probe_generation(self.current, req["profile"])
                    else:
                        raise Error("控制命令无效。")
            writer.write(dump({"data": result}))
        except (Error, ValueError, KeyError, TypeError, OSError, asyncio.TimeoutError):
            writer.write(dump({"error": "运行时请求失败。"}))
        finally:
            with contextlib.suppress(OSError):
                await writer.drain()
            await close(writer)

    async def start(self):
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.directory.chmod(0o700)
        control = self.directory / "control.sock"
        control.unlink(missing_ok=True)
        self.servers.append(await asyncio.start_unix_server(self.control, str(control), limit=MAX_BODY + 1))
        control.chmod(0o600)
        self.servers.append(await asyncio.start_server(self.track, *endpoint(self.options["gateway"])))
        await self.prepare(self.initial, self.initial_revision)
        await self.commit(self.initial_revision)
        front = self.directory / "frontend.json"
        private_write(front, native.frontend(self.initial, self.options))
        self.front_log = open(self.directory / "frontend.log", "wb")
        os.chmod(self.directory / "frontend.log", 0o600)
        self.frontend = await asyncio.create_subprocess_exec(
            *self.binary, "-C", str(front), stdout=self.front_log, stderr=self.front_log)

    async def stop(self):
        self.stopping = True
        for server in self.servers:
            server.close()
            await server.wait_closed()
        for task in list(self.tasks):
            task.cancel()
        await asyncio.gather(*self.tasks, return_exceptions=True)
        if self.frontend and self.frontend.returncode is None:
            self.frontend.terminate()
            try:
                await asyncio.wait_for(self.frontend.wait(), 3)
            except asyncio.TimeoutError:
                self.frontend.kill()
                await self.frontend.wait()
        await asyncio.gather(*(w.stop() for w in self.workers.values()))
        if self.front_log:
            self.front_log.close()
        (self.directory / "control.sock").unlink(missing_ok=True)


async def serve(input_path, directory):
    value = json.loads(Path(input_path).read_text())
    runtime = Runtime(value["config"], value["options"], value["binary"], directory, value["revision"])
    stopped = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stopped.set)
    periodic = None
    waiter = None
    try:
        await runtime.start()
        periodic = asyncio.create_task(runtime.periodic())
        waiter = asyncio.create_task(stopped.wait())
        done, _ = await asyncio.wait([periodic, waiter], return_when=asyncio.FIRST_COMPLETED)
        if periodic in done:
            await periodic
    finally:
        for task in (periodic, waiter):
            if task:
                task.cancel()
        await asyncio.gather(*(t for t in (periodic, waiter) if t), return_exceptions=True)
        await runtime.stop()


def main(input_path="/etc/gost/runtime.json", directory="/run/gost-deploy"):
    os.umask(0o077)
    asyncio.run(serve(input_path, directory))
