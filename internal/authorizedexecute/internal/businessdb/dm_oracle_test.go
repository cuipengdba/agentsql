package businessdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	dm8 "github.com/godoes/gorm-dameng/dm8"
	gooranetwork "github.com/sijms/go-ora/v2/network"
	"github.com/stretchr/testify/require"
)

func TestBuildDMDSNHandlesDriverCredentialRules(t *testing.T) {
	datasource := model.Datasource{
		Host: "127.0.0.1", Port: 5236, Database: "APP", Username: "user@tenant",
		StmtTimeoutMS: 5_000,
	}
	dsn, err := buildDMDSN(datasource, "p@ss:/word")
	require.NoError(t, err)
	require.Equal(t, "dm://user@tenant:p@ss:/word@127.0.0.1:5236?schema=APP&connectTimeout=5000", dsn)

	datasource.Username = "user:name"
	_, err = buildDMDSN(datasource, "password")
	require.Error(t, err)
	datasource.Username = "user"
	_, err = buildDMDSN(datasource, "ambiguous?password")
	require.Error(t, err)
}

func TestOracleStyleSchemaQueriesUseBinds(t *testing.T) {
	tables := []SchemaTable{
		{Table: "CUSTOMERS"},
		{Schema: "REPORTING", Table: "DAILY_TOTALS"},
	}
	dmQuery, dmArgs := dmSchemaQuery("APP", tables)
	require.NotContains(t, dmQuery, "CUSTOMERS")
	require.Equal(t, []any{"APP", "CUSTOMERS", "REPORTING", "DAILY_TOTALS"}, dmArgs)
	require.Equal(t, 4, strings.Count(dmQuery, "?"))

	oracleQuery, oracleArgs := oracleSchemaQuery("APP", tables)
	require.NotContains(t, oracleQuery, "DAILY_TOTALS")
	require.Contains(t, oracleQuery, "OWNER=:1")
	require.Contains(t, oracleQuery, "TABLE_NAME=:4")
	require.Equal(t, dmArgs, oracleArgs)
}

func TestLimitedDialectSelectValidatorFailsClosed(t *testing.T) {
	for _, sqlText := range []string{
		"SELECT CUSTOMER_ID, NAME FROM APP.CUSTOMERS WHERE STATUS = 'ACTIVE'",
		`SELECT "MixedCase" FROM "APP"."Customer"`,
		"SELECT LEVEL AS N FROM DUAL CONNECT BY LEVEL <= 3",
	} {
		require.NoError(t, validateLimitedSelect(sqlText), sqlText)
	}
	for _, sqlText := range []string{
		"", "WITH X AS (SELECT 1 FROM DUAL) SELECT * FROM X", "SELECT COUNT(*) FROM T",
		"SELECT * FROM T; DELETE FROM T", "SELECT * INTO BACKUP FROM T", "SELECT SEQ.NEXTVAL FROM DUAL",
		"SELECT * FROM T -- trailing", "UPDATE T SET V=1", "SELECT * FROM T WHERE ID=:1",
	} {
		require.Error(t, validateLimitedSelect(sqlText), sqlText)
	}
}

func TestLimitedDialectPaginationAndKeywordBoundaries(t *testing.T) {
	accepted := []struct {
		dialect string
		sql     string
	}{
		{dialect: "dm", sql: "SELECT TOP 5 ID FROM APP.CUSTOMERS"},
		{dialect: "dm", sql: "SELECT ID FROM APP.CUSTOMERS LIMIT 5"},
		{dialect: "dm", sql: "SELECT ID FROM APP.CUSTOMERS FETCH FIRST 5 ROWS ONLY"},
		{dialect: "dm", sql: "SELECT ID FROM APP.CUSTOMERS WHERE ROWNUM <= 5"},
		{dialect: "oracle", sql: "SELECT ID FROM APP.CUSTOMERS FETCH FIRST 5 ROWS ONLY"},
		{dialect: "oracle", sql: "SELECT ID FROM APP.CUSTOMERS WHERE ROWNUM <= 5"},
		{dialect: "oracle", sql: "SELECT DUMMY FROM DUAL"},
		{dialect: "oracle", sql: `SELECT "LIMIT", UPDATE_COUNT, NEXTVALUE FROM "APP"."T"`},
		{dialect: "oracle", sql: "SELECT '-- LIMIT CURRVAL UPDATE' AS TEXT_VALUE FROM DUAL"},
	}
	for _, test := range accepted {
		require.NoError(t, validateLimitedSelectForDialect(test.dialect, test.sql), test.sql)
	}

	rejected := []struct {
		dialect string
		sql     string
	}{
		{dialect: "oracle", sql: "SELECT ID FROM APP.CUSTOMERS LIMIT 5"},
		{dialect: "oracle", sql: "SELECT TOP 5 ID FROM APP.CUSTOMERS"},
		{dialect: "oracle", sql: "SELECT SEQ.NEXTVAL FROM DUAL"},
		{dialect: "oracle", sql: "SELECT SEQ.CURRVAL FROM DUAL"},
		{dialect: "dm", sql: "SELECT SEQ.CURRVAL FROM DUAL"},
		{dialect: "dm", sql: "SELECT ID FROM T /*+ INDEX(T IDX_T) */"},
		{dialect: "oracle", sql: "SELECT ID FROM T -- trailing"},
	}
	for _, test := range rejected {
		require.Error(t, validateLimitedSelectForDialect(test.dialect, test.sql), test.sql)
	}
}

func TestBuildSampleQueryUsesVendorPagination(t *testing.T) {
	tableName := `"APP"."CUSTOMERS"`
	projections := []string{`"ID"`, `"NAME"`}
	require.Equal(
		t,
		`SELECT "ID","NAME" FROM "APP"."CUSTOMERS" LIMIT 10`,
		buildSampleQuery("dm", tableName, projections, 10),
	)
	require.Equal(
		t,
		`SELECT "ID","NAME" FROM "APP"."CUSTOMERS" FETCH FIRST 10 ROWS ONLY`,
		buildSampleQuery("oracle", tableName, projections, 10),
	)
	require.Equal(
		t,
		`SELECT "ID","NAME" FROM "APP"."CUSTOMERS" LIMIT 10`,
		buildSampleQuery("postgres", tableName, projections, 10),
	)
}

func TestLimitedDialectWritePathsStillFailClosed(t *testing.T) {
	executor := &limitedSQLExecutor{dialect: "oracle", readOnly: true}
	_, err := executor.Execute(context.Background(), "DELETE FROM T")
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeReadOnly, databaseError.Code)

	executor.readOnly = false
	_, err = executor.Execute(context.Background(), "DELETE FROM T")
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBStageExecute, databaseError.Stage)
	require.Equal(t, DBErrorCodeExecution, databaseError.Code)
}

func TestDMAndOracleErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		classify   limitedDialectErrorClassifier
		cause      error
		wantKind   DBErrorKind
		wantCode   DBErrorCode
		wantDriver string
	}{
		{name: "dm syntax", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2007}, wantKind: DBErrorKindSyntax, wantCode: DBErrorCodeSyntax, wantDriver: "-2007"},
		{name: "dm permission range", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -5516}, wantKind: DBErrorKindPermission, wantCode: DBErrorCodePermission, wantDriver: "-5516"},
		{name: "dm authentication", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2501}, wantKind: DBErrorKindAuthentication, wantCode: DBErrorCodeAuthentication, wantDriver: "-2501"},
		{name: "dm column", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2111}, wantKind: DBErrorKindColumnNotFound, wantCode: DBErrorCodeColumnNotFound, wantDriver: "-2111"},
		{name: "dm object", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2106}, wantKind: DBErrorKindObjectNotFound, wantCode: DBErrorCodeObjectNotFound, wantDriver: "-2106"},
		{name: "dm schema", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2103}, wantKind: DBErrorKindObjectNotFound, wantCode: DBErrorCodeObjectNotFound, wantDriver: "-2103"},
		{name: "dm lock timeout", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -6407}, wantKind: DBErrorKindRetryable, wantCode: DBErrorCodeRetryable, wantDriver: "-6407"},
		{name: "dm constraint", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -6602}, wantKind: DBErrorKindConstraint, wantCode: DBErrorCodeConstraint, wantDriver: "-6602"},
		{name: "dm unknown", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -77777}, wantKind: DBErrorKindExecution, wantCode: DBErrorCodeExecution, wantDriver: "-77777"},
		{name: "dm connection", classify: classifyDMError, cause: errors.New("connection refused"), wantKind: DBErrorKindConnection, wantCode: DBErrorCodeConnection},
		{name: "oracle syntax", classify: classifyOracleError, cause: gooranetwork.NewOracleError(900), wantKind: DBErrorKindSyntax, wantCode: DBErrorCodeSyntax, wantDriver: "900"},
		{name: "oracle permission", classify: classifyOracleError, cause: gooranetwork.NewOracleError(1031), wantKind: DBErrorKindPermission, wantCode: DBErrorCodePermission, wantDriver: "1031"},
		{name: "oracle authentication", classify: classifyOracleError, cause: gooranetwork.NewOracleError(1017), wantKind: DBErrorKindAuthentication, wantCode: DBErrorCodeAuthentication, wantDriver: "1017"},
		{name: "oracle connection", classify: classifyOracleError, cause: gooranetwork.NewOracleError(12514), wantKind: DBErrorKindConnection, wantCode: DBErrorCodeConnection, wantDriver: "12514"},
		{name: "oracle object", classify: classifyOracleError, cause: gooranetwork.NewOracleError(4043), wantKind: DBErrorKindObjectNotFound, wantCode: DBErrorCodeObjectNotFound, wantDriver: "4043"},
		{name: "oracle resource busy", classify: classifyOracleError, cause: gooranetwork.NewOracleError(54), wantKind: DBErrorKindRetryable, wantCode: DBErrorCodeRetryable, wantDriver: "54"},
		{name: "oracle deadlock", classify: classifyOracleError, cause: gooranetwork.NewOracleError(60), wantKind: DBErrorKindRetryable, wantCode: DBErrorCodeRetryable, wantDriver: "60"},
		{name: "oracle temp space", classify: classifyOracleError, cause: gooranetwork.NewOracleError(1652), wantKind: DBErrorKindResource, wantCode: DBErrorCodeResource, wantDriver: "1652"},
		{name: "oracle shared pool", classify: classifyOracleError, cause: gooranetwork.NewOracleError(4031), wantKind: DBErrorKindResource, wantCode: DBErrorCodeResource, wantDriver: "4031"},
		{name: "oracle unknown", classify: classifyOracleError, cause: gooranetwork.NewOracleError(77777), wantKind: DBErrorKindExecution, wantCode: DBErrorCodeExecution, wantDriver: "77777"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified, ok := test.classify(context.Background(), DBStageQuery, test.cause)
			require.True(t, ok)
			var databaseError *DBError
			require.ErrorAs(t, classified, &databaseError)
			require.Equal(t, test.wantKind, databaseError.Kind)
			require.Equal(t, test.wantCode, databaseError.Code)
			require.Equal(t, test.wantDriver, databaseError.DriverCode)
		})
	}
}

func TestParseDMExplainTextOfficialManualFixtures(t *testing.T) {
	// Source: DM SQL Development Guide, "Data Query Statements", 4.19.1
	// https://eco.dameng.com/document/dm/zh-cn/pm/check-phrases.html
	complexPlan := `1   #NSET2: [1, 28, 100]
2     #PRJT2: [1, 28, 100]; exp_num(2), is_atom(FALSE); INFO_BITS(0); spl_info(NULL)
3       #HASH LEFT SEMI JOIN2: [1, 28, 100]; (ANTI), KEY_NUM(1), KEY(SYSOBJECTS.NAME = DMTEMPVIEW_889193466.colname), KEY_NULL_EQU(0)
4         #SLCT2: [1, 28, 100]; SYSOBJECTS.SUBTYPE$ = 'STAB'; slct_pushdown(1); spl_info(NULL); flt_batch_exec(1)
5           #CSCN2: [1, 1124, 100]; SYSINDEXSYSOBJECTS(SYSOBJECTS as SYSOBJECTS); btr_scan(1); need_slct(1); prejudge_iescn(0)
6         #PRJT2: [1, 1, 96]; exp_num(1), is_atom(FALSE); INFO_BITS(0); spl_info(NULL)
7           #INDEX JOIN SEMI JOIN2: [1, 1, 96]; join condition(SYSOBJECTS.SUBTYPE$ = 'STAB'), flt_batch_exec(0)
8             #CSEK2: [1, 28, 96]; scan_type(ASC), SYSINDEXSYSOBJECTS(SYSOBJECTS as SYSOBJECTS), scan_range[('DSYNOM',min,min),('DSYNOM',max,max))
9             #BLKUP2: [1, 3, 96]; SYSINDEXNAMESYSOBJECTS(SYSOBJECTS); use_clu_addr(0)
10              #SSEK2: [1, 3, 96]; scan_type(ASC), SYSINDEXNAMESYSOBJECTS(SYSOBJECTS as SYSOBJECTS), scan_range[SYSOBJECTS.NAME,SYSOBJECTS.NAME], is_global(0)

Predicate Information (identified by operation id):
---------------------------------------------------
3 - access(SYSOBJECTS.NAME = DMTEMPVIEW_889193466.colname)
4 - filter(SYSOBJECTS.SUBTYPE$ = 'STAB')
7 - filter(SYSOBJECTS.SUBTYPE$ = 'STAB')`

	info, err := parseDMExplainText(complexPlan)
	require.NoError(t, err)
	require.Equal(t, int64(28), info.EstScanRows)
	require.Equal(t, float64(1), info.EstCost)
	require.True(t, info.UsesIndex)
	require.True(t, info.SeqScan)
	require.Contains(t, info.Raw, "10|6|SSEK2|3|1")
	require.NotContains(t, info.Raw, "SYSOBJECTS")
	require.NotContains(t, info.Raw, "STAB")

	// Same source, 4.22 example with an explicitly selected secondary index.
	indexPlan := `1   #NSET2: [1, 1, 108]
2     #PRJT2: [1, 1, 108]; exp_num(2), is_atom(FALSE); INFO_BITS(0); spl_info(NULL)
3       #SLCT2: [1, 1, 108]; ADDRESS.ADDRESS2 = '洪山区保利花园50号'; slct_pushdown(0); spl_info(NULL); flt_batch_exec(1)
4         #BLKUP2: [1, 16, 108]; INDEX1(ADDRESS); use_clu_addr(0)
5           #SSCN: [1, 16, 108]; INDEX1(ADDRESS); btr_scan(1); is_global(0)`
	info, err = parseDMExplainText(indexPlan)
	require.NoError(t, err)
	require.Equal(t, int64(1), info.EstScanRows)
	require.True(t, info.UsesIndex)
	require.False(t, info.SeqScan)
	require.NotContains(t, info.Raw, "INDEX1")
	require.NotContains(t, info.Raw, "洪山区")
}

func TestParseDMExplainForOfficialManualFixture(t *testing.T) {
	// Source: DM SQL Development Guide, "Data Query Statements", 4.19.2,
	// EXPLAIN AS A1 FOR SELECT ... (all values below are copied from the sample).
	result := model.QueryResult{
		Columns: append([]string{}, dmExplainColumns...),
		Rows: [][]string{
			{"6", "A1", "2022-12-14 13:55:55.000000", "0", "NSET2", "", "", "", "", "65", "100", "1", "0", "0", "", "", "", "0", "0"},
			{"6", "A1", "2022-12-14 13:55:55.000000", "1", "PRJT2", "", "", "", "", "65", "100", "1", "0", "0", "", "", "", "0", "0"},
			{"6", "A1", "2022-12-14 13:55:55.000000", "2", "SLCT2", "", "", "", "", "65", "100", "1", "0", "0", "SYSOBJECTS.SUBTYPE$ = 'STAB'", "", "", "0", "0"},
			{"6", "A1", "2022-12-14 13:55:55.000000", "3", "CSCN2", "SYSOBJECTS", "SYSINDEXSYSOBJECTS", "", "", "1103", "100", "1", "0", "0", "", "", "", "0", "0"},
		},
		RowCount: 4,
	}

	info, err := parseDMExplainFor(result)
	require.NoError(t, err)
	require.Equal(t, int64(65), info.EstScanRows)
	require.Equal(t, float64(1), info.EstCost)
	require.False(t, info.UsesIndex)
	require.True(t, info.SeqScan)
	require.Equal(t, "1|0|NSET2|65|1\n2|1|PRJT2|65|1\n3|2|SLCT2|65|1\n4|3|CSCN2|1103|1", info.Raw)
	require.NotContains(t, info.Raw, "SYSOBJECTS")
}

func TestParseDMExplainFailsClosed(t *testing.T) {
	valid := "1   #NSET2: [1, 1, 4]\n2     #PRJT2: [1, 1, 4]\n3       #CSCN2: [1, 1, 4]"
	textCases := map[string]string{
		"unknown operator": strings.Replace(valid, "PRJT2", "MYSTERY2", 1),
		"wrong root":       strings.Replace(valid, "NSET2", "PRJT2", 1),
		"node id gap":      strings.Replace(valid, "3       #", "4       #", 1),
		"level jump":       strings.Replace(valid, "2     #", "2         #", 1),
		"negative rows":    strings.Replace(valid, "[1, 1, 4]", "[1, -1, 4]", 1),
		"invalid tuple":    strings.Replace(valid, "[1, 1, 4]", "[1, 1]", 1),
		"control":          valid + "\t",
		"predicate only":   "Predicate Information (identified by operation id):",
		"oversized":        strings.Repeat("x", dmExplainTextLimit+1),
	}
	for name, fixture := range textCases {
		t.Run("text "+name, func(t *testing.T) {
			_, err := parseDMExplainText(fixture)
			require.Error(t, err)
		})
	}

	base := model.QueryResult{
		Columns: append([]string{}, dmExplainColumns...),
		Rows: [][]string{
			{"6", "A1", "2022-12-14 13:55:55.000000", "0", "NSET2", "", "", "", "", "65", "100", "1", "0", "0", "", "", "", "0", "0"},
			{"6", "A1", "2022-12-14 13:55:55.000000", "1", "CSCN2", "SYSOBJECTS", "SYSINDEXSYSOBJECTS", "", "", "1103", "100", "1", "0", "0", "", "", "", "0", "0"},
		},
		RowCount: 2,
	}
	structuredCases := map[string]model.QueryResult{}
	badColumns := base
	badColumns.Columns = append([]string{}, base.Columns...)
	badColumns.Columns[4] = "NODE"
	structuredCases["columns"] = badColumns
	unknown := base
	unknown.Rows = cloneStringRows(base.Rows)
	unknown.Rows[1][4] = "MYSTERY2"
	structuredCases["unknown operator"] = unknown
	levelGap := base
	levelGap.Rows = cloneStringRows(base.Rows)
	levelGap.Rows[1][3] = "2"
	structuredCases["level gap"] = levelGap
	nonFinite := base
	nonFinite.Rows = cloneStringRows(base.Rows)
	nonFinite.Rows[0][11] = "NaN"
	structuredCases["non-finite cost"] = nonFinite
	truncated := base
	truncated.Truncated = true
	structuredCases["truncated"] = truncated
	for name, fixture := range structuredCases {
		t.Run("structured "+name, func(t *testing.T) {
			_, err := parseDMExplainFor(fixture)
			require.Error(t, err)
		})
	}
}

func cloneStringRows(rows [][]string) [][]string {
	cloned := make([][]string, len(rows))
	for index := range rows {
		cloned[index] = append([]string{}, rows[index]...)
	}
	return cloned
}

func TestParseOraclePlanTable(t *testing.T) {
	result := model.QueryResult{
		Columns: []string{"ID", "PARENT_ID", "OPERATION", "OPTIONS", "CARDINALITY", "COST"},
		Rows: [][]string{
			{"0", "", "SELECT STATEMENT", "", "12", "7"},
			{"1", "0", "TABLE ACCESS", "FULL", "12", "7"},
			{"2", "1", "INDEX", "RANGE SCAN", "3", "1"},
		},
		RowCount: 3,
	}
	info, err := parseOraclePlanTable(result)
	require.NoError(t, err)
	require.Equal(t, int64(12), info.EstScanRows)
	require.Equal(t, float64(7), info.EstCost)
	require.True(t, info.UsesIndex)
	require.True(t, info.SeqScan)
	require.NotContains(t, info.Raw, "OBJECT_NAME")

	result.Rows[0][2] = "UPDATE STATEMENT"
	_, err = parseOraclePlanTable(result)
	require.Error(t, err)
	result.Rows[0][2] = "SELECT STATEMENT"
	result.Rows[2][1] = "999"
	_, err = parseOraclePlanTable(result)
	require.Error(t, err)
}

func TestParseOraclePlanTableOfflineFixtures(t *testing.T) {
	columns := []string{"ID", "PARENT_ID", "OPERATION", "OPTIONS", "CARDINALITY", "COST"}
	tests := []struct {
		name        string
		rows        [][]string
		wantIndex   bool
		wantSeqScan bool
		wantRaw     string
		wantError   bool
	}{
		{
			name: "fast dual",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "2"},
				{"1", "0", "FAST DUAL", "", "1", "2"},
			},
			wantRaw: "0|SELECT STATEMENT||1|2\n1|FAST DUAL||1|2",
		},
		{
			name: "bitmap index",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "4", "3"},
				{"1", "0", "TABLE ACCESS", "BY INDEX ROWID", "4", "3"},
				{"2", "1", "BITMAP INDEX", "RANGE SCAN", "4", "1"},
			},
			wantIndex: true,
		},
		{
			name: "domain index",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "2", "8"},
				{"1", "0", "DOMAIN INDEX", "", "2", "8"},
			},
			wantIndex: true,
		},
		{
			name: "multiple roots",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "", "FAST DUAL", "", "1", "1"},
			},
			wantError: true,
		},
		{
			name: "cycle",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "2", "VIEW", "", "1", "1"},
				{"2", "1", "FAST DUAL", "", "1", "1"},
			},
			wantError: true,
		},
		{
			name: "invalid child cardinality",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "0", "TABLE ACCESS", "FULL", "NaN", "1"},
			},
			wantError: true,
		},
		{
			name: "control character",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "0", "TABLE\nACCESS", "FULL", "1", "1"},
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := model.QueryResult{Columns: columns, Rows: test.rows, RowCount: len(test.rows)}
			info, err := parseOraclePlanTable(result)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantIndex, info.UsesIndex)
			require.Equal(t, test.wantSeqScan, info.SeqScan)
			if test.wantRaw != "" {
				require.Equal(t, test.wantRaw, info.Raw)
			}
		})
	}
}

func TestDMDiscoveryE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_DM_E2E") != "1" {
		t.Skip("set AGENTSQL_DM_E2E=1 to run against DM8")
	}
	adminPassword := os.Getenv("DM_PASSWORD")
	require.NotEmpty(t, adminPassword, "DM_PASSWORD is required")
	adminDatasource := model.Datasource{
		ID: "dm-e2e-admin", DBType: "dm", Host: envOrDefault("DM_HOST", "127.0.0.1"),
		Port: 5236, Database: envOrDefault("DM_SCHEMA", "SYSDBA"),
		Username: envOrDefault("DM_USER", "SYSDBA"), ConnLimit: 1, StmtTimeoutMS: 15_000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adminDSN, err := buildDMDSN(adminDatasource, adminPassword)
	require.NoError(t, err)
	admin, err := sql.Open("dm", adminDSN)
	require.NoError(t, err)
	admin.SetMaxOpenConns(1)
	require.NoError(t, admin.PingContext(ctx))

	suffix := fmt.Sprintf("%X", time.Now().UnixNano())
	ownerName := "AGSQL_D28_O_" + suffix
	readerName := "AGSQL_D28_R_" + suffix
	const ownerPassword = "Owner@2026"
	const readerPassword = "Read@2026"
	ownerCreated := false
	readerCreated := false
	var ownerExecutor *DMExecutor
	var readerExecutor *DMExecutor
	var readerDB *sql.DB
	t.Cleanup(func() {
		if readerExecutor != nil {
			if closeErr := readerExecutor.Close(); closeErr != nil {
				t.Errorf("close DM reader executor: %v", closeErr)
			}
		}
		if ownerExecutor != nil {
			if closeErr := ownerExecutor.Close(); closeErr != nil {
				t.Errorf("close DM owner executor: %v", closeErr)
			}
		}
		if readerDB != nil {
			if closeErr := readerDB.Close(); closeErr != nil {
				t.Errorf("close raw DM reader connection: %v", closeErr)
			}
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if readerCreated {
			if _, dropErr := admin.ExecContext(cleanupCtx, "DROP USER "+readerName+" CASCADE"); dropErr != nil {
				t.Errorf("drop DM E2E reader: %v", dropErr)
			}
		}
		if ownerCreated {
			if _, dropErr := admin.ExecContext(cleanupCtx, "DROP USER "+ownerName+" CASCADE"); dropErr != nil {
				t.Errorf("drop DM E2E owner: %v", dropErr)
			}
		}
		var remaining int
		if queryErr := admin.QueryRowContext(
			cleanupCtx,
			"SELECT COUNT(*) FROM SYS.DBA_USERS WHERE USERNAME IN (?,?)",
			ownerName,
			readerName,
		).Scan(&remaining); queryErr != nil {
			t.Errorf("verify DM E2E cleanup: %v", queryErr)
		} else if remaining != 0 {
			t.Errorf("DM E2E cleanup left %d test users", remaining)
		} else {
			t.Logf("cleanup verified: users %s and %s are absent", ownerName, readerName)
		}
		if closeErr := admin.Close(); closeErr != nil {
			t.Errorf("close DM admin connection: %v", closeErr)
		}
	})

	mustExec := func(label, statement string) {
		t.Helper()
		_, execErr := admin.ExecContext(ctx, statement)
		require.NoError(t, execErr, label)
		t.Log(label + ": OK")
	}
	mustExec("create owner", "CREATE USER "+ownerName+` IDENTIFIED BY "`+ownerPassword+`"`)
	ownerCreated = true
	mustExec("create reader", "CREATE USER "+readerName+` IDENTIFIED BY "`+readerPassword+`"`)
	readerCreated = true
	mustExec("grant owner session", "GRANT CREATE SESSION TO "+ownerName)
	mustExec("grant reader session", "GRANT CREATE SESSION TO "+readerName)
	mustExec("create typed table", "CREATE TABLE "+ownerName+`.DM28_DATA (
ID INT NOT NULL, BIG_VALUE BIGINT, CODE VARCHAR(32), NOTE VARCHAR(100),
AMOUNT DECIMAL(12,2), CREATED_AT TIMESTAMP(6), EVENT_DATE DATE,
RAW_VALUE VARBINARY(16), TEXT_VALUE CLOB, BLOB_VALUE BLOB,
CONSTRAINT DM28_DATA_PK PRIMARY KEY(ID))`)
	mustExec(
		"create lookup index",
		"CREATE UNIQUE INDEX "+ownerName+".DM28_DATA_CODE_UK ON "+ownerName+".DM28_DATA(CODE)",
	)
	mustExec(
		"seed 2505 rows",
		"INSERT INTO "+ownerName+`.DM28_DATA
(ID,BIG_VALUE,CODE,NOTE,AMOUNT,CREATED_AT,EVENT_DATE,RAW_VALUE,TEXT_VALUE)
SELECT LEVEL,CAST(LEVEL AS BIGINT)*10000000000,'K'||LPAD(LEVEL,4,'0'),
CASE WHEN LEVEL=1 THEN 'O''Reilly @ 上海' ELSE 'row-'||LEVEL END,
LEVEL/100.0,TIMESTAMP '2026-10-03 12:34:56.123456',DATE '2026-10-03',
HEXTORAW('0A0B'),'clob-'||LEVEL FROM DUAL CONNECT BY LEVEL<=2505`,
	)
	mustExec("grant table select", "GRANT SELECT ON "+ownerName+".DM28_DATA TO "+readerName)

	ownerDatasource := adminDatasource
	ownerDatasource.ID = "dm-e2e-owner"
	ownerDatasource.Database = ownerName
	ownerDatasource.Username = ownerName
	ownerExecutor, err = NewDMExecutor(ctx, ownerDatasource, ownerPassword, true)
	require.NoError(t, err)
	require.NoError(t, ownerExecutor.Ping(ctx))
	ownerResult, err := ownerExecutor.Query(ctx, "SELECT USER AS CURRENT_USER", 1)
	require.NoError(t, err)
	require.Equal(t, [][]string{{ownerName}}, ownerResult.Rows)
	t.Logf("owner connection: user=%s schema=%s", ownerName, ownerExecutor.username)

	readerDatasource := adminDatasource
	readerDatasource.ID = "dm-e2e-reader"
	readerDatasource.Database = ownerName
	readerDatasource.Username = readerName
	_, err = NewDMExecutor(ctx, readerDatasource, "Wrong@Password", true)
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeAuthentication, databaseError.Code)
	require.Equal(t, "-2501", databaseError.DriverCode)
	t.Logf("authentication error normalized: code=%s driver=%s", databaseError.Code, databaseError.DriverCode)

	readerExecutor, err = NewDMExecutor(ctx, readerDatasource, readerPassword, true)
	require.NoError(t, err)
	require.NoError(t, readerExecutor.Ping(ctx))
	require.Equal(t, ownerName, readerExecutor.username, "metadata owner must follow DSN schema, not login user")
	t.Logf("reader connection: user=%s active_schema=%s", readerName, readerExecutor.username)

	readerDSN, err := buildDMDSN(readerDatasource, readerPassword)
	require.NoError(t, err)
	readerDB, err = sql.Open("dm", readerDSN)
	require.NoError(t, err)
	readerDB.SetMaxOpenConns(1)
	require.NoError(t, readerDB.PingContext(ctx))
	var loginUser, activeSchema string
	require.NoError(t, readerDB.QueryRowContext(
		ctx,
		"SELECT USER,SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)",
	).Scan(&loginUser, &activeSchema))
	require.Equal(t, readerName, loginUser)
	require.Equal(t, ownerName, activeSchema)

	var sysPrivileges, tablePrivileges string
	require.NoError(t, admin.QueryRowContext(
		ctx,
		"SELECT LISTAGG(PRIVILEGE,',') WITHIN GROUP (ORDER BY PRIVILEGE) FROM SYS.DBA_SYS_PRIVS WHERE GRANTEE=?",
		readerName,
	).Scan(&sysPrivileges))
	require.NoError(t, admin.QueryRowContext(
		ctx,
		"SELECT LISTAGG(PRIVILEGE,',') WITHIN GROUP (ORDER BY PRIVILEGE) FROM SYS.DBA_TAB_PRIVS WHERE GRANTEE=?",
		readerName,
	).Scan(&tablePrivileges))
	require.Equal(t, "CREATE SESSION", sysPrivileges)
	require.Equal(t, "SELECT", tablePrivileges)
	t.Logf("reader explicit privileges: system=%s table=%s", sysPrivileges, tablePrivileges)

	catalogResult, err := readerExecutor.Query(
		ctx,
		"SELECT OWNER,TABLE_NAME FROM SYS.ALL_TABLES WHERE OWNER='"+ownerName+"' AND TABLE_NAME='DM28_DATA'",
		5,
	)
	require.NoError(t, err)
	require.Equal(t, [][]string{{ownerName, "DM28_DATA"}}, catalogResult.Rows)
	t.Logf("catalog discovery: %v", catalogResult.Rows)

	columns, err := ListSchema(ctx, readerExecutor, ownerName, []SchemaTable{{Table: "DM28_DATA"}})
	require.NoError(t, err)
	require.Len(t, columns, 10)
	wantTypes := []string{"INT", "BIGINT", "VARCHAR", "VARCHAR", "DECIMAL", "TIMESTAMP", "DATE", "VARBINARY", "CLOB", "BLOB"}
	for index, column := range columns {
		require.Equal(t, ownerName, column.Schema)
		require.Equal(t, "DM28_DATA", column.Table)
		require.Equal(t, index+1, column.Ordinal)
		require.Equal(t, wantTypes[index], column.DataType)
	}
	t.Logf("column discovery/types: %+v", columns)

	result, err := readerExecutor.Query(
		ctx,
		"SELECT ID,CODE,NOTE,AMOUNT FROM DM28_DATA WHERE NOTE='O''Reilly @ 上海'",
		10,
	)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1", "K0001", "O'Reilly @ 上海", ".01"}}, result.Rows)
	t.Logf("controlled quoted/unicode query: %v", result.Rows)

	sample, err := Sample(
		ctx,
		readerExecutor,
		SchemaTable{Schema: ownerName, Table: "DM28_DATA"},
		[]SampleColumn{
			{Schema: ownerName, Table: "DM28_DATA", Column: "ID"},
			{Schema: ownerName, Table: "DM28_DATA", Column: "CODE"},
		},
		2,
	)
	require.NoError(t, err)
	require.Equal(t, []string{"ID", "CODE"}, sample.Columns)
	require.Equal(t, 2, sample.RowCount)
	t.Logf("typed sample via DM LIMIT: rows=%v", sample.Rows)

	pageZero, err := readerExecutor.Query(ctx, "SELECT ID FROM DM28_DATA ORDER BY ID LIMIT 3 OFFSET 0", 3)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1"}, {"2"}, {"3"}}, pageZero.Rows)
	lastPage, err := readerExecutor.Query(ctx, "SELECT ID FROM DM28_DATA ORDER BY ID LIMIT 3 OFFSET 2503", 3)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"2504"}, {"2505"}}, lastPage.Rows)
	overPage, err := readerExecutor.Query(ctx, "SELECT ID FROM DM28_DATA ORDER BY ID LIMIT 3 OFFSET 2147483647", 3)
	require.NoError(t, err)
	require.Empty(t, overPage.Rows)
	t.Logf("pagination: offset0=%v last=%v huge_offset_rows=%d", pageZero.Rows, lastPage.Rows, overPage.RowCount)

	largeResult, err := readerExecutor.Query(ctx, "SELECT ID,CODE FROM DM28_DATA ORDER BY ID", 2_000)
	require.NoError(t, err)
	require.Equal(t, 2_000, largeResult.RowCount)
	require.True(t, largeResult.Truncated)
	t.Logf("large result bounded: rows=%d truncated=%t", largeResult.RowCount, largeResult.Truncated)

	indexPlan, err := readerExecutor.Explain(ctx, "SELECT ID,CODE FROM DM28_DATA WHERE CODE='K0001'")
	require.NoError(t, err)
	require.True(t, indexPlan.UsesIndex)
	require.False(t, indexPlan.SeqScan)
	require.Positive(t, indexPlan.EstScanRows)
	require.Positive(t, indexPlan.EstCost)
	require.NotContains(t, indexPlan.Raw, "DM28_DATA")
	require.NotContains(t, indexPlan.Raw, "K0001")
	t.Logf("normalized index EXPLAIN: rows=%d cost=%g raw=%q", indexPlan.EstScanRows, indexPlan.EstCost, indexPlan.Raw)
	seqPlan, err := readerExecutor.Explain(ctx, "SELECT ID FROM DM28_DATA WHERE NOTE='row-2'")
	require.NoError(t, err)
	require.True(t, seqPlan.SeqScan)
	t.Logf("normalized sequential EXPLAIN: rows=%d cost=%g raw=%q", seqPlan.EstScanRows, seqPlan.EstCost, seqPlan.Raw)

	_, err = readerExecutor.Query(ctx, "SELECT MISSING_COLUMN FROM DM28_DATA", 1)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeColumnNotFound, databaseError.Code)
	require.Equal(t, "-2111", databaseError.DriverCode)
	t.Logf("missing column normalized: code=%s driver=%s", databaseError.Code, databaseError.DriverCode)
	_, err = readerExecutor.Query(ctx, "SELECT * FROM DM28_MISSING", 1)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeObjectNotFound, databaseError.Code)
	require.Equal(t, "-2106", databaseError.DriverCode)
	_, err = readerExecutor.Query(ctx, "SELECT FROM DM28_DATA", 1)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeSyntax, databaseError.Code)
	require.Equal(t, "-2007", databaseError.DriverCode)
	t.Logf("object/syntax normalized: object=-2106 syntax=-2007")

	_, permissionCause := readerDB.ExecContext(
		ctx,
		"INSERT INTO "+ownerName+".DM28_DATA(ID,CODE) VALUES(9000,'DENIED')",
	)
	require.Error(t, permissionCause)
	classified, ok := classifyDMError(ctx, DBStageExecute, permissionCause)
	require.True(t, ok)
	require.ErrorAs(t, classified, &databaseError)
	require.Equal(t, DBErrorCodePermission, databaseError.Code)
	require.Equal(t, "-5501", databaseError.DriverCode)
	_, permissionCause = readerDB.ExecContext(ctx, "CREATE TABLE "+readerName+".SHOULD_FAIL(ID INT)")
	require.Error(t, permissionCause)
	classified, ok = classifyDMError(ctx, DBStageExecute, permissionCause)
	require.True(t, ok)
	require.ErrorAs(t, classified, &databaseError)
	require.Equal(t, DBErrorCodePermission, databaseError.Code)
	require.Equal(t, "-5515", databaseError.DriverCode)
	t.Logf("least-privilege denials normalized: insert=-5501 create=-5515")

	session, err := readerExecutor.OpenSession(ctx, "dm-e2e-session")
	require.NoError(t, err)
	sessionResult, err := session.Query(ctx, "SELECT ID,CODE FROM DM28_DATA WHERE ID=1", 1)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1", "K0001"}}, sessionResult.Rows)
	require.NoError(t, session.Close())

	_, lineageErr := parser.NewParser(model.DBDialect("dm"))
	require.ErrorIs(t, lineageErr, parser.ErrUnsupportedDialect)
	t.Log("DM lineage parser: fail-closed (unsupported dialect); no lineage PASS claimed")
}

func TestOracleDiscoveryE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_ORACLE_E2E") != "1" {
		t.Skip("set AGENTSQL_ORACLE_E2E=1 to run against Oracle")
	}
	datasource := model.Datasource{
		ID: "oracle-e2e", DBType: "oracle", Host: envOrDefault("ORACLE_HOST", "127.0.0.1"),
		Port: 1521, Database: envOrDefault("ORACLE_SERVICE", "FREEPDB1"),
		Username: envOrDefault("ORACLE_USER", "SYSTEM"), ConnLimit: 1, StmtTimeoutMS: 15_000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := NewOracleExecutor(ctx, datasource, "agentsql-intentionally-wrong", true)
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeAuthentication, databaseError.Code)

	executor, err := NewOracleExecutor(ctx, datasource, os.Getenv("ORACLE_PASSWORD"), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	require.NoError(t, executor.Ping(ctx))
	session, err := executor.OpenSession(ctx, "oracle-e2e-session")
	require.NoError(t, err)
	result, err := session.Query(ctx, "SELECT DUMMY FROM SYS.DUAL", 1)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"X"}}, result.Rows)
	require.NoError(t, session.Close())
	result, err = executor.Query(ctx, "SELECT LEVEL AS N FROM DUAL CONNECT BY LEVEL <= 3", 2)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1"}, {"2"}}, result.Rows)
	require.True(t, result.Truncated)
	_, err = executor.Query(ctx, "SELECT FROM DUAL", 1)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeSyntax, databaseError.Code)
	require.Equal(t, DBStageQuery, databaseError.Stage)
	info, err := executor.Explain(ctx, "SELECT TABLE_NAME FROM ALL_TABLES WHERE OWNER='SYSTEM'")
	require.NoError(t, err)
	require.Positive(t, info.EstScanRows)
	require.Positive(t, info.EstCost)
	require.NotEmpty(t, info.Raw)
	columns, err := ListSchema(ctx, executor, datasource.Database, []SchemaTable{{Schema: "SYS", Table: "DUAL"}})
	require.NoError(t, err)
	require.NotEmpty(t, columns)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
