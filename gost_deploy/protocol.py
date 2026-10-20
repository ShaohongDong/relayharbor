"""Internal SOCKS handshake and end-to-end probes; no credential logging."""
import asyncio
import http.client
import ipaddress
import io
import socket
import ssl
import struct
import time
import urllib.parse

from .model import Error, endpoint, format_endpoint

CONNECT = 1
UDP_TUNNEL = 0xF3


def address_bytes(host, port):
    try:
        ip = ipaddress.ip_address(host)
        return bytes([1 if ip.version == 4 else 4]) + ip.packed + struct.pack("!H", port)
    except ValueError:
        name = host.encode("idna")
        if not 1 <= len(name) <= 255:
            raise Error("目标域名长度无效。")
        return b"\x03" + bytes([len(name)]) + name + struct.pack("!H", port)


async def read_address(reader):
    kind = await reader.readexactly(1)
    if kind == b"\x01":
        value = await reader.readexactly(4)
    elif kind == b"\x04":
        value = await reader.readexactly(16)
    elif kind == b"\x03":
        length = await reader.readexactly(1)
        if not length[0]:
            raise Error("目标地址无效。")
        value = length + await reader.readexactly(length[0])
    else:
        raise Error("目标地址类型无效。")
    return kind + value + await reader.readexactly(2)


async def handshake(reader, writer, auth):
    writer.write(b"\x05\x01\x02")
    await writer.drain()
    if await reader.readexactly(2) != b"\x05\x02":
        raise Error("内部认证协商失败。")
    user, password = auth["username"].encode(), auth["password"].encode()
    writer.write(b"\x01" + bytes([len(user)]) + user + bytes([len(password)]) + password)
    await writer.drain()
    if await reader.readexactly(2) != b"\x01\x00":
        raise Error("内部认证失败。")


async def request(reader, writer, command, address):
    writer.write(b"\x05" + bytes([command]) + b"\x00" + address)
    await writer.drain()
    header = await reader.readexactly(3)
    result = header + await read_address(reader)
    if header != b"\x05\x00\x00":
        raise Error("上游连接失败。")
    return result


def sync_read(stream, size, deadline=None):
    value = b""
    while len(value) < size:
        if deadline is not None:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError()
            stream.settimeout(remaining)
        part = stream.recv(size - len(value))
        if not part:
            raise Error("探测连接提前结束。")
        value += part
    return value


class DeadlineFile(io.RawIOBase):
    def __init__(self, stream, deadline):
        self.stream, self.deadline = stream, deadline

    def readable(self):
        return True

    def readinto(self, buffer):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError()
        self.stream.settimeout(remaining)
        return self.stream.recv_into(buffer)


class DeadlineSocket:
    def __init__(self, stream, deadline):
        self.stream, self.deadline = stream, deadline

    def makefile(self, mode):
        return io.BufferedReader(DeadlineFile(self.stream, self.deadline))


def probe(addr, auth, cfg):
    """CONNECT always completes through the selected worker before success."""
    started = time.monotonic()
    if cfg["type"] == "tcp":
        host, port = endpoint(cfg["addr"])
        url = None
    else:
        url = urllib.parse.urlsplit(cfg["url"])
        host, port = url.hostname, url.port or (443 if url.scheme == "https" else 80)
    deadline = started + cfg["timeout"]
    stream = None
    try:
        stream = socket.create_connection(endpoint(addr), cfg["timeout"])
        def remaining():
            left = deadline - time.monotonic()
            if left <= 0:
                raise TimeoutError()
            stream.settimeout(left)
        remaining()
        stream.sendall(b"\x05\x01\x02")
        if sync_read(stream, 2, deadline) != b"\x05\x02":
            raise Error("探测认证失败。")
        user, password = auth["username"].encode(), auth["password"].encode()
        stream.sendall(b"\x01" + bytes([len(user)]) + user + bytes([len(password)]) + password)
        if sync_read(stream, 2, deadline) != b"\x01\x00":
            raise Error("探测认证失败。")
        remaining()
        stream.sendall(b"\x05\x01\x00" + address_bytes(host, port))
        if sync_read(stream, 3, deadline) != b"\x05\x00\x00":
            raise Error("探测 CONNECT 失败。")
        kind = sync_read(stream, 1, deadline)[0]
        length = {1: 4, 4: 16}.get(kind)
        if kind == 3:
            length = sync_read(stream, 1, deadline)[0]
        if length is None:
            raise Error("探测响应无效。")
        sync_read(stream, length + 2, deadline)
        if url:
            remaining()
            if url.scheme == "https":
                stream = ssl.create_default_context().wrap_socket(stream, server_hostname=host)
            remaining()
            path = urllib.parse.urlunsplit(("", "", url.path or "/", url.query, ""))
            host_header = format_endpoint(host, port)
            stream.sendall(("GET {} HTTP/1.1\r\nHost: {}\r\nConnection: close\r\n\r\n".format(
                path, host_header)).encode("ascii"))
            response = http.client.HTTPResponse(DeadlineSocket(stream, deadline))
            response.begin()
            if response.status != cfg["expectedStatus"]:
                raise Error("HTTP 探测状态不匹配。")
            response.close()
        return {"healthy": True, "latencyMs": round((time.monotonic() - started) * 1000, 2)}
    except (OSError, Error, ValueError, http.client.HTTPException):
        return {"healthy": False, "latencyMs": None}
    finally:
        if stream is not None:
            stream.close()
