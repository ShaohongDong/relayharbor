"""Real SOCKS5 TCP/domain/UDP clients and local targets (no third-party dependency)."""
import contextlib
import socket
import struct
import threading


def receive(stream, size):
    data = b""
    while len(data) < size:
        part = stream.recv(size - len(data))
        if not part:
            raise AssertionError("unexpected SOCKS EOF")
        data += part
    return data


def auth_connect(host, port, auth=None, *, anonymous=False):
    stream = socket.create_connection((host, port), 3)
    try:
        stream.sendall(b"\x05\x01\x00" if anonymous else b"\x05\x01\x02")
        method = receive(stream, 2)
        if anonymous:
            # 3.2.6 requests password authentication even if the client only offers
            # no-auth; newer releases reject negotiation. Neither may accept no-auth.
            assert method in (b"\x05\xff", b"\x05\x02"), method
            stream.close()
            return None
        assert method == b"\x05\x02", method
        user, password = auth["username"].encode(), auth["password"].encode()
        stream.sendall(b"\x01" + bytes([len(user)]) + user + bytes([len(password)]) + password)
        result = receive(stream, 2)
        if result != b"\x01\x00":
            stream.close()
            return None
        return stream
    except BaseException:
        stream.close()
        raise


def reply(stream):
    header = receive(stream, 4)
    assert header[:3] == b"\x05\x00\x00", header
    kind = header[3]
    if kind == 1:
        host = socket.inet_ntop(socket.AF_INET, receive(stream, 4))
    elif kind == 4:
        host = socket.inet_ntop(socket.AF_INET6, receive(stream, 16))
    else:
        assert kind == 3, kind
        host = receive(stream, receive(stream, 1)[0]).decode()
    return host, struct.unpack("!H", receive(stream, 2))[0]


@contextlib.contextmanager
def targets():
    tcp = socket.socket()
    tcp.bind(("127.0.0.1", 0))
    tcp.listen()
    tcp.settimeout(0.2)
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.bind(("127.0.0.1", 0))
    udp.settimeout(0.2)
    stop = threading.Event()

    def serve_tcp():
        while not stop.is_set():
            try:
                stream, _ = tcp.accept()
                with stream:
                    stream.settimeout(3)
                    stream.sendall(stream.recv(4096))
            except socket.timeout:
                continue

    def serve_udp():
        while not stop.is_set():
            try:
                data, addr = udp.recvfrom(65535)
                udp.sendto(data, addr)
            except socket.timeout:
                continue

    threads = [threading.Thread(target=fn) for fn in (serve_tcp, serve_udp)]
    for thread in threads:
        thread.start()
    try:
        yield tcp.getsockname()[1], udp.getsockname()[1]
    finally:
        stop.set()
        for thread in threads:
            thread.join(4)
        tcp.close()
        udp.close()


def exercise(cfg, *, udp=False, host="127.0.0.1"):
    port = int(cfg["services"][0]["addr"].rsplit(":", 1)[1])
    auths = cfg["authers"][0]["auths"]
    with targets() as (tcp_port, udp_port):
        for auth in auths:
            stream = auth_connect(host, port, auth)
            assert stream is not None, "valid user rejected"
            with stream:
                # Send an ATYP=DOMAIN request without resolving the name in the client.
                domain = b"gost-target.test"
                stream.sendall(b"\x05\x01\x00\x03" + bytes([len(domain)]) + domain + struct.pack("!H", tcp_port))
                reply(stream)
                payload = b"gost-domain-tcp-echo"
                stream.sendall(payload)
                assert receive(stream, len(payload)) == payload
        auth_connect(host, port, anonymous=True)
        assert auth_connect(host, port, {"username": auths[0]["username"], "password": "definitely-wrong"}) is None
        assert auth_connect(host, port, {"username": "missing-user", "password": "wrong"}) is None
        stream = auth_connect(host, port, auths[0])
        with stream:
            stream.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00")
            if udp:
                relay_host, relay_port = reply(stream)
                relay_host = {"0.0.0.0": host, "::": host}.get(relay_host, relay_host)
                with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
                    client.settimeout(3)
                    packet = b"\x00\x00\x00\x01" + socket.inet_aton("127.0.0.1") + struct.pack("!H", udp_port) + b"gost-udp-echo"
                    client.sendto(packet, (relay_host, relay_port))
                    data, _ = client.recvfrom(65535)
                    assert data[:3] == b"\x00\x00\x00" and data.endswith(b"gost-udp-echo"), data
            else:
                response = receive(stream, 4)
                assert response[0] == 5 and response[1] != 0, response
