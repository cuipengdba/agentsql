#include "postgres.h"

#include <ctype.h>

#include "access/xact.h"
#include "access/relation.h"
#include "catalog/catalog.h"
#include "catalog/namespace.h"
#include "catalog/pg_type.h"
#include "commands/prepare.h"
#include "common/cryptohash.h"
#include "executor/spi.h"
#include "funcapi.h"
#include "miscadmin.h"
#include "nodes/nodeFuncs.h"
#include "nodes/print.h"
#include "parser/parser.h"
#include "parser/parsetree.h"
#include "storage/ipc.h"
#include "tcop/utility.h"
#include "utils/builtins.h"
#include "utils/datum.h"
#include "utils/guc.h"
#include "utils/lsyscache.h"
#include "utils/memutils.h"
#include "utils/plancache.h"
#include "utils/rel.h"
#include "utils/snapmgr.h"
#include "utils/syscache.h"

PG_MODULE_MAGIC;

#define AGENTSQL_ABI "agentsql-binder-4.1"
#define AGENTSQL_DML_ABI "agentsql-binder-dml-1"
#define AGENTSQL_EXT_VERSION "0.4-s3"
#define AGENTSQL_MAX_DEPTH 64

typedef struct
{
	Oid oid;
	char kind;
	bool inh;
	AclMode required_perms;
	Oid check_as_user;
	int view_depth;
	char *path;
} BinderRelation;

typedef struct
{
	char *site;
	Oid relation_oid;
	AttrNumber attnum;
	Oid type_oid;
	Oid collation_oid;
	char *usage;
	int query_depth;
	int view_depth;
	int contributor_group;
	bool contributor_complete;
	bool whole_row;
	Oid result_type_oid;
	bool result_composite;
} BinderVar;

typedef struct
{
	char *kind;
	Oid oid;
} BinderObject;

typedef struct BinderRecord
{
	MemoryContext memory_context;
	char *name;
	int backend_pid;
	char *transaction_id;
	Oid role_oid;
	char *role_name;
	char *search_path;
	char *analyzed_digest;
	char *dependency_digest;
	uint64 plan_generation;
	uint64 replan_count;
	bool invalidated;
	bool sealed;
	char *command_type;
	bool dml;
	Oid target_relation_oid;
	char *dml_shape;
	bool has_returning;
	bool has_recursive;
	bool has_modifying_cte;
	int node_count;
	int edge_count;
	int work_units;
	BinderRelation *relations;
	int relation_count;
	int relation_capacity;
	BinderVar *vars;
	int var_count;
	int var_capacity;
	BinderObject *objects;
	int object_count;
	int object_capacity;
	CachedPlanSource *plansource;
	struct BinderRecord *next;
} BinderRecord;

typedef struct
{
	BinderRecord *record;
	Query *query_stack[AGENTSQL_MAX_DEPTH];
	int stack_depth;
	int view_depth;
	const char *site;
	const char *usage;
	int contributor_group;
	bool unsupported;
	Oid view_stack[AGENTSQL_MAX_DEPTH];
	int view_stack_depth;
} BinderWalkContext;

static BinderRecord *records = NULL;
static ProcessUtility_hook_type previous_ProcessUtility = NULL;
static bool internal_prepare = false;

void _PG_init(void);
void _PG_fini(void);

PG_FUNCTION_INFO_V1(agentsql_binder_capabilities);
PG_FUNCTION_INFO_V1(agentsql_binder_prepare);
PG_FUNCTION_INFO_V1(agentsql_binder_dml_prepare);
PG_FUNCTION_INFO_V1(agentsql_binder_seal);
PG_FUNCTION_INFO_V1(agentsql_binder_manifest);
PG_FUNCTION_INFO_V1(agentsql_binder_dml_manifest);
PG_FUNCTION_INFO_V1(agentsql_binder_relations);
PG_FUNCTION_INFO_V1(agentsql_binder_vars);
PG_FUNCTION_INFO_V1(agentsql_binder_objects);

static void binder_ProcessUtility(PlannedStmt *pstmt, const char *queryString,
								  bool readOnlyTree, ProcessUtilityContext context,
								  ParamListInfo params, QueryEnvironment *queryEnv,
								  DestReceiver *dest, QueryCompletion *qc);
static void binder_xact_callback(XactEvent event, void *arg);
static BinderRecord *find_record(const char *name, bool missing_ok);
static void remove_record(const char *name);
static void remove_all_records(void);
static void validate_record(BinderRecord *record, bool require_sealed);
static bool preflight_walker(Node *node, void *context);
static bool expression_walker(Node *node, void *context);
static void scan_query(BinderWalkContext *context, Query *query, int view_depth, const char *path);
static void resolve_var(BinderWalkContext *context, Var *var);
static void resolve_rte_var(BinderWalkContext *context, Query *query, Index varno,
							AttrNumber attnum, Oid type_oid, Oid collid, int view_depth,
							int recursion_depth);
static JoinExpr *find_join_expr(Node *node, Index rtindex);
static void resolve_join_input(BinderWalkContext *context, Query *query, Node *join_input,
							   AttrNumber attnum, Oid type_oid, Oid collid,
							   int view_depth, int recursion_depth);
static void walk_expression(BinderWalkContext *context, Query *query, Node *node,
							const char *site, const char *usage, int contributor_group);
static void add_relation(BinderWalkContext *context, Oid oid, char kind, bool inh,
						 AclMode required_perms, Oid check_as_user, int view_depth,
						 const char *path);
static void add_var(BinderWalkContext *context, Oid relid, AttrNumber attnum,
					Oid type_oid, Oid collid, bool complete, bool whole_row,
					Oid result_type_oid, bool result_composite);
static void add_object(BinderRecord *record, const char *kind, Oid oid);
static void add_expression_objects(BinderWalkContext *context, Node *node);
static bool supported_expression_node(Node *node);
static char *sha256_hex(const char *bytes, Size length);
static char *dependency_digest(BinderRecord *record);
static bool is_composite_type(Oid type_oid);
static void ensure_relation_capacity(BinderRecord *record);
static void ensure_var_capacity(BinderRecord *record);
static void ensure_object_capacity(BinderRecord *record);
static Datum tuple_result(FunctionCallInfo fcinfo, Datum *values, bool *nulls);
static Datum binder_prepare_common(FunctionCallInfo fcinfo, bool dml);
static bool record_has_write_target(BinderRecord *record, Oid relation_oid, AttrNumber attnum);

void
_PG_init(void)
{
	previous_ProcessUtility = ProcessUtility_hook;
	ProcessUtility_hook = binder_ProcessUtility;
	RegisterXactCallback(binder_xact_callback, NULL);
}

void
_PG_fini(void)
{
	ProcessUtility_hook = previous_ProcessUtility;
	UnregisterXactCallback(binder_xact_callback, NULL);
}

static void
binder_xact_callback(XactEvent event, void *arg)
{
	(void) arg;
	if (event == XACT_EVENT_COMMIT || event == XACT_EVENT_ABORT ||
		event == XACT_EVENT_PARALLEL_COMMIT || event == XACT_EVENT_PARALLEL_ABORT)
		remove_all_records();
}

static void
binder_ProcessUtility(PlannedStmt *pstmt, const char *queryString,
					  bool readOnlyTree, ProcessUtilityContext context,
					  ParamListInfo params, QueryEnvironment *queryEnv,
					  DestReceiver *dest, QueryCompletion *qc)
{
	Node *node = pstmt->utilityStmt;
	BinderRecord *record = NULL;

	if (IsA(node, ExecuteStmt))
	{
		ExecuteStmt *execute = (ExecuteStmt *) node;
		record = find_record(execute->name, true);
		if (record != NULL)
			validate_record(record, true);
	}
	else if (IsA(node, PrepareStmt) && !internal_prepare)
	{
		PrepareStmt *prepare = (PrepareStmt *) node;
		if (strncmp(prepare->name, "agentsql_", 9) == 0)
			ereport(ERROR, (errcode(ERRCODE_INSUFFICIENT_PRIVILEGE),
							errmsg("AgentSQL prepared statement name is reserved")));
	}

	if (previous_ProcessUtility)
		previous_ProcessUtility(pstmt, queryString, readOnlyTree, context, params,
							queryEnv, dest, qc);
	else
		standard_ProcessUtility(pstmt, queryString, readOnlyTree, context, params,
							queryEnv, dest, qc);

	if (record != NULL)
		validate_record(record, true);

	if (IsA(node, DeallocateStmt))
	{
		DeallocateStmt *deallocate = (DeallocateStmt *) node;
		if (deallocate->name != NULL)
			remove_record(deallocate->name);
		else
			remove_all_records();
	}
}

static BinderRecord *
find_record(const char *name, bool missing_ok)
{
	BinderRecord *record;
	for (record = records; record != NULL; record = record->next)
		if (strcmp(record->name, name) == 0)
			return record;
	if (!missing_ok)
		ereport(ERROR, (errcode(ERRCODE_INVALID_SQL_STATEMENT_NAME),
						errmsg("AgentSQL prepared statement is unavailable")));
	return NULL;
}

static void
remove_record(const char *name)
{
	BinderRecord **link = &records;
	while (*link != NULL)
	{
		BinderRecord *record = *link;
		if (strcmp(record->name, name) == 0)
		{
			*link = record->next;
			MemoryContextDelete(record->memory_context);
			return;
		}
		link = &record->next;
	}
}

static void
remove_all_records(void)
{
	while (records != NULL)
	{
		BinderRecord *record = records;
		records = record->next;
		MemoryContextDelete(record->memory_context);
	}
}

static void
validate_record(BinderRecord *record, bool require_sealed)
{
	PreparedStatement *prepared;
	const char *search_path;
	char *tree;
	char *digest;

	if (record == NULL || (require_sealed && !record->sealed))
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
						errmsg("AgentSQL prepared statement is not sealed")));
	prepared = FetchPreparedStatement(record->name, true);
	if (prepared == NULL || prepared->plansource != record->plansource ||
		!record->plansource->is_valid)
	{
		record->invalidated = true;
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
						errmsg("AgentSQL prepared statement was invalidated")));
	}
	if (record->sealed && record->plansource->generation != record->plan_generation)
	{
		record->invalidated = true;
		record->replan_count++;
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
						errmsg("AgentSQL prepared statement replanning is forbidden")));
	}
	search_path = GetConfigOption("search_path", false, false);
	if (GetUserId() != record->role_oid || strcmp(search_path, record->search_path) != 0)
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
						errmsg("AgentSQL prepared statement identity changed")));
	tree = nodeToString(record->plansource->query_list);
	digest = sha256_hex(tree, strlen(tree));
	if (strcmp(digest, record->analyzed_digest) != 0)
	{
		record->invalidated = true;
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
						errmsg("AgentSQL analyzed tree changed")));
	}
}

static bool
is_internal_name(const char *name)
{
	const unsigned char *p = (const unsigned char *) name;
	if (strncmp(name, "agentsql_", 9) != 0)
		return false;
	for (; *p; p++)
		if (!(IS_HIGHBIT_SET(*p) == 0 && (isalnum(*p) || *p == '_')))
			return false;
	return true;
}

static bool
allowed_raw_type(TypeName *type_name)
{
	static const char *allowed[] = {"bool", "boolean", "int2", "smallint", "int4",
		"integer", "int8", "bigint", "numeric", "text", "varchar", "date",
		"timestamp", "timestamptz", "uuid", "bytea", "float4", "float8"};
	char *schema = NULL;
	char *name;
	int i;
	if (type_name == NULL || type_name->names == NIL)
		return false;
	name = strVal(llast(type_name->names));
	if (list_length(type_name->names) == 2)
		schema = strVal(linitial(type_name->names));
	else if (list_length(type_name->names) != 1)
		return false;
	if (schema != NULL && strcmp(schema, "pg_catalog") != 0)
		return false;
	for (i = 0; i < lengthof(allowed); i++)
		if (pg_strcasecmp(name, allowed[i]) == 0)
			return true;
	return false;
}

static bool
preflight_walker(Node *node, void *context)
{
	(void) context;
	if (node == NULL)
		return false;
	if (IsA(node, ParamRef) || IsA(node, RangeFunction) || IsA(node, TableFunc) ||
		IsA(node, SQLValueFunction) || IsA(node, CurrentOfExpr))
		ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
						errmsg("AgentSQL expression identity is unsupported")));
	if (IsA(node, FuncCall))
	{
		FuncCall *call = (FuncCall *) node;
		if (list_length(call->funcname) != 2 ||
			strcmp(strVal(linitial(call->funcname)), "pg_catalog") != 0)
			ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
							errmsg("AgentSQL function must be explicitly pg_catalog qualified")));
	}
	if (IsA(node, TypeCast) && !allowed_raw_type(((TypeCast *) node)->typeName))
		ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
						errmsg("AgentSQL cast type is unsupported")));
	if (IsA(node, RangeVar))
	{
		RangeVar *range = (RangeVar *) node;
		Oid relid = RangeVarGetRelid(range, AccessShareLock, false);
		Relation relation = relation_open(relid, NoLock);
		TupleDesc descriptor = RelationGetDescr(relation);
		int i;
		char kind = relation->rd_rel->relkind;
		if (kind != RELKIND_RELATION && kind != RELKIND_VIEW)
			ereport(ERROR, (errcode(ERRCODE_WRONG_OBJECT_TYPE),
							errmsg("AgentSQL relation kind is unsupported")));
		for (i = 0; i < descriptor->natts; i++)
		{
			Form_pg_attribute attribute = TupleDescAttr(descriptor, i);
			if (attribute->attisdropped)
				continue;
			if (attribute->atttypid >= FirstNormalObjectId ||
				is_composite_type(attribute->atttypid))
				ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
								errmsg("AgentSQL relation column type is unsupported")));
		}
		relation_close(relation, NoLock);
	}
	return raw_expression_tree_walker(node, preflight_walker, context);
}

Datum
agentsql_binder_prepare(PG_FUNCTION_ARGS)
{
	return binder_prepare_common(fcinfo, false);
}

Datum
agentsql_binder_dml_prepare(PG_FUNCTION_ARGS)
{
	return binder_prepare_common(fcinfo, true);
}

static Datum
binder_prepare_common(FunctionCallInfo fcinfo, bool dml)
{
	char *name = text_to_cstring(PG_GETARG_TEXT_PP(0));
	char *sql = text_to_cstring(PG_GETARG_TEXT_PP(1));
	List *raw;
	RawStmt *statement;
	SelectStmt *select = NULL;
	const char *command_type = NULL;
	const char *dml_shape = "SIMPLE";
	bool has_returning = false;
	StringInfoData command;
	PreparedStatement *prepared;
	BinderRecord *record;
	MemoryContext previous;
	MemoryContext record_context;
	char *tree;
	TransactionId xid;
	int result;

	if (!is_internal_name(name) || strlen(name) > 96)
		ereport(ERROR, (errcode(ERRCODE_INVALID_NAME), errmsg("invalid AgentSQL statement name")));
	if (find_record(name, true) != NULL || FetchPreparedStatement(name, false) != NULL)
		ereport(ERROR, (errcode(ERRCODE_DUPLICATE_PSTATEMENT), errmsg("AgentSQL statement already exists")));
	raw = raw_parser(sql, RAW_PARSE_DEFAULT);
	if (list_length(raw) != 1)
		ereport(ERROR, (errcode(ERRCODE_SYNTAX_ERROR), errmsg("AgentSQL requires one statement")));
	statement = linitial_node(RawStmt, raw);
	if (!dml)
	{
		if (!IsA(statement->stmt, SelectStmt))
			ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL supports SELECT only")));
		select = (SelectStmt *) statement->stmt;
		if (select->intoClause != NULL || select->lockingClause != NIL || select->withClause != NULL)
			ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL SELECT shape is unsupported")));
		command_type = "SELECT";
	}
	else if (IsA(statement->stmt, InsertStmt))
	{
		InsertStmt *insert = (InsertStmt *) statement->stmt;
		command_type = "INSERT";
#if PG_VERSION_NUM >= 180000
		has_returning = insert->returningClause != NULL;
#else
		has_returning = insert->returningList != NIL;
#endif
		if (insert->withClause != NULL)
			dml_shape = "CTE";
		else if (insert->onConflictClause != NULL)
			dml_shape = "UPSERT";
		else if (insert->selectStmt != NULL &&
			(!IsA(insert->selectStmt, SelectStmt) || ((SelectStmt *) insert->selectStmt)->valuesLists == NIL))
			dml_shape = "INSERT_SELECT";
	}
	else if (IsA(statement->stmt, UpdateStmt))
	{
		UpdateStmt *update = (UpdateStmt *) statement->stmt;
		command_type = "UPDATE";
#if PG_VERSION_NUM >= 180000
		has_returning = update->returningClause != NULL;
#else
		has_returning = update->returningList != NIL;
#endif
		if (update->withClause != NULL)
			dml_shape = "CTE";
		else if (update->fromClause != NIL)
			dml_shape = "UPDATE_FROM";
	}
	else if (IsA(statement->stmt, DeleteStmt))
	{
		DeleteStmt *delete_stmt = (DeleteStmt *) statement->stmt;
		command_type = "DELETE";
#if PG_VERSION_NUM >= 180000
		has_returning = delete_stmt->returningClause != NULL;
#else
		has_returning = delete_stmt->returningList != NIL;
#endif
		if (delete_stmt->withClause != NULL)
			dml_shape = "CTE";
		else if (delete_stmt->usingClause != NIL)
			dml_shape = "DELETE_USING";
	}
	else
		ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL DML binder supports INSERT, UPDATE, or DELETE only")));
	(void) preflight_walker(statement->stmt, NULL);

	initStringInfo(&command);
	appendStringInfo(&command, "PREPARE \"%s\" AS %s", name, sql);
	if (SPI_connect() != SPI_OK_CONNECT)
		elog(ERROR, "AgentSQL SPI_connect failed");
	internal_prepare = true;
	PG_TRY();
	{
		result = SPI_execute(command.data, false, 0);
		internal_prepare = false;
	}
	PG_CATCH();
	{
		internal_prepare = false;
		SPI_finish();
		PG_RE_THROW();
	}
	PG_END_TRY();
	if (result != SPI_OK_UTILITY)
	{
		SPI_finish();
		elog(ERROR, "AgentSQL PREPARE failed");
	}
	SPI_finish();
	prepared = FetchPreparedStatement(name, true);
	if (prepared == NULL || prepared->plansource == NULL ||
		list_length(prepared->plansource->query_list) < 1)
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
						errmsg("AgentSQL analyzed tree is unavailable")));

	record_context = AllocSetContextCreate(TopMemoryContext,
										 "AgentSQL binder record",
										 ALLOCSET_SMALL_SIZES);
	previous = MemoryContextSwitchTo(record_context);
	record = palloc0(sizeof(BinderRecord));
	record->memory_context = record_context;
	record->name = pstrdup(name);
	record->backend_pid = MyProcPid;
	record->role_oid = GetUserId();
	record->role_name = pstrdup(GetUserNameFromId(record->role_oid, false));
	record->search_path = pstrdup(GetConfigOption("search_path", false, false));
	xid = GetTopTransactionIdIfAny();
	record->transaction_id = psprintf("%u:%d", xid, GetCurrentTransactionNestLevel());
	record->command_type = pstrdup(command_type);
	record->dml = dml;
	record->dml_shape = pstrdup(dml_shape);
	record->has_returning = has_returning;
	record->plansource = prepared->plansource;
	record->next = records;
	records = record;
	MemoryContextSwitchTo(previous);

	tree = nodeToString(prepared->plansource->query_list);
	previous = MemoryContextSwitchTo(record->memory_context);
	record->analyzed_digest = sha256_hex(tree, strlen(tree));
	MemoryContextSwitchTo(previous);

	{
		BinderWalkContext context;
		ListCell *cell;
		memset(&context, 0, sizeof(context));
		context.record = record;
		foreach(cell, prepared->plansource->query_list)
		{
			Query *query = lfirst_node(Query, cell);
			if ((!dml && query->commandType != CMD_SELECT) ||
				(dml && query->commandType != CMD_INSERT && query->commandType != CMD_UPDATE && query->commandType != CMD_DELETE))
				ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL rewrite changed the statement class")));
			if (dml && ((query->commandType == CMD_INSERT && strcmp(command_type, "INSERT") != 0) ||
				(query->commandType == CMD_UPDATE && strcmp(command_type, "UPDATE") != 0) ||
				(query->commandType == CMD_DELETE && strcmp(command_type, "DELETE") != 0)))
				ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL rewrite changed the DML action")));
			scan_query(&context, query, 0, "query");
		}
		if (dml && (!OidIsValid(record->target_relation_oid) || list_length(prepared->plansource->query_list) != 1))
			ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL DML target is not singular")));
		if (context.unsupported)
			ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL analyzed node is unsupported")));
	}
	previous = MemoryContextSwitchTo(record->memory_context);
	record->dependency_digest = dependency_digest(record);
	MemoryContextSwitchTo(previous);
	PG_RETURN_VOID();
}

Datum
agentsql_binder_seal(PG_FUNCTION_ARGS)
{
	char *name = text_to_cstring(PG_GETARG_TEXT_PP(0));
	BinderRecord *record = find_record(name, false);
	CachedPlan *plan;
	validate_record(record, false);
	if (record->sealed)
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE), errmsg("AgentSQL prepared statement already sealed")));
	plan = GetCachedPlan(record->plansource, NULL, NULL, NULL);
	ReleaseCachedPlan(plan, NULL);
	if (!record->plansource->is_valid || record->plansource->generation == 0)
		ereport(ERROR, (errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE), errmsg("AgentSQL prepared plan is invalid")));
	record->plan_generation = record->plansource->generation;
	record->sealed = true;
	PG_RETURN_VOID();
}

static void
scan_query(BinderWalkContext *context, Query *query, int view_depth, const char *path)
{
	ListCell *cell;
	int rti = 0;
	int output = 0;
	int saved_depth = context->stack_depth;
	int saved_view_depth = context->view_depth;

	if (context->stack_depth >= AGENTSQL_MAX_DEPTH)
		ereport(ERROR, (errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED), errmsg("AgentSQL query depth exceeded")));
	context->query_stack[context->stack_depth++] = query;
	context->view_depth = view_depth;
	context->record->node_count++;
	context->record->work_units++;
	context->record->has_recursive |= query->hasRecursive;
	context->record->has_modifying_cte |= query->hasModifyingCTE;
	if (context->record->dml && query->hasModifyingCTE)
		context->record->dml_shape = MemoryContextStrdup(context->record->memory_context, "WRITABLE_CTE");
	else if (context->record->dml && query->hasSubLinks && strcmp(context->record->dml_shape, "SIMPLE") == 0)
		context->record->dml_shape = MemoryContextStrdup(context->record->memory_context, "DML_SUBQUERY");

	foreach(cell, query->rtable)
	{
		RangeTblEntry *rte = lfirst_node(RangeTblEntry, cell);
		AclMode required = 0;
		Oid check_as_user = InvalidOid;
		rti++;
#if PG_VERSION_NUM >= 160000
		if (rte->perminfoindex > 0)
		{
			RTEPermissionInfo *permission = list_nth_node(RTEPermissionInfo,
				query->rteperminfos, rte->perminfoindex - 1);
			required = permission->requiredPerms;
			check_as_user = permission->checkAsUser;
		}
#else
		required = rte->requiredPerms;
		check_as_user = rte->checkAsUser;
#endif
		if (rte->rtekind == RTE_RELATION)
		{
			char kind = get_rel_relkind(rte->relid);
			add_relation(context, rte->relid, kind, rte->inh, required, check_as_user,
						 view_depth, psprintf("%s.rte%d", path, rti));
			if (context->record->dml && query->resultRelation == rti)
			{
				if (OidIsValid(context->record->target_relation_oid) && context->record->target_relation_oid != rte->relid)
					context->unsupported = true;
				context->record->target_relation_oid = rte->relid;
			}
		}
		else if (rte->rtekind == RTE_SUBQUERY)
		{
			int next_depth = view_depth;
			if (OidIsValid(rte->relid))
			{
				char kind = get_rel_relkind(rte->relid);
				int i;
				for (i = 0; i < context->view_stack_depth; i++)
					if (context->view_stack[i] == rte->relid)
						ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL recursive view is unsupported")));
				add_relation(context, rte->relid, kind, rte->inh, required, check_as_user,
							 view_depth, psprintf("%s.view%d", path, rti));
				context->view_stack[context->view_stack_depth++] = rte->relid;
				next_depth++;
			}
			scan_query(context, rte->subquery, next_depth, psprintf("%s.subquery%d", path, rti));
			if (OidIsValid(rte->relid))
				context->view_stack_depth--;
		}
		else if (rte->rtekind == RTE_JOIN || rte->rtekind == RTE_RESULT)
			;
		else
			context->unsupported = true;
		if (rte->securityQuals != NIL)
			walk_expression(context, query, (Node *) rte->securityQuals, "security_qual", "reference", ++context->record->edge_count);
	}

	foreach(cell, query->targetList)
	{
		TargetEntry *entry = lfirst_node(TargetEntry, cell);
		if (entry->resjunk)
		{
			if (context->record->dml)
				walk_expression(context, query, (Node *) entry->expr, "internal_read", "reference", ++context->record->edge_count);
			continue;
		}
		output++;
		if (context->record->dml)
		{
			Oid target = context->record->target_relation_oid;
			Oid type_oid = InvalidOid;
			Oid collation_oid = InvalidOid;
			int32 type_modifier = -1;
			const char *saved_site = context->site;
			const char *saved_usage = context->usage;
			int saved_group = context->contributor_group;
			if (OidIsValid(target) && entry->resno > 0)
				get_atttypetypmodcoll(target, entry->resno, &type_oid, &type_modifier, &collation_oid);
			context->site = IsA(entry->expr, SetToDefault) ? "write.explicit_default" : "write.explicit";
			context->usage = "write_target";
			context->contributor_group = ++context->record->edge_count;
			add_var(context, target, entry->resno, type_oid, collation_oid,
				OidIsValid(target) && entry->resno > 0 && OidIsValid(type_oid), false, type_oid, false);
			context->site = saved_site;
			context->usage = saved_usage;
			context->contributor_group = saved_group;
			walk_expression(context, query, (Node *) entry->expr,
				psprintf("assignment.%d", output), "reference", ++context->record->edge_count);
		}
		else if (is_composite_type(exprType((Node *) entry->expr)))
			add_var(context, InvalidOid, InvalidAttrNumber,
				exprType((Node *) entry->expr), exprCollation((Node *) entry->expr),
				false, false, exprType((Node *) entry->expr), true);
		if (!context->record->dml)
			walk_expression(context, query, (Node *) entry->expr,
							psprintf("target.%d", output), "output", output);
	}
	if (context->record->dml && query->commandType == CMD_INSERT && OidIsValid(context->record->target_relation_oid))
	{
		Relation relation = relation_open(context->record->target_relation_oid, NoLock);
		TupleDesc descriptor = RelationGetDescr(relation);
		int i;
		for (i = 0; i < descriptor->natts; i++)
		{
			Form_pg_attribute attribute = TupleDescAttr(descriptor, i);
			const char *saved_site;
			const char *saved_usage;
			int saved_group;
			if (attribute->attisdropped || record_has_write_target(context->record, context->record->target_relation_oid, attribute->attnum))
				continue;
			saved_site = context->site;
			saved_usage = context->usage;
			saved_group = context->contributor_group;
			context->site = "write.implicit_null";
			context->usage = "write_target";
			context->contributor_group = ++context->record->edge_count;
			add_var(context, context->record->target_relation_oid, attribute->attnum,
				attribute->atttypid, attribute->attcollation, true, false, attribute->atttypid, false);
			context->site = saved_site;
			context->usage = saved_usage;
			context->contributor_group = saved_group;
		}
		relation_close(relation, NoLock);
	}
	if (context->record->dml)
	{
		foreach(cell, query->returningList)
		{
			TargetEntry *entry = lfirst_node(TargetEntry, cell);
			walk_expression(context, query, (Node *) entry->expr, "returning", "reference", ++context->record->edge_count);
		}
		if (query->onConflict != NULL)
		{
			walk_expression(context, query, (Node *) query->onConflict->arbiterElems, "conflict_check", "reference", ++context->record->edge_count);
			walk_expression(context, query, query->onConflict->arbiterWhere, "conflict_check", "reference", ++context->record->edge_count);
			walk_expression(context, query, (Node *) query->onConflict->onConflictSet, "conflict_update", "reference", ++context->record->edge_count);
			walk_expression(context, query, query->onConflict->onConflictWhere, "conflict_update", "reference", ++context->record->edge_count);
		}
	}
	if (query->jointree != NULL)
		walk_expression(context, query, (Node *) query->jointree, "join_where", "reference", ++context->record->edge_count);
	if (query->havingQual != NULL)
		walk_expression(context, query, query->havingQual, "having", "reference", ++context->record->edge_count);
	if (query->limitOffset != NULL)
		walk_expression(context, query, query->limitOffset, "offset", "reference", ++context->record->edge_count);
	if (query->limitCount != NULL)
		walk_expression(context, query, query->limitCount, "limit", "reference", ++context->record->edge_count);
	foreach(cell, query->groupClause)
	{
		SortGroupClause *clause = lfirst_node(SortGroupClause, cell);
		ListCell *target;
		foreach(target, query->targetList)
		{
			TargetEntry *entry = lfirst_node(TargetEntry, target);
			if (entry->ressortgroupref == clause->tleSortGroupRef)
				walk_expression(context, query, (Node *) entry->expr, "group", "reference", ++context->record->edge_count);
		}
	}
	foreach(cell, query->sortClause)
	{
		SortGroupClause *clause = lfirst_node(SortGroupClause, cell);
		ListCell *target;
		foreach(target, query->targetList)
		{
			TargetEntry *entry = lfirst_node(TargetEntry, target);
			if (entry->ressortgroupref == clause->tleSortGroupRef)
				walk_expression(context, query, (Node *) entry->expr, "sort", "reference", ++context->record->edge_count);
		}
	}

	context->stack_depth = saved_depth;
	context->view_depth = saved_view_depth;
}

static bool
record_has_write_target(BinderRecord *record, Oid relation_oid, AttrNumber attnum)
{
	int i;
	for (i = 0; i < record->var_count; i++)
		if (record->vars[i].relation_oid == relation_oid && record->vars[i].attnum == attnum &&
			strcmp(record->vars[i].usage, "write_target") == 0)
			return true;
	return false;
}

static void
walk_expression(BinderWalkContext *context, Query *query, Node *node,
				const char *site, const char *usage, int contributor_group)
{
	const char *saved_site = context->site;
	const char *saved_usage = context->usage;
	int saved_group = context->contributor_group;
	(void) query;
	context->site = site;
	context->usage = usage;
	context->contributor_group = contributor_group;
	(void) expression_walker(node, context);
	context->site = saved_site;
	context->usage = saved_usage;
	context->contributor_group = saved_group;
}

static bool
expression_walker(Node *node, void *opaque)
{
	BinderWalkContext *context = (BinderWalkContext *) opaque;
	Oid result_type;
	if (node == NULL)
		return false;
	context->record->node_count++;
	context->record->work_units++;
	add_expression_objects(context, node);
	if (IsA(node, Var))
	{
		resolve_var(context, (Var *) node);
		return false;
	}
	if (IsA(node, Query))
	{
		scan_query(context, (Query *) node, context->view_depth, "expression_query");
		return false;
	}
	if (IsA(node, SubPlan) || IsA(node, AlternativeSubPlan))
	{
		context->unsupported = true;
		return false;
	}
	result_type = supported_expression_node(node) ? exprType(node) : InvalidOid;
	if (OidIsValid(result_type) && is_composite_type(result_type))
		add_var(context, InvalidOid, InvalidAttrNumber, result_type, InvalidOid,
				false, false, result_type, true);
	return expression_tree_walker(node, expression_walker, context);
}

static void
resolve_var(BinderWalkContext *context, Var *var)
{
	int target_depth = context->stack_depth - 1 - var->varlevelsup;
	if (target_depth < 0 || target_depth >= context->stack_depth)
	{
		add_var(context, InvalidOid, var->varattno, var->vartype, var->varcollid,
				false, var->varattno == InvalidAttrNumber, var->vartype,
				is_composite_type(var->vartype));
		return;
	}
	resolve_rte_var(context, context->query_stack[target_depth], var->varno,
				var->varattno, var->vartype, var->varcollid,
				context->view_depth, 0);
}

static void
resolve_rte_var(BinderWalkContext *context, Query *query, Index varno,
				AttrNumber attnum, Oid type_oid, Oid collid, int view_depth,
				int recursion_depth)
{
	RangeTblEntry *rte;
	if (recursion_depth > AGENTSQL_MAX_DEPTH || varno == 0 || varno > list_length(query->rtable))
	{
		add_var(context, InvalidOid, attnum, type_oid, collid, false,
				attnum == InvalidAttrNumber, type_oid, is_composite_type(type_oid));
		return;
	}
	rte = rt_fetch(varno, query->rtable);
	if (rte->rtekind == RTE_RELATION)
	{
		add_var(context, rte->relid, attnum, type_oid, collid, true,
				attnum == InvalidAttrNumber, type_oid, is_composite_type(type_oid));
		return;
	}
	if (rte->rtekind == RTE_SUBQUERY)
	{
		TargetEntry *entry = NULL;
		ListCell *cell;
		if (OidIsValid(rte->relid))
			add_var(context, rte->relid, attnum, type_oid, collid, true,
					attnum == InvalidAttrNumber, type_oid, is_composite_type(type_oid));
		foreach(cell, rte->subquery->targetList)
		{
			TargetEntry *candidate = lfirst_node(TargetEntry, cell);
			if (!candidate->resjunk && candidate->resno == attnum)
			{
				entry = candidate;
				break;
			}
		}
		if (entry == NULL)
			add_var(context, InvalidOid, attnum, type_oid, collid, false,
					attnum == InvalidAttrNumber, type_oid, is_composite_type(type_oid));
		else
		{
			int saved_depth = context->stack_depth;
			int saved_view = context->view_depth;
			if (context->stack_depth >= AGENTSQL_MAX_DEPTH)
				ereport(ERROR, (errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED), errmsg("AgentSQL view depth exceeded")));
			context->query_stack[context->stack_depth++] = rte->subquery;
			context->view_depth = view_depth + (OidIsValid(rte->relid) ? 1 : 0);
			(void) expression_walker((Node *) entry->expr, context);
			context->stack_depth = saved_depth;
			context->view_depth = saved_view;
		}
		return;
	}
	if (rte->rtekind == RTE_JOIN && attnum > 0)
	{
		JoinExpr *join = find_join_expr((Node *) query->jointree, varno);
		if (join != NULL && attnum <= rte->joinmergedcols &&
			attnum <= list_length(rte->joinleftcols) && attnum <= list_length(rte->joinrightcols))
		{
			AttrNumber left_attnum = list_nth_int(rte->joinleftcols, attnum - 1);
			AttrNumber right_attnum = list_nth_int(rte->joinrightcols, attnum - 1);
			resolve_join_input(context, query, join->larg, left_attnum, type_oid, collid,
							   view_depth, recursion_depth + 1);
			resolve_join_input(context, query, join->rarg, right_attnum, type_oid, collid,
							   view_depth, recursion_depth + 1);
			return;
		}
		if (attnum <= list_length(rte->joinaliasvars))
		{
			Node *alias = list_nth(rte->joinaliasvars, attnum - 1);
			(void) expression_walker(alias, context);
			return;
		}
	}
	add_var(context, InvalidOid, attnum, type_oid, collid, false,
			attnum == InvalidAttrNumber, type_oid, is_composite_type(type_oid));
}

static JoinExpr *
find_join_expr(Node *node, Index rtindex)
{
	ListCell *cell;
	JoinExpr *found;
	if (node == NULL)
		return NULL;
	if (IsA(node, JoinExpr))
	{
		JoinExpr *join = (JoinExpr *) node;
		if (join->rtindex == rtindex)
			return join;
		found = find_join_expr(join->larg, rtindex);
		if (found != NULL)
			return found;
		return find_join_expr(join->rarg, rtindex);
	}
	if (IsA(node, FromExpr))
	{
		foreach(cell, ((FromExpr *) node)->fromlist)
		{
			found = find_join_expr(lfirst(cell), rtindex);
			if (found != NULL)
				return found;
		}
	}
	return NULL;
}

static void
resolve_join_input(BinderWalkContext *context, Query *query, Node *join_input,
				   AttrNumber attnum, Oid type_oid, Oid collid,
				   int view_depth, int recursion_depth)
{
	if (IsA(join_input, RangeTblRef))
		resolve_rte_var(context, query, ((RangeTblRef *) join_input)->rtindex,
					attnum, type_oid, collid, view_depth, recursion_depth);
	else if (IsA(join_input, JoinExpr))
		resolve_rte_var(context, query, ((JoinExpr *) join_input)->rtindex,
					attnum, type_oid, collid, view_depth, recursion_depth);
	else
		add_var(context, InvalidOid, attnum, type_oid, collid, false, false,
				type_oid, is_composite_type(type_oid));
}

static void
add_relation(BinderWalkContext *context, Oid oid, char kind, bool inh,
			 AclMode required_perms, Oid check_as_user, int view_depth,
			 const char *path)
{
	BinderRecord *record = context->record;
	int i;
	for (i = 0; i < record->relation_count; i++)
		if (record->relations[i].oid == oid)
		{
			if (view_depth > record->relations[i].view_depth)
				record->relations[i].view_depth = view_depth;
			record->relations[i].required_perms |= required_perms;
			return;
		}
	ensure_relation_capacity(record);
	record->relations[record->relation_count].oid = oid;
	record->relations[record->relation_count].kind = kind;
	record->relations[record->relation_count].inh = inh;
	record->relations[record->relation_count].required_perms = required_perms;
	record->relations[record->relation_count].check_as_user = check_as_user;
	record->relations[record->relation_count].view_depth = view_depth;
	record->relations[record->relation_count].path = MemoryContextStrdup(record->memory_context, path);
	record->relation_count++;
}

static void
add_var(BinderWalkContext *context, Oid relid, AttrNumber attnum,
		Oid type_oid, Oid collid, bool complete, bool whole_row,
		Oid result_type_oid, bool result_composite)
{
	BinderRecord *record = context->record;
	int i;
	for (i = 0; i < record->var_count; i++)
	{
		BinderVar *existing = &record->vars[i];
		if (existing->relation_oid == relid && existing->attnum == attnum &&
			existing->contributor_group == context->contributor_group &&
			strcmp(existing->site, context->site ? context->site : "unknown") == 0 &&
			strcmp(existing->usage, context->usage ? context->usage : "reference") == 0)
			return;
	}
	ensure_var_capacity(record);
	record->vars[record->var_count].site = MemoryContextStrdup(record->memory_context, context->site ? context->site : "unknown");
	record->vars[record->var_count].relation_oid = relid;
	record->vars[record->var_count].attnum = attnum;
	record->vars[record->var_count].type_oid = type_oid;
	record->vars[record->var_count].collation_oid = collid;
	record->vars[record->var_count].usage = MemoryContextStrdup(record->memory_context, context->usage ? context->usage : "reference");
	record->vars[record->var_count].query_depth = context->stack_depth - 1;
	record->vars[record->var_count].view_depth = context->view_depth;
	record->vars[record->var_count].contributor_group = context->contributor_group;
	record->vars[record->var_count].contributor_complete = complete;
	record->vars[record->var_count].whole_row = whole_row;
	record->vars[record->var_count].result_type_oid = result_type_oid;
	record->vars[record->var_count].result_composite = result_composite;
	record->var_count++;
	add_object(record, "type", type_oid);
	if (OidIsValid(collid))
		add_object(record, "collation", collid);
}

static void
add_object(BinderRecord *record, const char *kind, Oid oid)
{
	int i;
	if (!OidIsValid(oid))
		return;
	for (i = 0; i < record->object_count; i++)
		if (record->objects[i].oid == oid && strcmp(record->objects[i].kind, kind) == 0)
			return;
	ensure_object_capacity(record);
	record->objects[record->object_count].kind = MemoryContextStrdup(record->memory_context, kind);
	record->objects[record->object_count].oid = oid;
	record->object_count++;
}

static void
add_expression_objects(BinderWalkContext *context, Node *node)
{
	BinderRecord *record = context->record;
	Oid type_oid = supported_expression_node(node) ? exprType(node) : InvalidOid;
	Oid collid = supported_expression_node(node) ? exprCollation(node) : InvalidOid;
	if (OidIsValid(type_oid)) add_object(record, "type", type_oid);
	if (OidIsValid(collid)) add_object(record, "collation", collid);
	if (IsA(node, FuncExpr))
		add_object(record, "function", ((FuncExpr *) node)->funcid);
	else if (IsA(node, Aggref))
	{
		add_object(record, "aggregate", ((Aggref *) node)->aggfnoid);
		add_object(record, "function", ((Aggref *) node)->aggfnoid);
	}
	else if (IsA(node, WindowFunc))
	{
		add_object(record, "window", ((WindowFunc *) node)->winfnoid);
		add_object(record, "function", ((WindowFunc *) node)->winfnoid);
	}
	else if (IsA(node, OpExpr) || IsA(node, DistinctExpr) || IsA(node, NullIfExpr))
	{
		OpExpr *op = (OpExpr *) node;
		add_object(record, "operator", op->opno);
		add_object(record, "function", op->opfuncid);
	}
	else if (IsA(node, ScalarArrayOpExpr))
	{
		ScalarArrayOpExpr *op = (ScalarArrayOpExpr *) node;
		add_object(record, "operator", op->opno);
		add_object(record, "function", op->opfuncid);
	}
}

static bool
supported_expression_node(Node *node)
{
	if (node == NULL)
		return false;
	switch (nodeTag(node))
	{
		case T_Var: case T_Const: case T_Param: case T_Aggref: case T_GroupingFunc:
		case T_WindowFunc: case T_SubscriptingRef: case T_FuncExpr: case T_NamedArgExpr:
		case T_OpExpr: case T_DistinctExpr: case T_NullIfExpr: case T_ScalarArrayOpExpr:
		case T_BoolExpr: case T_SubLink: case T_SubPlan: case T_AlternativeSubPlan:
		case T_FieldSelect: case T_FieldStore: case T_RelabelType: case T_CoerceViaIO:
		case T_ArrayCoerceExpr: case T_ConvertRowtypeExpr: case T_CollateExpr:
		case T_CaseExpr: case T_CaseTestExpr: case T_ArrayExpr: case T_RowExpr:
		case T_RowCompareExpr: case T_CoalesceExpr: case T_MinMaxExpr: case T_SQLValueFunction:
		case T_XmlExpr: case T_NullTest: case T_BooleanTest: case T_CoerceToDomain:
		case T_CoerceToDomainValue: case T_SetToDefault: case T_CurrentOfExpr:
		case T_NextValueExpr: case T_InferenceElem:
			return true;
		default:
			return false;
	}
}

static void
ensure_relation_capacity(BinderRecord *record)
{
	MemoryContext previous;
	if (record->relation_count < record->relation_capacity) return;
	previous = MemoryContextSwitchTo(record->memory_context);
	record->relation_capacity = record->relation_capacity == 0 ? 16 : record->relation_capacity * 2;
	record->relations = record->relations == NULL ? palloc0(sizeof(BinderRelation) * record->relation_capacity) : repalloc(record->relations, sizeof(BinderRelation) * record->relation_capacity);
	MemoryContextSwitchTo(previous);
}

static void
ensure_var_capacity(BinderRecord *record)
{
	MemoryContext previous;
	if (record->var_count < record->var_capacity) return;
	previous = MemoryContextSwitchTo(record->memory_context);
	record->var_capacity = record->var_capacity == 0 ? 32 : record->var_capacity * 2;
	record->vars = record->vars == NULL ? palloc0(sizeof(BinderVar) * record->var_capacity) : repalloc(record->vars, sizeof(BinderVar) * record->var_capacity);
	MemoryContextSwitchTo(previous);
}

static void
ensure_object_capacity(BinderRecord *record)
{
	MemoryContext previous;
	if (record->object_count < record->object_capacity) return;
	previous = MemoryContextSwitchTo(record->memory_context);
	record->object_capacity = record->object_capacity == 0 ? 32 : record->object_capacity * 2;
	record->objects = record->objects == NULL ? palloc0(sizeof(BinderObject) * record->object_capacity) : repalloc(record->objects, sizeof(BinderObject) * record->object_capacity);
	MemoryContextSwitchTo(previous);
}

static bool
is_composite_type(Oid type_oid)
{
	return type_oid == RECORDOID || (OidIsValid(type_oid) && type_is_rowtype(type_oid));
}

static char *
sha256_hex(const char *bytes, Size length)
{
	pg_cryptohash_ctx *ctx = pg_cryptohash_create(PG_SHA256);
	uint8 digest[32];
	char *hex = palloc(32 * 2 + 1);
	static const char digits[] = "0123456789abcdef";
	int i;
	if (ctx == NULL || pg_cryptohash_init(ctx) < 0 ||
		pg_cryptohash_update(ctx, (const uint8 *) bytes, length) < 0 ||
		pg_cryptohash_final(ctx, digest, sizeof(digest)) < 0)
		elog(ERROR, "AgentSQL SHA-256 failed");
	pg_cryptohash_free(ctx);
	for (i = 0; i < 32; i++)
	{
		hex[i * 2] = digits[digest[i] >> 4];
		hex[i * 2 + 1] = digits[digest[i] & 15];
	}
	hex[32 * 2] = '\0';
	return hex;
}

static int
compare_relations(const void *left, const void *right)
{
	const BinderRelation *a = left, *b = right;
	return a->oid < b->oid ? -1 : a->oid > b->oid ? 1 : 0;
}

static int
compare_objects(const void *left, const void *right)
{
	const BinderObject *a = left, *b = right;
	int cmp = strcmp(a->kind, b->kind);
	if (cmp != 0) return cmp;
	return a->oid < b->oid ? -1 : a->oid > b->oid ? 1 : 0;
}

static char *
dependency_digest(BinderRecord *record)
{
	StringInfoData buffer;
	int i;
	qsort(record->relations, record->relation_count, sizeof(BinderRelation), compare_relations);
	qsort(record->objects, record->object_count, sizeof(BinderObject), compare_objects);
	initStringInfo(&buffer);
	for (i = 0; i < record->relation_count; i++)
		appendStringInfo(&buffer, "r:%u:%c;", record->relations[i].oid, record->relations[i].kind);
	for (i = 0; i < record->object_count; i++)
		appendStringInfo(&buffer, "%s:%u;", record->objects[i].kind, record->objects[i].oid);
	return sha256_hex(buffer.data, buffer.len);
}

Datum
agentsql_binder_capabilities(PG_FUNCTION_ARGS)
{
	Datum result;
	MemoryContext caller_context = CurrentMemoryContext;
	MemoryContext previous_context;
	StringInfoData sql;
	StringInfoData json;
	HeapTuple tuple;
	TupleDesc descriptor;
	int column;
	static const char *keys[] = {"types","functions","operators","casts","relation_ams","index_ams","opclasses","opfamilies","am_operators","am_procedures","type_io_functions","collations","aggregate_support","window_support"};
	initStringInfo(&sql);
	appendStringInfoString(&sql,
		"SELECT ARRAY(SELECT oid FROM pg_catalog.pg_type WHERE oid<16384 AND typtype IN ('b','p') ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_proc WHERE oid<16384 AND provolatile='i' AND NOT prosecdef ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_operator WHERE oid<16384 ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_cast WHERE oid<16384 ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_am WHERE oid<16384 AND amtype='t' ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_am WHERE oid<16384 AND amtype='i' ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_opclass WHERE oid<16384 ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_opfamily WHERE oid<16384 ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_amop WHERE oid<16384 ORDER BY oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_amproc WHERE oid<16384 ORDER BY oid)::text,"
		"ARRAY(SELECT DISTINCT f::oid FROM pg_catalog.pg_type t CROSS JOIN LATERAL unnest(ARRAY[t.typinput,t.typoutput,t.typreceive,t.typsend]) f WHERE t.oid<16384 AND f<>0 ORDER BY f::oid)::text,"
		"ARRAY(SELECT oid FROM pg_catalog.pg_collation WHERE oid<16384 AND collprovider IN ('c','d') ORDER BY oid)::text,"
		"ARRAY(SELECT DISTINCT f::oid FROM pg_catalog.pg_aggregate a CROSS JOIN LATERAL unnest(ARRAY[a.aggtransfn,a.aggfinalfn,a.aggcombinefn,a.aggserialfn,a.aggdeserialfn,a.aggmtransfn,a.aggminvtransfn,a.aggmfinalfn]) f WHERE a.aggfnoid<16384 AND f<>0 ORDER BY f::oid)::text,"
		"ARRAY(SELECT prosupport::oid FROM pg_catalog.pg_proc WHERE oid<16384 AND prosupport<>0 ORDER BY prosupport::oid)::text");
	if (SPI_connect() != SPI_OK_CONNECT) elog(ERROR, "AgentSQL SPI_connect failed");
	if (SPI_execute(sql.data, true, 1) != SPI_OK_SELECT || SPI_processed != 1)
	{
		SPI_finish();
		elog(ERROR, "AgentSQL capability query failed");
	}
	tuple = SPI_tuptable->vals[0];
	descriptor = SPI_tuptable->tupdesc;
	initStringInfo(&json);
	appendStringInfo(&json,"{\"abi\":\"%s\",\"server_major\":%s,\"extension_version\":\"%s\",\"extension_hash\":\"agentsql-binder-source-v1-pg%s\",\"node_manifest_hash\":\"query-rte-var-join-v1-pg%s\",\"allowlist_hash\":\"builtin-exact-oids-v1-pg%s\",\"matview\":false,\"allowlist\":{",AGENTSQL_ABI,PG_MAJORVERSION,AGENTSQL_EXT_VERSION,PG_MAJORVERSION,PG_MAJORVERSION,PG_MAJORVERSION);
	for (column=1;column<=lengthof(keys);column++)
	{
		char *array = SPI_getvalue(tuple,descriptor,column);
		Size length;
		if (column>1) appendStringInfoChar(&json,',');
		appendStringInfo(&json,"\"%s\":[",keys[column-1]);
		if (array != NULL && strcmp(array,"{}") != 0)
		{
			length=strlen(array);
			if (length>=2) appendBinaryStringInfo(&json,array+1,length-2);
		}
		appendStringInfoChar(&json,']');
	}
	appendStringInfoString(&json,"}}");
	result = DirectFunctionCall1(jsonb_in,CStringGetDatum(json.data));
	previous_context = MemoryContextSwitchTo(caller_context);
	result = datumCopy(result, false, -1);
	MemoryContextSwitchTo(previous_context);
	SPI_finish();
	PG_RETURN_DATUM(result);
}

static Datum
tuple_result(FunctionCallInfo fcinfo, Datum *values, bool *nulls)
{
	TupleDesc descriptor;
	HeapTuple tuple;
	if (get_call_result_type(fcinfo, NULL, &descriptor) != TYPEFUNC_COMPOSITE)
		elog(ERROR, "AgentSQL function must return a composite type");
	descriptor = BlessTupleDesc(descriptor);
	tuple = heap_form_tuple(descriptor, values, nulls);
	return HeapTupleGetDatum(tuple);
}

Datum
agentsql_binder_manifest(PG_FUNCTION_ARGS)
{
	FuncCallContext *funcctx; BinderRecord *record;
	if (SRF_IS_FIRSTCALL()) { MemoryContext old; char *name=text_to_cstring(PG_GETARG_TEXT_PP(0)); funcctx=SRF_FIRSTCALL_INIT(); old=MemoryContextSwitchTo(funcctx->multi_call_memory_ctx); record=find_record(name,false); validate_record(record,false); funcctx->user_fctx=record; funcctx->max_calls=1; MemoryContextSwitchTo(old); }
	funcctx=SRF_PERCALL_SETUP(); record=funcctx->user_fctx;
	if (funcctx->call_cntr<1) { Datum values[17]; bool nulls[17]={false}; values[0]=CStringGetTextDatum(record->name); values[1]=Int32GetDatum(record->backend_pid); values[2]=CStringGetTextDatum(record->transaction_id); values[3]=ObjectIdGetDatum(record->role_oid); values[4]=CStringGetTextDatum(record->role_name); values[5]=CStringGetTextDatum(record->search_path); values[6]=CStringGetTextDatum(record->analyzed_digest); values[7]=CStringGetTextDatum(record->dependency_digest); values[8]=Int64GetDatum((int64)record->plan_generation); values[9]=Int64GetDatum((int64)record->replan_count); values[10]=BoolGetDatum(record->invalidated); values[11]=CStringGetTextDatum(record->command_type); values[12]=BoolGetDatum(record->has_recursive); values[13]=BoolGetDatum(record->has_modifying_cte); values[14]=Int32GetDatum(record->node_count); values[15]=Int32GetDatum(record->edge_count); values[16]=Int32GetDatum(record->work_units); SRF_RETURN_NEXT(funcctx,tuple_result(fcinfo,values,nulls)); }
	SRF_RETURN_DONE(funcctx);
}

Datum
agentsql_binder_dml_manifest(PG_FUNCTION_ARGS)
{
	FuncCallContext *funcctx; BinderRecord *record;
	if (SRF_IS_FIRSTCALL()) { MemoryContext old; char *name=text_to_cstring(PG_GETARG_TEXT_PP(0)); funcctx=SRF_FIRSTCALL_INIT(); old=MemoryContextSwitchTo(funcctx->multi_call_memory_ctx); record=find_record(name,false); validate_record(record,false); if (!record->dml) ereport(ERROR,(errcode(ERRCODE_WRONG_OBJECT_TYPE),errmsg("AgentSQL statement is not DML"))); funcctx->user_fctx=record; funcctx->max_calls=1; MemoryContextSwitchTo(old); }
	funcctx=SRF_PERCALL_SETUP(); record=funcctx->user_fctx;
	if (funcctx->call_cntr<1) { Datum values[20]; bool nulls[20]={false}; values[0]=CStringGetTextDatum(record->name); values[1]=Int32GetDatum(record->backend_pid); values[2]=CStringGetTextDatum(record->transaction_id); values[3]=ObjectIdGetDatum(record->role_oid); values[4]=CStringGetTextDatum(record->role_name); values[5]=CStringGetTextDatum(record->search_path); values[6]=CStringGetTextDatum(record->analyzed_digest); values[7]=CStringGetTextDatum(record->dependency_digest); values[8]=Int64GetDatum((int64)record->plan_generation); values[9]=Int64GetDatum((int64)record->replan_count); values[10]=BoolGetDatum(record->invalidated); values[11]=CStringGetTextDatum(record->command_type); values[12]=BoolGetDatum(record->has_recursive); values[13]=BoolGetDatum(record->has_modifying_cte); values[14]=Int32GetDatum(record->node_count); values[15]=Int32GetDatum(record->edge_count); values[16]=Int32GetDatum(record->work_units); values[17]=ObjectIdGetDatum(record->target_relation_oid); values[18]=CStringGetTextDatum(record->dml_shape); values[19]=BoolGetDatum(record->has_returning); SRF_RETURN_NEXT(funcctx,tuple_result(fcinfo,values,nulls)); }
	SRF_RETURN_DONE(funcctx);
}

Datum
agentsql_binder_relations(PG_FUNCTION_ARGS)
{
	FuncCallContext *funcctx; BinderRecord *record;
	if (SRF_IS_FIRSTCALL()) { MemoryContext old; char *name=text_to_cstring(PG_GETARG_TEXT_PP(0)); funcctx=SRF_FIRSTCALL_INIT(); old=MemoryContextSwitchTo(funcctx->multi_call_memory_ctx); record=find_record(name,false); validate_record(record,false); funcctx->user_fctx=record; funcctx->max_calls=record->relation_count; MemoryContextSwitchTo(old); }
	funcctx=SRF_PERCALL_SETUP(); record=funcctx->user_fctx;
	if (funcctx->call_cntr<funcctx->max_calls) { BinderRelation *r=&record->relations[funcctx->call_cntr]; Datum values[7]; bool nulls[7]={false}; values[0]=ObjectIdGetDatum(r->oid); values[1]=CharGetDatum(r->kind); values[2]=BoolGetDatum(r->inh); values[3]=Int32GetDatum((int32)r->required_perms); values[4]=ObjectIdGetDatum(r->check_as_user); values[5]=Int32GetDatum(r->view_depth); values[6]=CStringGetTextDatum(r->path); SRF_RETURN_NEXT(funcctx,tuple_result(fcinfo,values,nulls)); }
	SRF_RETURN_DONE(funcctx);
}

Datum
agentsql_binder_vars(PG_FUNCTION_ARGS)
{
	FuncCallContext *funcctx; BinderRecord *record;
	if (SRF_IS_FIRSTCALL()) { MemoryContext old; char *name=text_to_cstring(PG_GETARG_TEXT_PP(0)); funcctx=SRF_FIRSTCALL_INIT(); old=MemoryContextSwitchTo(funcctx->multi_call_memory_ctx); record=find_record(name,false); validate_record(record,false); funcctx->user_fctx=record; funcctx->max_calls=record->var_count; MemoryContextSwitchTo(old); }
	funcctx=SRF_PERCALL_SETUP(); record=funcctx->user_fctx;
	if (funcctx->call_cntr<funcctx->max_calls) { BinderVar *v=&record->vars[funcctx->call_cntr]; Datum values[13]; bool nulls[13]={false}; values[0]=CStringGetTextDatum(v->site); values[1]=ObjectIdGetDatum(v->relation_oid); values[2]=Int16GetDatum(v->attnum); values[3]=ObjectIdGetDatum(v->type_oid); values[4]=ObjectIdGetDatum(v->collation_oid); values[5]=CStringGetTextDatum(v->usage); values[6]=Int32GetDatum(v->query_depth); values[7]=Int32GetDatum(v->view_depth); values[8]=Int32GetDatum(v->contributor_group); values[9]=BoolGetDatum(v->contributor_complete); values[10]=BoolGetDatum(v->whole_row); values[11]=ObjectIdGetDatum(v->result_type_oid); values[12]=BoolGetDatum(v->result_composite); SRF_RETURN_NEXT(funcctx,tuple_result(fcinfo,values,nulls)); }
	SRF_RETURN_DONE(funcctx);
}

Datum
agentsql_binder_objects(PG_FUNCTION_ARGS)
{
	FuncCallContext *funcctx; BinderRecord *record;
	if (SRF_IS_FIRSTCALL()) { MemoryContext old; char *name=text_to_cstring(PG_GETARG_TEXT_PP(0)); funcctx=SRF_FIRSTCALL_INIT(); old=MemoryContextSwitchTo(funcctx->multi_call_memory_ctx); record=find_record(name,false); validate_record(record,false); funcctx->user_fctx=record; funcctx->max_calls=record->object_count; MemoryContextSwitchTo(old); }
	funcctx=SRF_PERCALL_SETUP(); record=funcctx->user_fctx;
	if (funcctx->call_cntr<funcctx->max_calls) { BinderObject *o=&record->objects[funcctx->call_cntr]; Datum values[2]; bool nulls[2]={false}; values[0]=CStringGetTextDatum(o->kind); values[1]=ObjectIdGetDatum(o->oid); SRF_RETURN_NEXT(funcctx,tuple_result(fcinfo,values,nulls)); }
	SRF_RETURN_DONE(funcctx);
}
