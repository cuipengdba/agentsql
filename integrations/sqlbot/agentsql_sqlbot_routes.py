"""Authenticated diagnostics and explicit approval actions for the derived SQLBot image."""
import json
from pathlib import Path
from types import SimpleNamespace
from urllib.parse import urlsplit

from fastapi import APIRouter, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import HTMLResponse, JSONResponse

from agentsql_sqlbot import AdapterError, _read_config, _sqlbot_id, dispatch
from agentsql_sqlbot_context import authenticated_request_user, verified_scope

router = APIRouter(prefix="/agentsql", tags=["AgentSQL"])


def _response(data, status=200):
    return JSONResponse(data, status_code=status, headers={"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"})


def _bindings(user):
    config = _read_config()
    if config["version"] != 2:
        raise AdapterError("Managed configuration version 2 is required", code="configuration_invalid")
    return [b for b in config["bindings"] if _sqlbot_id(b.get("workspace_id")) == _sqlbot_id(user.oid)
            and _sqlbot_id(b.get("sqlbot_user_id")) == _sqlbot_id(user.id)]


def _datasource(user, binding_id):
    matches = [b for b in _bindings(user) if b.get("id") == binding_id]
    if len(matches) != 1:
        raise AdapterError("No unique binding is available to this signed-in user", code="mapping_missing")
    binding = matches[0]
    return SimpleNamespace(oid=binding["workspace_id"], name=binding["datasource_name"], type=binding.get("dialect"))


async def _body(request):
    # A login token in an explicit header is required by authenticated_request_user.
    # No ambient cookie, query-string credential, form POST or cross-origin action.
    origin = request.headers.get("origin")
    if origin and (urlsplit(origin).scheme, urlsplit(origin).netloc) != (request.url.scheme, request.url.netloc):
        raise AdapterError("Cross-origin approval actions are refused", code="origin_rejected")
    if request.headers.get("content-type", "").split(";")[0].strip().lower() != "application/json":
        raise AdapterError("JSON action body is required", code="request_invalid")
    raw = await request.body()
    if len(raw) > 100000:
        raise AdapterError("Action body is too large", code="request_invalid")
    try:
        body = json.loads(raw)
        if not isinstance(body, dict):
            raise ValueError()
        return body
    except (ValueError, UnicodeError):
        raise AdapterError("JSON action body is invalid", code="request_invalid") from None


@router.get("/console.html", include_in_schema=False)
async def console():
    # Upstream's *.html whitelist allows this inert shell. It contains no user,
    # binding, credentials or results; all data/actions require authenticated APIs.
    from common.core.config import settings
    html = Path(__file__).with_name("agentsql_sqlbot_console.html").read_text(encoding="utf-8")
    runtime = json.dumps({"context_path": settings.CONTEXT_PATH, "token_header": settings.TOKEN_KEY}).replace("<", "\\u003c")
    return HTMLResponse(html.replace("/* AGENTSQL_RUNTIME */", "const runtime = " + runtime + ";"),
                        headers={"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"})


@router.get("/bindings")
async def bindings(request: Request):
    try:
        user = authenticated_request_user(request)
        values = _bindings(user)
        return _response({"bindings": [{"id": b["id"], "workspace_id": b["workspace_id"],
                                        "datasource_name": b["datasource_name"]} for b in values]})
    except AdapterError as error:
        return _response({"error": error.feedback()}, 403)


@router.post("/diagnostics")
async def diagnostics(request: Request):
    try:
        user = authenticated_request_user(request)
        body = await _body(request)
        ds = _datasource(user, body.get("binding_id"))
        def inspect():
            with verified_scope(user):
                dispatch("check_connection", ds)
                tables = dispatch("get_tables", ds)
            return {"status": "ready", "adapter_version": "2", "upstream_version": "v1.10.2",
                    "identity": "SQLBot user login", "visible_table_count": len(tables),
                    "direct_connections": "disabled", "automatic_sql_retry": "disabled"}
        return _response(await run_in_threadpool(inspect))
    except AdapterError as error:
        return _response({"error": error.feedback()}, 400)


@router.post("/approvals/request")
async def request_approval(request: Request):
    return await _approval_action(request, "request_approval")


@router.post("/approvals/status")
async def approval_status(request: Request):
    return await _approval_action(request, "get_approval_result")


@router.post("/approvals/execute")
async def execute_approval(request: Request):
    return await _approval_action(request, "execute_approved")


async def _approval_action(request, operation):
    try:
        user = authenticated_request_user(request)
        body = await _body(request)
        ds = _datasource(user, body.get("binding_id"))
        if operation != "get_approval_result" and body.get("confirmed") is not True:
            raise AdapterError("Explicit confirmation is required for this approval action", code="confirmation_required")
        def perform():
            with verified_scope(user):
                return dispatch(operation, ds, sql=body.get("sql"), reason=body.get("reason"),
                                approval_id=body.get("approval_id"))
        return _response(await run_in_threadpool(perform))
    except AdapterError as error:
        return _response({"error": error.feedback()}, 400)
