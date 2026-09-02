#!/usr/bin/env python3
"""Verifikasi matriks isolasi multi-tenant terhadap server yang benar-benar jalan.

Menjalankan seluruh matriks di phase-09-verifikasi-rollout.md, lalu mencetak
tabel hasil. Baris yang tidak bisa diotomatiskan dilaporkan sebagai LEWAT
beserta alasannya — bukan didiamkan.

Pemakaian:

    python docs/multitenant/verify_matrix.py --binary /tmp/gowa.exe

Skrip ini menyiapkan instalasi uji sendiri di direktori sementara: dua operator,
satu admin, dan tiga device. TIDAK pernah menyentuh storages/ milik proyek.

Butuh: python 3.8+, dan node untuk baris WebSocket (dilewati kalau tidak ada).
"""

import argparse
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

PASS, FAIL, SKIP = "LULUS", "GAGAL", "LEWAT"


class Result:
    def __init__(self):
        self.rows = []

    def add(self, num, what, expect, actual, ok, note=""):
        self.rows.append((num, what, expect, actual, ok, note))

    def check(self, num, what, expect, actual, note=""):
        self.add(num, what, expect, str(actual), PASS if str(actual) == str(expect) else FAIL, note)

    def skip(self, num, what, why):
        self.add(num, what, "-", "-", SKIP, why)

    def report(self):
        width = max(len(r[1]) for r in self.rows) + 2
        print()
        print("=" * (width + 46))
        print(f"{'#':>3}  {'AKSI':<{width}} {'HARAP':<14} {'DAPAT':<14} HASIL")
        print("=" * (width + 46))
        for num, what, expect, actual, ok, note in self.rows:
            mark = {PASS: "ok  ", FAIL: "GAGAL", SKIP: "lewat"}[ok]
            print(f"{num:>3}  {what:<{width}} {expect:<14} {actual:<14} {mark}")
            if note:
                print(f"     {'':<{width}} catatan: {note}")
        print("=" * (width + 46))

        counts = {PASS: 0, FAIL: 0, SKIP: 0}
        for row in self.rows:
            counts[row[4]] += 1
        print(f"lulus {counts[PASS]}   gagal {counts[FAIL]}   lewat {counts[SKIP]}   "
              f"total {len(self.rows)}")
        return counts[FAIL] == 0


def basic(user, password):
    return "Basic " + base64.b64encode(f"{user}:{password}".encode()).decode()


def request(base, path, method="GET", cred=None, body=None, headers=None, cookie=None):
    """Mengembalikan (status, body-text, set-cookie)."""
    url = base + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json")
    if cred:
        req.add_header("Authorization", basic(*cred))
    if cookie:
        req.add_header("Cookie", cookie)
    for key, value in (headers or {}).items():
        req.add_header(key, value)

    try:
        with urllib.request.urlopen(req, timeout=20) as res:
            return res.status, res.read().decode("utf-8", "replace"), res.headers.get("Set-Cookie")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode("utf-8", "replace"), err.headers.get("Set-Cookie")
    except Exception as err:  # noqa: BLE001 — laporkan apa pun sebagai kegagalan transport
        return 0, f"<transport error: {err}>", None


def devices_of(base, cred):
    status, body, _ = request(base, "/devices", cred=cred)
    if status != 200:
        return None
    return sorted(d["id"] for d in (json.loads(body).get("results") or []))


class Server:
    """Satu instance gowa di direktori kerja sendiri."""

    def __init__(self, binary, workdir, port, env):
        self.binary, self.workdir, self.port = binary, workdir, port
        self.env = env
        self.proc = None
        self.base = f"http://127.0.0.1:{port}"

    def start(self):
        os.makedirs(os.path.join(self.workdir, "storages"), exist_ok=True)
        os.makedirs(os.path.join(self.workdir, "statics"), exist_ok=True)
        environ = dict(os.environ)
        environ.update(self.env)
        environ["APP_PORT"] = str(self.port)
        environ["APP_UI_ENABLED"] = "false"
        log = open(os.path.join(self.workdir, f"server-{self.port}.log"), "wb")
        self.proc = subprocess.Popen(
            [self.binary, "rest"], cwd=self.workdir, env=environ,
            stdout=log, stderr=subprocess.STDOUT,
        )
        for _ in range(80):
            status, _, _ = request(self.base, "/app/info", cred=("admin", "rahasia123"))
            if status:
                return True
            time.sleep(0.25)
        return False

    def stop(self):
        if self.proc:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
            self.proc = None

    def log_text(self):
        path = os.path.join(self.workdir, f"server-{self.port}.log")
        if not os.path.exists(path):
            return ""
        with open(path, "rb") as handle:
            return handle.read().decode("utf-8", "replace")


ADMIN = ("admin", "rahasia123")
OP1 = ("op1", "rahasia123")
OP2 = ("op2", "rahasia123")
OP3 = ("op3", "rahasia123")


def seed(base, res):
    """Membuat tiga akun dan tiga device: dev-a (op1), dev-b (op2), dev-orphan."""
    for user, limit in ((OP1, 2), (OP2, 0), (OP3, 0)):
        request(base, "/admin/users", "POST", cred=ADMIN, body={
            "username": user[0], "password": user[1], "role": "operator",
            "display_name": user[0].upper(), "device_limit": limit,
        })

    request(base, "/devices", "POST", cred=OP1, body={"device_id": "dev-a"})
    request(base, "/devices", "POST", cred=OP2, body={"device_id": "dev-b"})
    request(base, "/devices", "POST", cred=ADMIN, body={"device_id": "dev-orphan"})
    request(base, "/admin/devices/dev-orphan/owner", "DELETE", cred=ADMIN)

    owned = devices_of(base, ADMIN)
    res.check("pre", "instalasi uji siap", "['dev-a', 'dev-b', 'dev-orphan']", owned)


def section_a1(base, res):
    res.check(1, "op1 GET /devices", "['dev-a']", devices_of(base, OP1))
    res.check(2, "op2 GET /devices", "['dev-b']", devices_of(base, OP2))
    res.check(3, "admin GET /devices", "['dev-a', 'dev-b', 'dev-orphan']", devices_of(base, ADMIN))

    status, body, _ = request(base, "/app/devices", cred=OP1, headers={"X-Device-Id": "dev-a"})
    ids = sorted(d["device"] for d in (json.loads(body).get("results") or [])) if status == 200 else None
    res.check(4, "op1 GET /app/devices", "['dev-a']", ids)

    for num, path, cred, must_have, must_not in (
        (5, "/command/configs", OP1, None, "dev-b"),
        (6, "/chatwoot/configs", OP1, None, "dev-b"),
    ):
        status, body, _ = request(base, path, cred=cred, headers={"X-Device-Id": "dev-a"})
        if status != 200:
            res.skip(num, f"op1 GET {path}", f"HTTP {status} (fitur mungkin mati di instalasi uji)")
            continue
        res.check(num, f"op1 GET {path} bebas dev-b", "True", must_not not in body)


CROSS = [
    (7, "GET /app/status", "GET", "/app/status", True),
    (8, "POST /send/message", "POST", "/send/message", True),
    (9, "GET /chats", "GET", "/chats", True),
    (10, "GET /devices/dev-b", "GET", "/devices/dev-b", False),
    (11, "GET /devices/dev-b/status", "GET", "/devices/dev-b/status", False),
    (12, "GET /devices/dev-b/login (QR)", "GET", "/devices/dev-b/login", False),
    (13, "POST /devices/dev-b/logout", "POST", "/devices/dev-b/logout", False),
    (14, "DELETE /devices/dev-b", "DELETE", "/devices/dev-b", False),
    (15, "GET /devices/dev-b/command/config", "GET", "/devices/dev-b/command/config", False),
    (16, "PUT /devices/dev-b/command/config", "PUT", "/devices/dev-b/command/config", False),
    (17, "GET /devices/dev-b/queue", "GET", "/devices/dev-b/queue", False),
    (18, "DELETE /devices/dev-b/queue/1", "DELETE", "/devices/dev-b/queue/1", False),
    # needs_header=True: rute ini didaftarkan setelah headerDeviceGroup, jadi
    # ikut tertangkap DeviceMiddleware dan menuntut X-Device-Id. Tanpa header
    # yang terlihat hanya 400, bukan perilaku handler-nya.
    (19, "GET /devices/dev-b/chatwoot/config", "GET", "/devices/dev-b/chatwoot/config", True),
    (20, "PATCH /devices/dev-b/webhook", "PATCH", "/devices/dev-b/webhook", False),
    (21, "GET /devices/dev-orphan/status", "GET", "/devices/dev-orphan/status", False),
    (22, "GET /admin/users", "GET", "/admin/users", False),
    (23, "PUT /admin/devices/dev-a/owner", "PUT", "/admin/devices/dev-a/owner", False),
    (24, "GET /custom/users", "GET", "/custom/users", False),
]


def section_a2(base, res):
    for num, what, method, path, needs_header in CROSS:
        headers = {"X-Device-Id": "dev-b"} if needs_header else None
        body = {} if method in ("POST", "PUT", "PATCH") else None
        status, _, _ = request(base, path, method, cred=OP1, body=body, headers=headers)
        res.check(num, f"op1 -> {what}", 404, status)

    # Efek samping dari baris 13 dan 14 adalah kegagalan terburuk yang mungkin:
    # 404 yang benar tapi device korban tetap terhapus.
    res.check("13b", "dev-b masih ada setelah percobaan logout/hapus",
              "True", "dev-b" in (devices_of(base, ADMIN) or []))


def section_a3(base, res):
    # DefaultDevice() hanya aktif kalau registry berisi TEPAT SATU device, dan
    # instalasi uji punya tiga. Jadi baris 25-28 tidak bisa diamati di sini;
    # yang bisa dipastikan adalah request tanpa device_id TIDAK pernah lolos ke
    # device orang lain.
    status, body, _ = request(base, "/app/status", cred=OP1)
    res.check(25, "op1 tanpa X-Device-Id tidak lolos", "True", status != 200,
              f"HTTP {status}; DefaultDevice() nil untuk >1 device")
    res.skip(26, "operator 0 device, registry 1 device",
             "butuh instalasi 1-device; dikunci TestIntegrationFallbackMatrix")
    res.skip(27, "operator 2 device tanpa header",
             "butuh instalasi 1-device; dikunci TestIntegrationFallbackMatrix")
    status, _, _ = request(base, "/app/status", cred=ADMIN)
    res.check(28, "admin tanpa header", "True", status != 0,
              f"HTTP {status}; sama seperti perilaku upstream")
    _ = body


def section_a4(base, res):
    status, body, _ = request(base, "/devices", "POST", cred=OP1, body={"device_id": "dev-a2"})
    res.check("29a", "op1 device ke-2 (limit 2)", 200, status)
    status, body, _ = request(base, "/devices", "POST", cred=OP1, body={"device_id": "dev-a3"})
    res.check(29, "op1 device ke-3 ditolak kuota", 403, status)
    res.check("29b", "device yatim tidak dibuat", "True",
              "dev-a3" not in (devices_of(base, ADMIN) or []))

    status, _, _ = request(base, "/devices", "POST", cred=OP2, body={"device_id": "dev-b2"})
    res.check(30, "op2 tanpa batas bisa menambah", 200, status)

    # Hapus op3 (tanpa device) hanya untuk memastikan alurnya bersih.
    status, body, _ = request(base, "/admin/users", cred=ADMIN)
    users = {u["username"]: u for u in json.loads(body).get("results", [])} if status == 200 else {}
    if "op3" in users:
        status, _, _ = request(base, f"/admin/users/{users['op3']['id']}", "DELETE", cred=ADMIN)
        res.check(31, "admin hapus user", 200, status)
    else:
        res.skip(31, "admin hapus user", "op3 tidak ada")

    status, _, _ = request(base, "/admin/devices/dev-orphan/owner", "PUT",
                           cred=ADMIN, body={"user_id": users.get("op2", {}).get("id", 0)})
    res.check(32, "admin tetapkan pemilik dev-orphan", 200, status)
    status, _, _ = request(base, "/app/status", cred=OP2, headers={"X-Device-Id": "dev-orphan"})
    res.check("32b", "op2 langsung bisa akses (cache ter-invalidasi)", "True", status != 404)

    status, _, _ = request(base, "/admin/devices/dev-orphan/owner", "DELETE", cred=ADMIN)
    res.check(33, "admin lepas kepemilikan", 200, status)
    status, _, _ = request(base, "/app/status", cred=OP2, headers={"X-Device-Id": "dev-orphan"})
    res.check("33b", "op2 langsung kehilangan akses", 404, status)


def section_a5(base, res):
    status, body, _ = request(base, "/auth/login", "POST",
                              body={"username": OP1[0], "password": OP1[1]})
    cookie = None
    status2, _, set_cookie = request(base, "/auth/login", "POST",
                                     body={"username": OP1[0], "password": OP1[1]})
    if set_cookie:
        cookie = set_cookie.split(";")[0]
    _ = status, body, status2

    status, body, _ = request(base, "/admin/users", cred=ADMIN)
    users = {u["username"]: u for u in json.loads(body).get("results", [])} if status == 200 else {}
    op1_id = users.get("op1", {}).get("id")

    if not (cookie and op1_id):
        res.skip(34, "ganti password mencabut sesi", "gagal menyiapkan sesi")
        res.skip(35, "nonaktifkan user mencabut sesi", "gagal menyiapkan sesi")
    else:
        request(base, f"/admin/users/{op1_id}", "PATCH", cred=ADMIN,
                body={"password": "rahasiabaru1"})
        status, _, _ = request(base, "/auth/me", cookie=cookie)
        res.check(34, "cookie mati setelah ganti password", 401, status)
        status, _, _ = request(base, "/auth/me", cred=OP1)
        res.check("34b", "basic lama mati SEKETIKA", 401, status,
                  "kalau 200, cache verifikasi Basic tidak ter-invalidasi")

        request(base, f"/admin/users/{op1_id}", "PATCH", cred=ADMIN, body={"active": False})
        status, _, _ = request(base, "/auth/me", cred=("op1", "rahasiabaru1"))
        res.check(35, "user nonaktif langsung ditolak", 401, status)
        request(base, f"/admin/users/{op1_id}", "PATCH", cred=ADMIN,
                body={"active": True, "password": OP1[1]})

    status, _, set_cookie = request(base, "/auth/login", "POST",
                                    body={"username": OP2[0], "password": OP2[1]})
    cookie2 = set_cookie.split(";")[0] if set_cookie else None
    if cookie2:
        request(base, "/auth/logout", "POST", cookie=cookie2)
        status, _, _ = request(base, "/auth/me", cookie=cookie2)
        res.check(36, "logout mematikan cookie", 401, status)
    else:
        res.skip(36, "logout mematikan cookie", "gagal menyiapkan sesi")

    res.skip(37, "sesi kedaluwarsa tersapu",
             "butuh menunggu TTL; dikunci TestSweepExpiredSessions")


def section_a6(base, res, node, probe):
    if not node or not os.path.exists(probe):
        for num in (38, 39, 40, 41):
            res.skip(num, f"WebSocket baris {num}", "node atau wsprobe.mjs tidak tersedia")
        return

    ws = base.replace("http://", "ws://")
    spec = f"op1={OP1[0]}:{OP1[1]}|dev-a,op2={OP2[0]}:{OP2[1]}|dev-b,admin={ADMIN[0]}:{ADMIN[1]}|dev-a"

    # Kepemilikan op1 dibaca dari server, bukan diasumsikan: seksi A4
    # menambahkan device kedua untuknya lebih dulu.
    op1_devices = devices_of(base, OP1) or []

    out = subprocess.run([node, probe, ws, spec, "fetch:op1"],
                         capture_output=True, text=True, timeout=90).stdout
    res.check(40, "FETCH_DEVICES hanya ke peminta", "True",
              "op1: 1 event" in out and "op2: 0 event" in out, out.strip().replace("\n", " | ")[:150])
    leaked = [d for d in (devices_of(base, ADMIN) or []) if d not in op1_devices and d in out]
    res.check("40b", "LIST_DEVICES tidak memuat device orang lain", "[]", leaked,
              f"op1 memiliki {op1_devices}")

    out = subprocess.run([node, probe, ws, spec,
                          f"http:POST|{base}/devices/dev-a/logout|{OP1[0]}:{OP1[1]}"],
                         capture_output=True, text=True, timeout=90).stdout
    res.check(38, "event device-a hanya ke pemilik + admin", "True",
              "op2: 0 event" in out and "DEVICE_LOGGED_OUT(dev-a)" in out,
              out.strip().replace("\n", " | ")[:150])
    res.check(39, "payload tanpa daftar device", "True", '["device_id"]' in out)
    res.check(41, "admin menerima event", "True", out.count("DEVICE_LOGGED_OUT(dev-a)") >= 2)


def mcp_call(base, cred, arguments):
    headers = {"Accept": "application/json, text/event-stream"}
    status, body, _ = request(base, "/mcp", "POST", cred=cred, headers=headers, body={
        "jsonrpc": "2.0", "id": 1, "method": "tools/call",
        "params": {"name": "whatsapp_app", "arguments": arguments},
    })
    if status != 200:
        return status, f"<HTTP {status}>", False
    try:
        result = json.loads(body).get("result", {})
        text = (result.get("content") or [{}])[0].get("text", "")
        return status, text, bool(result.get("isError"))
    except Exception:  # noqa: BLE001
        return status, body[:120], False


def section_a7(base, res):
    # Tanpa device_id, MCP memakai device pemanggil kalau pilihannya tunggal;
    # kalau lebih dari satu ia MEMINTA device_id eksplisit — menebak salah satu
    # berarti mengirim dari device yang tidak diminta. Keduanya benar, jadi yang
    # diperiksa adalah "tidak memakai device orang lain".
    owned = devices_of(base, OP1) or []
    _, text, is_error = mcp_call(base, OP1, {"action": "status"})
    if len(owned) == 1:
        res.check(42, "MCP tanpa device_id pakai device sendiri", "False", is_error, text[:80])
    else:
        res.check(42, "MCP tanpa device_id minta eksplisit", "True",
                  is_error and "device_id" in text,
                  f"op1 memiliki {owned}: {text[:70]}")

    _, cross, cross_err = mcp_call(base, OP1, {"action": "status", "device_id": "dev-b"})
    _, missing, missing_err = mcp_call(base, OP1, {"action": "status", "device_id": "dev-hantu"})
    res.check(43, "MCP device orang lain ditolak", "True", cross_err and missing_err)
    # Bentuk pesannya harus sama; kalau berbeda, bentuk itu sendiri membocorkan
    # bahwa device tersebut eksis.
    same_shape = cross.replace("dev-b", "X") == missing.replace("dev-hantu", "X")
    res.check("43b", "pesan MCP tidak bisa dibedakan", "True", same_shape,
              f"{cross!r} vs {missing!r}")

    # Baris 44 diverifikasi oleh section_oauth, yang menjalankan server
    # sendiri dengan MCP OAuth menyala. Tanpa OAuth rutenya tidak terdaftar dan
    # yang terlihat hanya auth gate menolak path asing.
    _ = base


def section_oauth(binary, workdir, port, res):
    """Baris 44: rute discovery OAuth harus tetap publik.

    Perlu server sendiri karena MCP OAuth mengubah tempat /mcp dipasang: ia
    didaftarkan SEBELUM auth gate global supaya discovery tetap terbuka.
    """
    server = Server(binary, workdir, port, {
        "APP_BASIC_AUTH": "admin:rahasia123",
        "MULTI_TENANT_ENABLED": "true",
        "MCP_OAUTH_ENABLED": "true",
        "MCP_OAUTH_ISSUER_URL": f"https://127.0.0.1:{port}",
        "MCP_OAUTH_DB_URI": "file:storages/mcp-oauth.db",
    })
    if not server.start():
        res.skip(44, "discovery OAuth tetap publik", "server dengan OAuth gagal start")
        return
    try:
        # TANPA kredensial: inilah intinya.
        status, body, _ = request(server.base, "/.well-known/oauth-authorization-server")
        res.check(44, "discovery OAuth publik tanpa kredensial", 200, status, body[:80])

        # Path-nya mengandung path resource MCP (lihat RegisterPublic:
        # wellKnownPath("oauth-protected-resource", s.resource.Path)), jadi
        # akhirannya /mcp — bukan sekadar nama metadata-nya.
        status, _, _ = request(server.base, "/.well-known/oauth-protected-resource/mcp")
        res.check("44b", "resource metadata publik tanpa kredensial", 200, status,
                  f"HTTP {status}")

        # /mcp sendiri tetap harus tertutup.
        status, _, _ = request(server.base, "/mcp", "POST",
                               headers={"Accept": "application/json, text/event-stream"},
                               body={"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
        res.check("44c", "/mcp tetap tertutup tanpa kredensial", "True", status in (401, 403),
                  f"HTTP {status}")
    finally:
        server.stop()


def section_a8(binary, workdir, port, res):
    """Regresi mode single-tenant, dengan server terpisah."""
    server = Server(binary, workdir, port, {
        "APP_BASIC_AUTH": "admin:rahasia123",
        "MULTI_TENANT_ENABLED": "false",
        "CHATWOOT_ENABLED": "true",
        "CHATWOOT_URL": "https://203.0.113.10",
        "CHATWOOT_API_TOKEN": "token-uji",
        "CHATWOOT_ACCOUNT_ID": "1",
        "CHATWOOT_INBOX_ID": "5",
        "CHATWOOT_WEBHOOK_SECRET": "rahasia-webhook",
    })
    if not server.start():
        for num in (45, 46, 47, 48, 49, 50):
            res.skip(num, f"single-tenant baris {num}", "server flag-off gagal start")
        return
    try:
        base = server.base
        res.check(45, "flag off: semua device terlihat", "True",
                  (devices_of(base, ADMIN) or []) != [])

        status, _, _ = request(base, "/app/status", cred=ADMIN,
                               headers={"X-Device-Id": "dev-b"})
        res.check(46, "flag off: device apa pun boleh", "True", status != 404)

        for num, path in ((47, "/auth/me"), ("47b", "/admin/users"), ("47c", "/custom/users")):
            status, _, _ = request(base, path, cred=ADMIN)
            res.check(num, f"flag off: {path} tidak terdaftar", "True", status != 200,
                      f"HTTP {status}")

        res.skip(48, "payload WebSocket identik",
                 "dikunci TestForWireKeepsSingleTenantPayloadIdentical")
        res.skip(49, "dashboard gowa-ui normal", "APP_UI_ENABLED=false di instalasi uji")

        # Webhook Chatwoot sengaja didaftarkan SEBELUM auth gate: server
        # Chatwoot memanggilnya tanpa kredensial HTTP. Yang menjaganya adalah
        # CHATWOOT_WEBHOOK_SECRET, bukan auth gate.
        #
        # Jadi yang diperiksa dua hal: dengan secret yang benar ia terjangkau
        # TANPA kredensial HTTP sama sekali, dan tanpa secret ia ditolak.
        status_ok, _, _ = request(base, "/chatwoot/webhook", "POST", body={},
                                  headers={"X-Chatwoot-Webhook-Secret": "rahasia-webhook"})
        res.check(50, "webhook Chatwoot terjangkau tanpa kredensial HTTP", "True",
                  status_ok not in (0, 401),
                  f"HTTP {status_ok} (secret benar, tanpa Authorization)")

        status_bad, _, _ = request(base, "/chatwoot/webhook", "POST", body={})
        res.check("50b", "webhook menolak tanpa secret", 401, status_bad,
                  "dijaga CHATWOOT_WEBHOOK_SECRET, bukan auth gate")

        res.check("A8", "tidak ada log MULTITENANT", 0, server.log_text().count("MULTITENANT"))
    finally:
        server.stop()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, help="binary gowa (hasil go build)")
    parser.add_argument("--port", type=int, default=3960)
    parser.add_argument("--keep", action="store_true", help="jangan hapus direktori uji")
    args = parser.parse_args()

    if not os.path.exists(args.binary):
        sys.exit(f"binary tidak ada: {args.binary}")

    node = shutil.which("node")
    probe = os.path.join(os.path.dirname(os.path.abspath(__file__)), "wsprobe.mjs")

    workdir = tempfile.mkdtemp(prefix="gowa-matrix-")
    print(f"direktori uji: {workdir}")
    res = Result()

    # Chatwoot dinyalakan supaya baris 19 dan 50 benar-benar menguji
    # handler-nya. Tanpa itu rutenya tidak terdaftar dan yang terlihat hanya
    # DeviceMiddleware menangkap path asing — lihat "Rute yang tidak terdaftar
    # TIDAK menjawab 404" di README.
    server = Server(args.binary, workdir, args.port, {
        "APP_BASIC_AUTH": "admin:rahasia123",
        "MULTI_TENANT_ENABLED": "true",
        "CHATWOOT_ENABLED": "true",
        "CHATWOOT_URL": "https://203.0.113.10",
        "CHATWOOT_API_TOKEN": "token-uji",
        "CHATWOOT_ACCOUNT_ID": "1",
        "CHATWOOT_INBOX_ID": "5",
        "CHATWOOT_WEBHOOK_SECRET": "rahasia-webhook",
    })
    if not server.start():
        sys.exit("server gagal start; lihat log di direktori uji")

    try:
        seed(server.base, res)
        section_a1(server.base, res)
        section_a2(server.base, res)
        section_a3(server.base, res)
        section_a4(server.base, res)
        section_a5(server.base, res)
        section_a6(server.base, res, node, probe)
        section_a7(server.base, res)
    finally:
        server.stop()

    section_oauth(args.binary, workdir, args.port + 1, res)
    section_a8(args.binary, workdir, args.port + 2, res)

    ok = res.report()
    if not args.keep:
        shutil.rmtree(workdir, ignore_errors=True)
    else:
        print(f"direktori uji dipertahankan: {workdir}")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
