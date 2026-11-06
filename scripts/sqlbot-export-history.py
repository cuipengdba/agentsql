"""Export pinned SQLBot Community chat/question metadata for AgentSQL.

Run inside the operator's existing SQLBot environment. The export contains
potentially sensitive user questions. It deliberately omits SQL, result rows,
charts, model prompts, credentials and API keys. The source transaction is
read-only and the output is create-only with private permissions.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import tomllib


SOURCE_COMMITS = {
    "1.10.2": "aa41fc1033bb974a703df9200a98ec8b9eec5f25",
    "1.10.3": "96f06c7eade60ecf3e6af0313821373761d1fb85",
}
CHAT_MODEL_SHA256 = "a50b6605136c2839669ec04b28195c8bfe8c4e05d7b824c51e3e7e737860317a"


def verify_baseline() -> tuple[str, str]:
    import apps.chat.models.chat_model as chat_model

    root = Path(chat_model.__file__).resolve().parents[3]
    version = tomllib.loads((root / "pyproject.toml").read_text(encoding="utf-8"))["project"]["version"]
    normalized = Path(chat_model.__file__).read_bytes().replace(b"\r\n", b"\n")
    if version not in SOURCE_COMMITS or hashlib.sha256(normalized).hexdigest() != CHAT_MODEL_SHA256:
        raise RuntimeError("This SQLBot Community chat schema is not a pinned baseline")
    return "v" + version, SOURCE_COMMITS[version]


def timestamp(value: datetime) -> str:
    if value.tzinfo is None:
        # SQLBot stores naive timestamps. The operator must verify source TZ.
        value = value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def export(after_id: int, limit: int) -> tuple[dict, dict]:
    from common.core.db import engine
    from sqlalchemy import text

    source_version, source_commit = verify_baseline()
    with engine.connect() as connection:
        connection.execute(text("SET TRANSACTION READ ONLY"))
        chats = connection.execute(text(
            "SELECT id, oid, create_time, create_by, brief, chat_type, datasource, origin "
            "FROM chat WHERE id > :after_id ORDER BY id LIMIT :limit"
        ), {"after_id": after_id, "limit": limit}).mappings().all()
        output = []
        skipped_chats = skipped_records = 0
        for chat in chats:
            if chat["chat_type"] != "chat" or any(chat[field] is None for field in
                ("id", "oid", "create_time", "create_by", "datasource")):
                skipped_chats += 1
                continue
            records = connection.execute(text(
                "SELECT id, create_time, question FROM chat_record "
                "WHERE chat_id = :chat_id ORDER BY id LIMIT 201"
            ), {"chat_id": chat["id"]}).mappings().all()
            if len(records) > 200:
                skipped_chats += 1
                continue
            questions = []
            for record in records:
                if record["id"] is None or record["create_time"] is None or not record["question"]:
                    skipped_records += 1
                    continue
                questions.append({"id": str(record["id"]), "question": record["question"],
                                  "create_time": timestamp(record["create_time"])})
            output.append({"id": str(chat["id"]), "oid": str(chat["oid"]),
                           "create_by": str(chat["create_by"]), "create_time": timestamp(chat["create_time"]),
                           "brief": chat["brief"] or "", "datasource": str(chat["datasource"]),
                           "origin": chat["origin"] or 0, "records": questions})
        connection.rollback()
    document = {"format_version": 1, "source_version": source_version, "source_commit": source_commit,
                "chats": output}
    report = {"exported_chats": len(output), "exported_questions": sum(len(c["records"]) for c in output),
              "skipped_chats": skipped_chats, "skipped_records": skipped_records,
              "next_after_id": str(chats[-1]["id"]) if chats else str(after_id),
              "contains_private_questions": True, "complete_migration": False,
              "source_database_modified": False}
    return document, report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--after-id", default=0, type=int)
    parser.add_argument("--limit", default=1000, type=int)
    args = parser.parse_args()
    if args.after_id < 0 or args.limit < 1 or args.limit > 1000:
        parser.error("after-id must be nonnegative and limit must be 1..1000")
    document, report = export(args.after_id, args.limit)
    descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        json.dump(document, output, ensure_ascii=False, separators=(",", ":"))
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    print(json.dumps(report))


if __name__ == "__main__":
    main()
