"""hank.app.broker.v1 adapter; bundle this module with a restricted .hankapp."""
import base64
import json
import os
import socket
import threading


class Broker:
    def __init__(self):
        self._socket = socket.socket(fileno=os.dup(int(os.environ["HANK_APP_BROKER_FD"])))
        self._stream = self._socket.makefile("rwb", buffering=0)
        self._sequence = 0
        self._lock = threading.Lock()

    def close(self):
        self._stream.close()
        self._socket.close()

    def call(self, permission, operation, **payload):
        with self._lock:
            return self._call(permission, operation, **payload)

    def _call(self, permission, operation, **payload):
        self._sequence += 1
        request = dict(payload, version="hank.app.broker.v1", id=str(self._sequence),
                       permission=permission, operation=operation)
        if isinstance(request.get("data"), bytes):
            request["data"] = base64.b64encode(request["data"]).decode("ascii")
        data = json.dumps(request).encode("utf-8") + b"\n"
        if len(data) >= 1024 * 1024:
            raise ValueError("Broker request exceeds 1 MiB")
        self._socket.sendall(data)
        raw = self._stream.readline(1024 * 1024)
        if not raw.endswith(b"\n"):
            raise RuntimeError("Broker closed or returned an oversized response")
        response = json.loads(raw)
        if response.get("id") != str(self._sequence) or not response.get("ok"):
            raise PermissionError(response.get("error", {}).get("code", "app_broker_failed"))
        return response.get("output")

    def http(self, field, url, method="GET", data=b"", headers=None):
        result = self.call("network:" + field, "http.request", url=url,
                           method=method, data=data, headers=headers or {})
        result["data"] = base64.b64decode(result.get("data", ""))
        return result

    def read(self, field, path, offset=0):
        result = self.call("files_read:" + field, "files.read", path=path, offset=offset)
        result["data"] = base64.b64decode(result.get("data", ""))
        return result

    def write(self, field, path, data, offset=0):
        return self.call("files_write:" + field, "files.write", path=path,
                         data=data, offset=offset)
