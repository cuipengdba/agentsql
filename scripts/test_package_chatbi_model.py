"""Negative archive checks that do not require 407 MB of model weights in CI."""

import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("package-chatbi-model.py")
SPEC = importlib.util.spec_from_file_location("chatbi_model_package", SCRIPT)
PACKAGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PACKAGE)


class PinnedModelArchiveTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.archive = Path(directory.name) / "model.tar.gz"
        self.manifest, self.files = PACKAGE.pinned_files()

    def add_members(self, members):
        with tarfile.open(self.archive, "w:gz") as bundle:
            for name, kind in members:
                info = tarfile.TarInfo(name)
                info.type = kind
                if kind == tarfile.REGTYPE:
                    content = b"untrusted"
                    info.size = len(content)
                    bundle.addfile(info, io.BytesIO(content))
                else:
                    bundle.addfile(info)

    def test_rejects_path_traversal(self):
        self.add_members([(f"{PACKAGE.ROOT}/../escape", tarfile.REGTYPE)])
        with self.assertRaises(ValueError):
            PACKAGE.verify_archive(self.archive, self.manifest, self.files)

    def test_rejects_symlinked_weights(self):
        self.add_members([(f"{PACKAGE.ROOT}/model.onnx", tarfile.SYMTYPE)])
        with self.assertRaises(ValueError):
            PACKAGE.verify_archive(self.archive, self.manifest, self.files)

    def test_rejects_duplicate_members(self):
        self.add_members([(PACKAGE.ROOT, tarfile.DIRTYPE), (PACKAGE.ROOT, tarfile.DIRTYPE)])
        with self.assertRaises(ValueError):
            PACKAGE.verify_archive(self.archive, self.manifest, self.files)

    def test_rejects_incomplete_archive(self):
        self.add_members([(PACKAGE.ROOT, tarfile.DIRTYPE)])
        with self.assertRaises(ValueError):
            PACKAGE.verify_archive(self.archive, self.manifest, self.files)


if __name__ == "__main__":
    unittest.main()
