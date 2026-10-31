"""Detached transport-signature checks against real OpenSSL Ed25519 output."""

import base64
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
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
        self.private = root / "dev-test-only.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "Ed25519", "-out", str(self.private)],
                       check=True, capture_output=True)
        der = subprocess.run(["openssl", "pkey", "-in", str(self.private), "-pubout", "-outform", "DER"],
                             check=True, capture_output=True).stdout
        self.assertEqual(der[:12], bytes.fromhex("302a300506032b6570032100"))
        public = der[12:]
        key_id = "sha256:" + hashlib.sha256(public).hexdigest()
        self.public_key = root / "trusted-public-key.json"
        self.public_key.write_text(json.dumps({"schemaVersion": 1, "algorithm": "Ed25519",
                                              "keyClass": "release", "keyId": key_id,
                                              "publicKeyBase64": base64.b64encode(public).decode()}))
        self.signature = root / "agentsql-v4.5.0-offline.tar.gz.sig.json"
        self.key_id = key_id
        self.resign()

    def resign(self):
        raw_signature = self.archive.parent / "raw-signature"
        subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(self.private),
                        "-rawin", "-in", str(self.archive), "-out", str(raw_signature)],
                       check=True, capture_output=True)
        self.signature.write_text(json.dumps({
            "schemaVersion": 1, "algorithm": "Ed25519", "keyClass": "release",
            "keyId": self.key_id, "signedArtifact": self.archive.name,
            "artifactSHA256": hashlib.sha256(self.archive.read_bytes()).hexdigest(),
            "signedPayload": "raw artifact bytes",
            "signatureBase64": base64.b64encode(raw_signature.read_bytes()).decode(),
        }))

    def check_archive(self, *options):
        return subprocess.run([sys.executable, str(VERIFIER), "--archive", str(self.archive),
                               "--signature", str(self.signature), "--public-key", str(self.public_key),
                               *options],
                              capture_output=True, text=True, check=False)

    def tar_archive(self, unsafe=None):
        with tarfile.open(self.archive, "w:gz") as output:
            root = tarfile.TarInfo("agentsql-v4.5.0-offline")
            root.type = tarfile.DIRTYPE
            output.addfile(root)
            data = b"verified installer\n"
            installer = tarfile.TarInfo("agentsql-v4.5.0-offline/install-offline.py")
            installer.size = len(data)
            output.addfile(installer, io.BytesIO(data))
            if unsafe is not None:
                output.addfile(unsafe)
        self.resign()

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

    def test_verified_copy_is_extracted(self):
        self.tar_archive()
        destination = self.archive.parent / "verified"
        result = self.check_archive("--extract-to", str(destination))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((destination / "agentsql-v4.5.0-offline/install-offline.py").read_bytes(),
                         b"verified installer\n")

    def test_changed_archive_is_not_extracted(self):
        self.tar_archive()
        with self.archive.open("ab") as output:
            output.write(b"tampered")
        destination = self.archive.parent / "verified"
        result = self.check_archive("--extract-to", str(destination))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(destination.exists())

    def test_existing_destination_is_not_replaced(self):
        self.tar_archive()
        destination = self.archive.parent / "verified"
        destination.mkdir()
        marker = destination / "keep"
        marker.write_text("existing data")
        result = self.check_archive("--extract-to", str(destination))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(marker.read_text(), "existing data")

    def test_signed_path_traversal_is_rejected_before_extract(self):
        member = tarfile.TarInfo("../outside")
        member.size = 0
        self.tar_archive(member)
        destination = self.archive.parent / "verified"
        result = self.check_archive("--extract-to", str(destination))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(destination.exists())
        self.assertFalse((self.archive.parent / "outside").exists())

    def test_signed_symlink_is_rejected_before_extract(self):
        member = tarfile.TarInfo("agentsql-v4.5.0-offline/link")
        member.type = tarfile.SYMTYPE
        member.linkname = "../../outside"
        self.tar_archive(member)
        destination = self.archive.parent / "verified"
        result = self.check_archive("--extract-to", str(destination))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(destination.exists())


if __name__ == "__main__":
    unittest.main()
