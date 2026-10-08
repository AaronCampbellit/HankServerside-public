#!/usr/bin/env python3
"""Deliver synthetic alerts through pinned Alertmanager, STARTTLS SMTP, and a webhook."""
import base64
import http.server
import json
import os
from pathlib import Path
import socket
import socketserver
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.request

repo = Path(__file__).resolve().parents[2]
messages = []
webhooks = []
inbox_available = threading.Event()
tls_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)

class SMTP(socketserver.StreamRequestHandler):
    def handle(self):
        def reply(line):
            self.wfile.write((line + "\r\n").encode())
            self.wfile.flush()
        secure = False
        authenticated = False
        reply("220 localhost fixture")
        while raw := self.rfile.readline():
            line = raw.decode().strip()
            verb = line.split(" ")[0].upper()
            if verb in ("EHLO", "HELO"):
                reply("250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN")
            elif verb == "STARTTLS":
                reply("220 ready")
                self.connection = tls_context.wrap_socket(self.connection, server_side=True)
                self.rfile = self.connection.makefile("rb")
                self.wfile = self.connection.makefile("wb")
                secure = True
            elif verb == "AUTH":
                authenticated = secure and base64.b64decode(line.split(" ")[-1]) == b"\x00fixture-user\x00fixture-password"
                reply("235 authenticated" if authenticated else "535 rejected")
            elif verb in ("MAIL", "RCPT"):
                reply("250 accepted" if authenticated else "530 authenticate first")
            elif verb == "DATA" and authenticated:
                reply("354 send message")
                body = []
                while (raw := self.rfile.readline()) not in (b".\r\n", b""):
                    body.append(raw)
                messages.append(b"".join(body))
                reply("250 queued")
            elif verb == "QUIT":
                reply("221 goodbye")
                return
            else:
                reply("250 ok")

class Inbox(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.headers.get("Authorization") != "Bearer fixture-inbox":
            self.send_response(401)
        elif not inbox_available.is_set():
            self.send_response(503)
        else:
            webhooks.append(json.loads(body))
            self.send_response(200)
        self.end_headers()

smtp = socketserver.ThreadingTCPServer(("127.0.0.1", 0), SMTP)
smtp.daemon_threads = True
inbox = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Inbox)
for server in (smtp, inbox):
    threading.Thread(target=server.serve_forever, daemon=True).start()
with socket.socket() as reserve:
    reserve.bind(("127.0.0.1", 0))
    port = reserve.getsockname()[1]
name = "hank-alert-delivery-test-" + str(os.getpid())

def wait_until(predicate, label, seconds=70):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.25)
    raise AssertionError(label)

try:
    with tempfile.TemporaryDirectory(prefix="hank-alert-delivery-") as tmp:
        root = Path(tmp)
        root.chmod(0o755)
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost", "-keyout", str(root / "key.pem"), "-out", str(root / "cert.pem")], check=True, capture_output=True)
        tls_context.load_cert_chain(root / "cert.pem", root / "key.pem")
        for filename, content in (("smtp-password", "fixture-password"), ("hank-inbox-token", "fixture-inbox")):
            (root / filename).write_text(content)
            (root / filename).chmod(0o444)  # Synthetic credentials only.
        (root / "cert.pem").chmod(0o444)
        config = (repo / "ops/alertmanager/alertmanager-email-inbox.example.yml").read_text()
        config = config.replace("http://cloud:8080/v1/integrations/alertmanager", f"http://127.0.0.1:{inbox.server_port}/v1/integrations/alertmanager")
        config = config.replace("smtp.example.invalid:587", f"localhost:{smtp.server_address[1]}")
        config = config.replace("auth_username: hank@example.invalid", "auth_username: fixture-user")
        config = config.replace("require_tls: true", "require_tls: true\n        tls_config:\n          ca_file: /etc/alertmanager/secrets/cert.pem")
        config = config.replace("group_wait: 30s", "group_wait: 1s").replace("group_interval: 5m", "group_interval: 1s")
        # The GUI renderer can supply its exact generated configuration. Keep
        # all transport destinations and credentials synthetic in this fixture.
        generated = os.environ.get("HANK_MONITORING_TEST_CONFIG")
        if generated:
            value = json.loads(Path(generated).read_text())
            value["route"]["group_wait"] = "1s"
            value["route"]["group_interval"] = "1s"
            for receiver in value["receivers"]:
                if receiver["name"] == "hank-inbox":
                    hook = receiver["webhook_configs"][0]
                    hook["url"] = f"http://127.0.0.1:{inbox.server_port}/v1/integrations/alertmanager"
                if receiver["name"] == "email":
                    email = receiver["email_configs"][0]
                    email["smarthost"] = f"127.0.0.1:{smtp.server_address[1]}"
                    email["tls_config"] = {"server_name": "localhost", "ca_file": "/etc/alertmanager/secrets/cert.pem"}
            config = json.dumps(value)
        (root / "alertmanager.yml").write_text(config)
        (root / "alertmanager.yml").chmod(0o444)
        mount = f"{root}:/etc/alertmanager/secrets:ro"
        subprocess.run(["docker", "run", "--rm", "--network", "none", "-v", mount, "--entrypoint", "amtool", "prom/alertmanager:v0.28.1", "check-config", "/etc/alertmanager/secrets/alertmanager.yml"], check=True, capture_output=True)
        subprocess.run(["docker", "run", "-d", "--rm", "--name", name, "--network", "host", "-v", mount, "prom/alertmanager:v0.28.1", "--config.file=/etc/alertmanager/secrets/alertmanager.yml", f"--web.listen-address=127.0.0.1:{port}", "--cluster.listen-address="], check=True, capture_output=True)
        def ready():
            try:
                return urllib.request.urlopen(f"http://127.0.0.1:{port}/-/ready", timeout=1).status == 200
            except OSError:
                return False
        wait_until(ready, "Alertmanager did not start")
        alert = {"labels": {"alertname": "HankDeliveryFixture", "instance": "fixture", "severity": "warning"}, "annotations": {"summary": "Synthetic delivery test"}, "startsAt": "2026-01-01T00:00:00Z", "endsAt": "2099-01-01T00:00:00Z"}
        def send():
            request = urllib.request.Request(f"http://127.0.0.1:{port}/api/v2/alerts", data=json.dumps([alert]).encode(), headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(request, timeout=5) as response:
                assert response.status == 200
        send()
        wait_until(lambda: bool(messages), "SMTP delivery failed while inbox was unavailable")
        assert b"HankDeliveryFixture" in messages[0] and not webhooks
        inbox_available.set()
        wait_until(lambda: bool(webhooks), "inbox did not recover and receive authenticated retry")
        assert webhooks[0]["alerts"][0]["status"] == "firing"
        alert["endsAt"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        send()
        wait_until(lambda: any(x["alerts"][0]["status"] == "resolved" for x in webhooks) and len(messages) >= 2, "recovery notification missing")
        print("Alertmanager: STARTTLS/authenticated email survives inbox outage; authenticated inbox retry and both recovery deliveries passed.")
finally:
    subprocess.run(["docker", "rm", "-f", name], capture_output=True)
    for server in (smtp, inbox):
        server.shutdown()
        server.server_close()
