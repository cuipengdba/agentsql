"""Detached transport-signature checks against real OpenSSL Ed25519 output."""

import base64
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


VERIFIER = Path(__file__).with_name("verify-offline-archive.py")


@unittest.skipUnless(shutil.which("openssl"), "OpenSSL is required for the offline signature test")
class OfflineArchiveSignatureTest(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        root = Path(self.folder.name)
        self.archive = root / "agentsql-v4.5.0-offline.tar.gz"
        self.archive.write_bytes(b"synthetic offline transport archive\n")
        private = root / "dev-test-only.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "Ed25519", "-out", str(private)],
                       check=True, capture_output=True)
        der = subprocess.run(["openssl", "pkey", "-in", str(private), "-pubout", "-outform", "DER"],
                             check=True, capture_output=True).stdout
        self.assertEqual(der[:12], bytes.fromhex("302a300506032b6570032100"))
        public = der[12:]
        key_id = "sha256:" + hashlib.sha256(public).hexdigest()
        self.public_key = root / "trusted-public-key.json"
        self.public_key.write_text(json.dumps({"schemaVersion": 1, "algorithm": "Ed25519",
                                              "keyClass": "release", "keyId": key_id,
                                              "publicKeyBase64": base64.b64encode(public).decode()}))
        raw_signature = root / "raw-signature"
        subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private),
                        "-rawin", "-in", str(self.archive), "-out", str(raw_signature)],
                       check=True, capture_output=True)
        self.signature = root / "agentsql-v4.5.0-offline.tar.gz.sig.json"
        self.signature.write_text(json.dumps({
            "schemaVersion": 1, "algorithm": "Ed25519", "keyClass": "release",
            "keyId": key_id, "signedArtifact": self.archive.name,
            "artifactSHA256": hashlib.sha256(self.archive.read_bytes()).hexdigest(),
            "signedPayload": "raw artifact bytes",
            "signatureBase64": base64.b64encode(raw_signature.read_bytes()).decode(),
        }))

    def check_archive(self):
        return subprocess.run([sys.executable, str(VERIFIER), "--archive", str(self.archive),
                               "--signature", str(self.signature), "--public-key", str(self.public_key)],
                              capture_output=True, text=True, check=False)

    def test_valid_archive(self):
        result = self.check_archive()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("OFFLINE_ARCHIVE=VERIFIED", result.stdout)

    def test_changed_archive_is_rejected(self):
        with self.archive.open("ab") as output:
            output.write(b"tampered")
        result = self.check_archive()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("OFFLINE_ARCHIVE=VERIFIED", result.stdout)

    def test_wrong_public_key_is_rejected(self):
        other_private = self.archive.parent / "other.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "Ed25519", "-out", str(other_private)],
                       check=True, capture_output=True)
        other_der = subprocess.run(["openssl", "pkey", "-in", str(other_private),
                                    "-pubout", "-outform", "DER"], check=True, capture_output=True).stdout
        other_public = other_der[12:]
        self.public_key.write_text(json.dumps({
            "schemaVersion": 1, "algorithm": "Ed25519", "keyClass": "release",
            "keyId": "sha256:" + hashlib.sha256(other_public).hexdigest(),
            "publicKeyBase64": base64.b64encode(other_public).decode(),
        }))
        result = self.check_archive()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("OFFLINE_ARCHIVE=VERIFIED", result.stdout)


if __name__ == "__main__":
    unittest.main()
