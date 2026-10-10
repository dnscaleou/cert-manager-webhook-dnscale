"""Exercise renewal and failure cleanup without contacting Kubernetes or a CA."""

import contextlib
import importlib.util
import io
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("verify_staging", Path(__file__).with_name("verify-staging.py"))
staging = importlib.util.module_from_spec(spec)
spec.loader.exec_module(staging)


class StagingTests(unittest.TestCase):
    def exercise(self, fail_namespace_cleanup=False):
        calls, resources = [], []
        renewed, pending_status, renew_polls, cert_reads = False, True, 0, 0

        def run(command, **kwargs):
            nonlocal renewed, pending_status, renew_polls, cert_reads
            calls.append(command)
            stdout, returncode = "", 0
            if command[0] == "dig" and "NS" in command:
                stdout = "ns1.example.test.\nns2.example.test.\n"
            elif command[0] == "dig":
                stdout = ";; ->>HEADER<<- opcode: QUERY, status: NXDOMAIN, id: 1\n;; flags: qr aa; QUERY: 1, ANSWER: 0\n"
            elif command[:2] == ["cmctl", "renew"]:
                renewed = True
            elif command[0] == "kubectl":
                operation = command[3:]
                if operation[0] == "apply":
                    resources.append(json.loads(kwargs["input"]))
                elif operation[:2] == ["get", "certificate"]:
                    if pending_status:
                        stdout, pending_status = "{}", False
                    else:
                        if renewed:
                            renew_polls += 1
                        revision = 2 if renew_polls >= 2 else 1
                        stdout = json.dumps({"status": {"revision": revision, "conditions": [{"type": "Ready", "status": "True"}]}})
                elif operation[:2] == ["get", "secret"]:
                    cert_reads += 1
                    stdout = "new-public-cert" if cert_reads >= 3 else "original-public-cert"
                elif operation[:2] == ["get", "namespace"] and fail_namespace_cleanup:
                    returncode = 1
            return subprocess.CompletedProcess(command, returncode, stdout, "private command diagnostics")

        output = io.StringIO()
        with tempfile.TemporaryDirectory() as temporary:
            token = Path(temporary) / "token"
            token.write_text("test-credential-must-not-appear-in-output")
            arguments = ["verify-staging.py", "--context", "disposable", "--zone", "example.test",
                         "--email", "operator@example.test", "--token-file", str(token)]
            with patch("sys.argv", arguments), patch.object(staging.subprocess, "run", side_effect=run), \
                    patch.object(staging.time, "sleep"), contextlib.redirect_stdout(output):
                if fail_namespace_cleanup:
                    with self.assertRaises(RuntimeError):
                        staging.main()
                else:
                    staging.main()
        self.assertNotIn("test-credential", output.getvalue())
        self.assertNotIn("private command diagnostics", output.getvalue())
        self.assertGreaterEqual(renew_polls, 3, "Ready alone or unchanged certificate must not satisfy renewal")
        issuer = next(r for r in resources if r["kind"] == "ClusterIssuer")
        self.assertEqual(issuer["spec"]["acme"]["server"], "https://acme-staging-v02.api.letsencrypt.org/directory")
        return calls, resources, output.getvalue()

    def test_renewal_requires_new_certificate_and_cleanup_is_scoped(self):
        calls, resources, output = self.exercise()
        name = next(r["metadata"]["name"] for r in resources if r["kind"] == "Namespace")
        deletes = [c[3:] for c in calls if c[0] == "kubectl" and c[3] == "delete"]
        self.assertEqual(deletes[0][:3], ["delete", "namespace", name])
        self.assertEqual(deletes[1][:3], ["delete", "clusterissuer", name])
        self.assertTrue(all(name in c for c in deletes))
        self.assertIn("final TXT cleanup", output)

    def test_failed_namespace_cleanup_keeps_issuer_and_credentials(self):
        calls, _, _ = self.exercise(fail_namespace_cleanup=True)
        deletes = [c[3:5] for c in calls if c[0] == "kubectl" and c[3] == "delete"]
        self.assertEqual(deletes, [["delete", "namespace"]])

    def test_dns_errors_and_non_authoritative_answers_cannot_prove_cleanup(self):
        for status, flags, answer, absent in [
            ("NXDOMAIN", "qr aa", "", True),
            ("NOERROR", "qr aa", "", True),
            ("SERVFAIL", "qr aa", "", False),
            ("REFUSED", "qr", "", False),
            ("NOERROR", "qr rd ra", "", False),
            ("NOERROR", "qr aa", 'test.example. 300 IN TXT "challenge"', False),
            ("NOERROR", "qr aa", "test.example. 300 IN CNAME other.example.", False),
        ]:
            with self.subTest(status=status, flags=flags, answer=answer):
                output = f";; ->>HEADER<<- opcode: QUERY, status: {status}, id: 1\n;; flags: {flags}; QUERY: 1\n{answer}\n"
                self.assertEqual(staging.authoritative_txt_absent(output), absent)
        self.assertFalse(staging.authoritative_txt_absent(""))


if __name__ == "__main__":
    unittest.main()
