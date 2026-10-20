"""Small, allow-listed native configurations; no mutable native routing registry."""
import copy


def service(addr, users, udp=False):
    return {"services": [{"name": "socks5", "addr": addr,
            "handler": {"type": "socks5", "auther": "users",
                        "metadata": {"udp": udp, "notls": True}},
            "listener": {"type": "tcp"}}],
            "authers": [{"name": "users", "auths": copy.deepcopy(users)}]}


def chain(cfg, node):
    connector = {"type": node["type"]}
    if node.get("auth"):
        connector["auth"] = copy.deepcopy(node["auth"])
    if node["type"] == "socks5":
        connector["metadata"] = {"notls": True}
    cfg["services"][0]["handler"]["chain"] = "upstream"
    cfg["chains"] = [{"name": "upstream", "hops": [{"name": "single",
                      "nodes": [{"name": "upstream", "addr": node["addr"],
                                 "connector": connector, "dialer": {"type": "tcp"}}]}]}]
    return cfg


def frontend(cfg, options):
    listener = cfg["listener"]
    return chain(service(listener["addr"], listener["users"], listener["udp"]),
                 {"type": "socks5", "addr": options["gateway"], "auth": options["auth"]})


def worker(addr, auth, node=None):
    cfg = service(addr, [auth], udp=node is None)
    return chain(cfg, node) if node else cfg
