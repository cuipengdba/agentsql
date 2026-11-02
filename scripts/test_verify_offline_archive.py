"""Detached transport-signature checks against real OpenSSL Ed25519 output."""

import base64
import hashlib
import io
import json
import runpy
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest


VERIFIER = Path(__file__).with_name("verify-offline-archive.py")
INSTALLER = Path(__file__).with_name("install-offline-release.py")
STAGE_MODEL = runpy.run_path(str(INSTALLER))["stage_model_archive"]
STAGE_RELEASE_APP = runpy.run_path(str(INSTALLER))["stage_release_app_for_model"]


class OfflineModelStageTest(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.root = Path(self.folder.name)
        self.archive = self.root / "model.tar.gz"
        self.prefix = "agentsql-v4.5.0-chatbi-model"
        self.files = ["model.onnx", "1_Pooling/config.json"] + [f"file-{i}.json" for i in range(7)]

    def build(self, *, tamper=False, extra=None):
        manifest = {"license": "Apache-2.0", "files": [
            {"path": name, "size": 1, "sha256": hashlib.sha256(b"x").hexdigest()}
            for name in self.files
        ]}
        data = json.dumps(manifest).encode()
        with tarfile.open(self.archive, "w:gz") as package:
            for name in (self.prefix, self.prefix + "/1_Pooling"):
                member = tarfile.TarInfo(name)
                member.type = tarfile.DIRTYPE
                package.addfile(member)
            member = tarfile.TarInfo(self.prefix + "/pinned_embedding_model.json")
            member.size = len(data)
            package.addfile(member, io.BytesIO(data))
            for name in self.files:
                member = tarfile.TarInfo(self.prefix + "/" + name)
                member.size = 1
                package.addfile(member, io.BytesIO(b"z" if tamper and name == "model.onnx" else b"x"))
            if extra is not None:
                package.addfile(extra)

    def test_signed_model_members_are_staged_without_manifest(self):
        self.build()
        scratch = self.root / "scratch"
        scratch.mkdir()
        directory = STAGE_MODEL(self.archive, "v4.5.0", scratch)
        self.assertEqual((directory / "model.onnx").read_bytes(), b"x")
        self.assertFalse((directory / "pinned_embedding_model.json").exists())

    def test_tampered_model_file_is_rejected(self):
        self.build(tamper=True)
        scratch = self.root / "scratch"
        scratch.mkdir()
        with self.assertRaises(ValueError):
            STAGE_MODEL(self.archive, "v4.5.0", scratch)

    def test_link_and_traversal_members_are_rejected(self):
        for index, (name, kind) in enumerate(((self.prefix + "/link", tarfile.SYMTYPE), ("../escape", tarfile.REGTYPE))):
            with self.subTest(name=name):
                extra = tarfile.TarInfo(name)
                extra.type = kind
                if kind == tarfile.SYMTYPE:
                    extra.linkname = "../../outside"
                self.build(extra=extra)
                scratch = self.root / f"scratch-{index}"
                scratch.mkdir()
                with self.assertRaises(ValueError):
                    STAGE_MODEL(self.archive, "v4.5.0", scratch)


class OfflineReleaseApplicationStageTest(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.root = Path(self.folder.name)
        self.archive = self.root / "application.tar.gz"
        self.prefix = "agentsql-v4.5.0-linux-amd64/"

    def build(self, unsafe_library=False):
        with tarfile.open(self.archive, "w:gz") as package:
            for name, data in (("agentsql", b"app"), ("lib/yashandb/libyascli.so", b"library")):
                member = tarfile.TarInfo(self.prefix + name)
                if unsafe_library and name.endswith("libyascli.so"):
                    member.type = tarfile.SYMTYPE
                    member.linkname = "../../escape"
                    package.addfile(member)
                else:
                    member.size = len(data)
                    package.addfile(member, io.BytesIO(data))

    def test_authenticated_binary_and_library_are_staged(self):
        self.build()
        app, libraries = STAGE_RELEASE_APP(self.archive, "v4.5.0", "amd64", self.root)
        self.assertEqual(app.read_bytes(), b"app")
        self.assertEqual((libraries / "libyascli.so").read_bytes(), b"library")

    def test_symlinked_library_is_rejected(self):
        self.build(unsafe_library=True)
        with self.assertRaises(ValueError):
            STAGE_RELEASE_APP(self.archive, "v4.5.0", "amd64", self.root)


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

    def tar_archive(self, unsafe=None, installer_data=b"verified installer\n"):
        with tarfile.open(self.archive, "w:gz") as output:
            root = tarfile.TarInfo("agentsql-v4.5.0-offline")
            root.type = tarfile.DIRTYPE
            output.addfile(root)
            installer = tarfile.TarInfo("agentsql-v4.5.0-offline/install-offline.py")
            installer.size = len(installer_data)
            output.addfile(installer, io.BytesIO(installer_data))
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

    def test_one_command_install_runs_only_verified_installer(self):
        marker = self.archive.parent / "installer-ran"
        code = ("from pathlib import Path\nimport sys\n"
                + f"Path({str(marker)!r}).write_text(' '.join(sys.argv[1:]))\n")
        self.tar_archive(installer_data=code.encode())
        result = self.check_archive("--install", "--action", "upgrade", "--no-start",
                                    "--external-backup-done")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("OFFLINE_ARCHIVE=INSTALLED", result.stdout)
        arguments = marker.read_text()
        self.assertIn("upgrade", arguments)
        self.assertIn("--no-start", arguments)
        self.assertIn("--external-backup-done", arguments)
        self.assertIn(str(self.public_key), arguments)

    def test_external_backup_confirmation_requires_upgrade_and_install(self):
        self.tar_archive()
        for options in (("--external-backup-done",),
                        ("--install", "--external-backup-done")):
            with self.subTest(options=options):
                result = self.check_archive(*options)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("OFFLINE_ARCHIVE=INSTALLED", result.stdout)

        inner = subprocess.run([sys.executable, str(INSTALLER), "install",
                                "--external-backup-done"], capture_output=True, text=True)
        self.assertNotEqual(inner.returncode, 0)
        self.assertIn("--external-backup-done requires upgrade", inner.stderr)
        verify_only = subprocess.run([sys.executable, str(INSTALLER), "upgrade",
                                      "--external-backup-done", "--verify-only"],
                                     capture_output=True, text=True)
        self.assertNotEqual(verify_only.returncode, 0)
        self.assertIn("only meaningful for installation", verify_only.stderr)

    def test_one_command_install_rejects_changed_archive_before_execution(self):
        marker = self.archive.parent / "installer-ran"
        code = f"from pathlib import Path\nPath({str(marker)!r}).write_text('ran')\n"
        self.tar_archive(installer_data=code.encode())
        with self.archive.open("ab") as output:
            output.write(b"tampered")
        result = self.check_archive("--install")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists())
        self.assertNotIn("OFFLINE_ARCHIVE=INSTALLED", result.stdout)

    def test_one_command_install_rejects_signed_unsafe_archive(self):
        marker = self.archive.parent / "installer-ran"
        code = f"from pathlib import Path\nPath({str(marker)!r}).write_text('ran')\n"
        member = tarfile.TarInfo("agentsql-v4.5.0-offline/escape")
        member.type = tarfile.SYMTYPE
        member.linkname = "../../outside"
        self.tar_archive(unsafe=member, installer_data=code.encode())
        result = self.check_archive("--install")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
