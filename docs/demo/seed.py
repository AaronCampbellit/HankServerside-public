#!/usr/bin/env python3
"""Seed a new, loopback-only Hank demo through its real HTTP API."""

import http.cookiejar
import json
import pathlib
import secrets
import sys
import urllib.parse
import urllib.request

base_url, output_directory = sys.argv[1:]
parsed = urllib.parse.urlparse(base_url)
if parsed.scheme != "http" or parsed.hostname not in {"localhost", "127.0.0.1"}:
    raise SystemExit("This fixture only seeds an HTTP loopback server.")
output = pathlib.Path(output_directory)
output.mkdir(mode=0o700, parents=True, exist_ok=True)
jar = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def request(path, body=None, method=None):
    headers = {"Content-Type": "application/json"}
    csrf = next((cookie.value for cookie in jar if cookie.name == "hank_csrf"), "")
    if csrf:
        headers["X-Hank-CSRF-Token"] = csrf
    payload = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base_url + path, payload, headers, method=method)
    with client.open(req, timeout=30) as response:
        return json.load(response)


email = "alex@hank.example"
password = secrets.token_urlsafe(24)
request("/v1/auth/register", {"email": email, "password": password})
request("/v1/home", {"name": "Demo Home"}, method="PUT")

notes = [
    {
        "title": "Welcome to Hank",
        "page_type": "text",
        "pinned": True,
        "body_markdown": "# Your home, connected\n\nThis is a disposable demonstration of the real Hank platform.\n\n## Explore the workspace\n\n- Open **Launch checklist** to see the Kanban board.\n- Edit this note; changes are saved through the server API.\n- Browse **Agents** and **Settings** to see the control plane.\n\n## How it fits together\n\nClients connect to HankServerside over HTTPS and WebSocket. Hank Agents connect outbound and keep local credentials on their own machines.\n\nNo real accounts, managed devices, home integrations, or AI providers are connected in this demo.\n\n#portfolio #hank",
    },
    {
        "title": "Weekend projects",
        "page_type": "text",
        "body_markdown": "# Weekend projects\n\n- Label the workshop shelves.\n- Organize family photos.\n- Review the backup schedule.\n\nAll entries in this workspace are synthetic.\n\n#home",
    },
    {
        "title": "Launch checklist",
        "page_type": "kanban",
        "body_markdown": "",
        "board": {
            "columns": [
                {"id": "plan", "title": "Planned", "role": "planning", "sort_order": 0, "cards": [
                    {"id": "c1", "text": "Connect a synthetic agent", "priority": "medium", "tags": ["agents"], "sort_order": 0},
                    {"id": "c2", "text": "Choose shared folders", "priority": "low", "tags": ["files"], "sort_order": 1},
                ]},
                {"id": "active", "title": "In progress", "role": "active", "sort_order": 1, "cards": [
                    {"id": "c3", "text": "Review the Notes workspace", "priority": "high", "tags": ["demo"], "sort_order": 0},
                ]},
                {"id": "review", "title": "Review", "role": "review", "sort_order": 2, "cards": [
                    {"id": "c4", "text": "Check the architecture diagram", "priority": "medium", "tags": ["docs"], "sort_order": 0},
                ]},
                {"id": "done", "title": "Complete", "role": "complete", "sort_order": 3, "cards": [
                    {"id": "c5", "text": "Create an isolated demo Home", "priority": "low", "tags": ["setup"], "sort_order": 0},
                ]},
            ]
        },
    },
]
for note in notes:
    request("/v1/me/notes", {"body_format": "markdown", "parent_id": "", **note})

credentials = {"email": email, "password": password}
credentials_path = output / "credentials.json"
credentials_path.write_text(json.dumps(credentials))
credentials_path.chmod(0o600)
login_path = output / "login.txt"
login_path.write_text(f"Email: {email}\nPassword: {password}\n")
login_path.chmod(0o600)
print(f"Seeded {len(notes)} synthetic notes. Login details: {login_path}")
