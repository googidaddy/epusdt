"""Opt-in real Nginx ingress tests, using loopback and disposable certificates.

EPUSDT_TEST_NGINX=1 python3 -B -m unittest discover -s ops/secure -p test_nginx_ingress.py
No production certificates/configuration or real upstream is used. The profile
has no management virtual host; operator routes must stay unproxied.
"""
import http.client
import http.server
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import unittest


class Upstream(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        result = json.dumps({"path": self.path, "method": self.command,
                             "host": self.headers.get("Host"),
                             "assertion": self.headers.get("Cf-Access-Jwt-Assertion")}).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(result)))
        self.end_headers()
        self.wfile.write(result)

    do_POST = do_GET

    def log_message(self, *_args):
        pass


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


@unittest.skipUnless(os.environ.get("EPUSDT_TEST_NGINX") == "1", "explicit Nginx integration opt-in required")
class NginxIngressTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        nginx = shutil.which("nginx")
        if not nginx or not shutil.which("openssl"):
            raise RuntimeError("opted-in ingress test requires nginx and openssl")
        temp = tempfile.TemporaryDirectory(prefix="epusdt-ingress-")
        cls.addClassCleanup(temp.cleanup)
        root = Path(temp.name)
        cls.root = root
        # Only synthetic keys are generated, retained under a private temporary root.
        def openssl(*args):
            subprocess.run(["openssl", *args], cwd=root, check=True, capture_output=True, timeout=15)
        openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", "ca.key", "-out", "ca.pem", "-days", "1", "-subj", "/CN=epusdt-test-ca")
        for name in ("server", "client"):
            openssl("req", "-newkey", "rsa:2048", "-nodes", "-keyout", name + ".key", "-out", name + ".csr", "-subj", "/CN=" + name)
            openssl("x509", "-req", "-in", name + ".csr", "-CA", "ca.pem", "-CAkey", "ca.key", "-CAcreateserial", "-out", name + ".pem", "-days", "1")
        upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        cls.addClassCleanup(upstream.server_close)
        cls.addClassCleanup(upstream.shutdown)
        threading.Thread(target=upstream.serve_forever, daemon=True).start()
        cls.http_port = free_port()
        cls.tls_port = free_port()
        while cls.tls_port == cls.http_port:
            cls.tls_port = free_port()
        cls.ipv6 = False
        try:
            with socket.socket(socket.AF_INET6) as listener:
                listener.bind(("::1", 0))
                cls.ipv6 = True
        except OSError:
            pass
        template = Path(__file__).with_name("nginx.conf.example").read_text(encoding="utf-8")
        template = template.replace("listen 80;", "listen 127.0.0.1:%s;" % cls.http_port)
        template = template.replace("listen 443 ssl;", "listen 127.0.0.1:%s ssl;" % cls.tls_port)
        template = template.replace("listen [::]:80;", "listen [::1]:%s;" % cls.http_port if cls.ipv6 else "")
        template = template.replace("listen [::]:443 ssl;", "listen [::1]:%s ssl;" % cls.tls_port if cls.ipv6 else "")
        template = template.replace("127.0.0.1:18081", "127.0.0.1:%s" % upstream.server_port)
        for original, name in (("REPLACE-origin.pem", "server.pem"), ("REPLACE-origin.key", "server.key"), ("REPLACE-cloudflare-origin-pull-ca.pem", "ca.pem")):
            template = template.replace("/etc/nginx/ssl/" + original, str(root / name))
        config = "worker_processes 1;\nmaster_process off;\ndaemon off;\npid " + str(root / "nginx.pid") + ";\nerror_log stderr crit;\nevents {worker_connections 64;}\nhttp {access_log off;\n" + template + "\n}\n"
        (root / "nginx.conf").write_text(config, encoding="utf-8")
        subprocess.run([nginx, "-t", "-p", str(root) + "/", "-c", str(root / "nginx.conf")], check=True, capture_output=True, timeout=10)
        cls.server = subprocess.Popen([nginx, "-p", str(root) + "/", "-c", str(root / "nginx.conf")], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        def stop():
            if cls.server.poll() is None:
                cls.server.terminate()
                try:
                    cls.server.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    cls.server.kill()
                    cls.server.wait(timeout=5)
        cls.addClassCleanup(stop)
        deadline = time.monotonic() + 5
        while True:
            if cls.server.poll() is not None:
                raise RuntimeError("isolated Nginx exited during startup")
            try:
                with socket.create_connection(("127.0.0.1", cls.http_port), timeout=0.2):
                    break
            except OSError:
                if time.monotonic() >= deadline:
                    raise RuntimeError("isolated Nginx startup deadline exceeded")
                time.sleep(0.05)

    def request(self, host, path, method="GET", *, client=True, sni=None, ip="127.0.0.1", tls=True, headers=None):
        connection = http.client.HTTPConnection(ip, self.tls_port if tls else self.http_port, timeout=3)
        if tls:
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
            context.check_hostname = False  # Synthetic server certificate; trust only our test CA.
            context.load_verify_locations(str(self.root / "ca.pem"))
            if client:
                context.load_cert_chain(str(self.root / "client.pem"), str(self.root / "client.key"))
            raw = socket.create_connection((ip, self.tls_port), timeout=3)
            connection.sock = context.wrap_socket(raw, server_hostname=sni or host)
        try:
            connection.request(method, path, headers={"Host": host, **(headers or {})})
            response = connection.getresponse()
            return response.status, dict(response.getheaders()), response.read()
        finally:
            connection.close()

    def test_plain_http_is_rejected(self):
        status, headers, _ = self.request("pay.example.com", "/payments/gmpay/v1/order/scan-health", tls=False)
        self.assertEqual(status, 404)
        self.assertEqual(headers.get("Cache-Control"), "no-store")

    def test_direct_origin_without_client_certificate_is_rejected(self):
        status, _, _ = self.request("pay.example.com", "/payments/gmpay/v1/order/scan-health", "POST", client=False)
        self.assertEqual(status, 400)

    def test_service_routes_require_post_and_are_not_cached(self):
        for suffix in ("create-transaction", "transfer-candidates", "transfer-candidates/ack", "scan-health"):
            path = "/payments/gmpay/v1/order/" + suffix
            with self.subTest(path=path):
                self.assertEqual(self.request("pay.example.com", path)[0], 405)
                status, headers, body = self.request("pay.example.com", path, "POST")
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(body)["host"], "pay.example.com")
                self.assertEqual(headers.get("Cache-Control"), "no-store")

    def test_operator_and_legacy_routes_are_not_forwarded(self):
        # /status and /healthz exist in the process but must never be proxied: the
        # operator reaches them only through an SSH tunnel to the loopback listener.
        for path in ("/admin", "/", "/api/v1/order", "/status", "/healthz", "/administrator"):
            with self.subTest(path=path):
                self.assertEqual(self.request("pay.example.com", path, "POST")[0], 404)

    def test_host_and_sni_mismatch_is_rejected(self):
        for host, sni in (("unrelated.example.com", "pay.example.com"), ("pay.example.com", "unrelated.example.com")):
            with self.subTest(host=host, sni=sni):
                self.assertEqual(self.request(host, "/payments/gmpay/v1/order/scan-health", sni=sni)[0], 421)

    def test_ipv6_obeys_same_ingress_rules(self):
        if not self.ipv6:
            self.skipTest("IPv6 loopback unavailable")
        self.assertEqual(self.request("pay.example.com", "/payments/gmpay/v1/order/scan-health", ip="::1", tls=False)[0], 404)
        self.assertEqual(self.request("pay.example.com", "/payments/gmpay/v1/order/scan-health", "POST", ip="::1", client=False)[0], 400)
        self.assertEqual(self.request("pay.example.com", "/status", ip="::1")[0], 404)


if __name__ == "__main__":
    unittest.main()
