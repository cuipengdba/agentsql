"""Trusted SQLBot identity bridge installed only into the pinned upstream image.

The managed adapter accepts ordinary SQLBot login sessions. Assistant, embedded
and ask-token contexts are deliberately excluded. No browser-supplied user ID,
username, SQL text or model output is an identity assertion.
"""
from contextlib import contextmanager
from contextvars import ContextVar
from functools import wraps
from types import SimpleNamespace

from agentsql_sqlbot import AdapterError, _read_config, dispatch

_identity = ContextVar("agentsql_sqlbot_verified_identity", default=None)


def _managed():
    return _read_config()["version"] == 2


def _valid_identity(user):
    identifier = getattr(user, "id", None)
    workspace = getattr(user, "oid", None)
    if (type(identifier) is not int or identifier <= 0 or type(workspace) is not int or workspace <= 0
            or getattr(user, "status", None) != 1):
        raise AdapterError("A valid signed-in SQLBot user is required", code="identity_required")
    return user


def authenticated_request_user(request=None):
    if request is None:
        try:
            from common.audit.schemas.request_context import RequestContext
            request = RequestContext.get_request()
        except (ImportError, RuntimeError, LookupError):
            raise AdapterError("A signed-in SQLBot request is required", code="identity_required") from None
    from common.core.config import settings
    headers = request.headers
    if (headers.get(settings.ASSISTANT_TOKEN_KEY) or headers.get("X-SQLBOT-ASK-TOKEN")
            or getattr(request.state, "assistant", None) is not None):
        raise AdapterError("AgentSQL managed bindings require an ordinary SQLBot user login", code="identity_required")
    token = headers.get(settings.TOKEN_KEY, "")
    if not isinstance(token, str) or not token.lower().startswith("bearer "):
        raise AdapterError("A SQLBot user login token is required", code="identity_required")
    return _valid_identity(getattr(request.state, "current_user", None))


def current_identity():
    user = _identity.get()
    return _valid_identity(user) if user is not None else authenticated_request_user()


@contextmanager
def verified_scope(user):
    # Internal entry point: callers are only the pinned constructor/worker and
    # authenticated FastAPI routes. The function is not exposed over HTTP/MCP.
    marker = _identity.set(_valid_identity(user))
    try:
        yield
    finally:
        _identity.reset(marker)


def protect_llm_service(cls):
    original_init = cls.__init__

    @wraps(original_init)
    def initialize(self, *args, **kwargs):
        if not _managed():
            return original_init(self, *args, **kwargs)
        user = args[1] if len(args) > 1 else kwargs.get("current_user")
        authenticated = authenticated_request_user()
        assistant = args[3] if len(args) > 3 else kwargs.get("current_assistant")
        if assistant is not None or getattr(user, "id", None) != authenticated.id:
            raise AdapterError("SQLBot task identity does not match its authenticated request", code="identity_mismatch")
        # SQLBot checks chat workspace membership and then updates user.oid
        # before doing datasource work. Retain that checked object during init.
        with verified_scope(user):
            original_init(self, *args, **kwargs)
            self._agentsql_user = SimpleNamespace(id=user.id, oid=user.oid, status=user.status,
                                                  language=getattr(user, "language", "en"))

    cls.__init__ = initialize

    def protect_worker(method):
        @wraps(method)
        def worker(self, *args, **kwargs):
            if not _managed():
                yield from method(self, *args, **kwargs)
                return
            user = getattr(self, "_agentsql_user", None)
            if user is None:
                raise AdapterError("SQLBot worker is missing its verified user", code="identity_required")
            with verified_scope(user):
                yield from method(self, *args, **kwargs)
        return worker

    for name in ("run_task", "run_recommend_questions_task", "run_analysis_or_predict_task"):
        setattr(cls, name, protect_worker(getattr(cls, name)))

    def execute(self, sql):
        from common.error import SingleMessageError
        try:
            return dispatch("exec_sql", self.ds, sql=sql, origin_column=False)
        except AdapterError as error:
            # SQLBot's frontend treats SQLBotDBError as an invitation to ask the
            # model to repair/retry SQL. Governance decisions must terminate it.
            raise SingleMessageError(str(error)) from None
    cls.execute_sql = execute
    return cls


def filter_table_objects(objects, ds):
    """Do not send a broader cached SQLBot schema to a narrower gateway user."""
    if not _managed():
        return objects
    columns = dispatch("authorized_schema", ds)
    allowed = {}
    for column in columns:
        key = (column.get("schema"), column.get("table"))
        allowed.setdefault(key, set()).add(column.get("column"))
    filtered = []
    for obj in objects:
        key = (obj.schema, obj.table.table_name)
        names = allowed.get(key, set())
        fields = [field for field in (obj.fields or []) if field.field_name in names]
        if fields:
            obj.fields = fields
            filtered.append(obj)
    return filtered
