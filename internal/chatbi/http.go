package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

type Handler struct {
	service             *Service
	mux                 *http.ServeMux
	loginLimit          *rate.Limiter
	appLookupLimit      *rate.Limiter
	slots               chan struct{}
	readQueue           chan struct{}
	version             string
	keySalt             string
	keySince            int64
	initialUserPassword string
}

func NewHandler(service *Service, version string) (http.Handler, error) {
	return NewHandlerWithLimit(service, version, 16)
}

func NewHandlerWithLimit(service *Service, version string, limit int) (http.Handler, error) {
	if service == nil || service.Store == nil || service.Identity == nil || service.Gateway == nil || service.Model == nil {
		return nil, errors.New("ChatBI service assembly is incomplete")
	}
	if limit < 1 || limit > 16 {
		return nil, errors.New("ChatBI concurrent data limit must be between 1 and 16")
	}
	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, errors.New("cannot initialize ChatBI login key metadata")
	}
	h := &Handler{service: service, mux: http.NewServeMux(), loginLimit: rate.NewLimiter(2, 10), appLookupLimit: rate.NewLimiter(20, 100),
		slots: make(chan struct{}, limit), readQueue: make(chan struct{}, 200), version: version,
		keySalt: hex.EncodeToString(salt[:]), keySince: time.Now().UnixMilli(),
		initialUserPassword: os.Getenv("AGENTSQL_CHATBI_INITIAL_USER_PASSWORD")}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": h.version, "service": "chatbi"})
	})
	h.mux.HandleFunc("POST /api/v1/login/access-token", h.login)
	h.mux.HandleFunc("POST /api/v1/login/change-pwd", h.changePasswordAtLogin)
	h.mux.HandleFunc("POST /api/v1/mcp/access_token", h.mcpAccessToken)
	h.mux.HandleFunc("POST /api/v1/mcp/mcp_start", h.mcpStart)
	h.mux.HandleFunc("POST /api/v1/mcp/mcp_ws_list", h.mcpWorkspaceList)
	h.mux.HandleFunc("POST /api/v1/mcp/mcp_ds_list", h.mcpDatasourceList)
	h.mux.HandleFunc("POST /api/v1/mcp/mcp_model_list", h.mcpModelList)
	h.mux.HandleFunc("POST /api/v1/mcp/mcp_question", h.bounded(h.mcpQuestion))
	h.mux.HandleFunc("GET /api/v1/system/config/key", h.publicLoginKey)
	h.mux.HandleFunc("GET /api/v1/system/license", h.sqlbotLicenseStatus)
	h.mux.HandleFunc("GET /api/v1/system/license/version", h.auth(h.sqlbotProtocolVersion))
	h.mux.HandleFunc("GET /api/v1/system/parameter/chat", h.auth(h.sqlbotChatParameters))
	h.mux.HandleFunc("GET /api/v1/system/parameter/login", h.sqlbotLoginParameters)
	h.mux.HandleFunc("GET /api/v1/system/parameter", h.auth(h.sqlbotAllParameters))
	h.mux.HandleFunc("POST /api/v1/system/parameter", h.auth(h.sqlbotSaveParameters))
	h.mux.HandleFunc("GET /api/v1/agentsql/chat-parameters", h.auth(h.nativeChatParameters))
	h.mux.HandleFunc("PUT /api/v1/agentsql/chat-parameters", h.auth(h.nativeSaveChatParameters))
	h.mux.HandleFunc("GET /api/v1/system/aimodel/default", h.auth(h.sqlbotDefaultModel))
	h.mux.HandleFunc("GET /api/v1/system/aimodel", h.auth(h.legacyModelList))
	h.mux.HandleFunc("POST /api/v1/system/aimodel", h.auth(h.legacyModelCreate))
	h.mux.HandleFunc("PUT /api/v1/system/aimodel", h.auth(h.legacyModelUpdate))
	h.mux.HandleFunc("POST /api/v1/system/aimodel/status", h.bounded(h.auth(h.legacyModelStatus)))
	h.mux.HandleFunc("GET /api/v1/system/aimodel/{id}", h.auth(h.legacyModelRead))
	h.mux.HandleFunc("DELETE /api/v1/system/aimodel/{id}", h.auth(h.legacyModelDelete))
	h.mux.HandleFunc("GET /api/v1/system/aimodel/{id}/ws_mapping", h.auth(h.legacyModelMappings))
	h.mux.HandleFunc("PUT /api/v1/system/aimodel/{id}/{action}", h.auth(h.legacyModelPutTwoSegments))
	h.mux.HandleFunc("POST /api/v1/system/aimodel/{id}/ws_mapping", h.auth(h.legacyModelMappingsChange))
	h.mux.HandleFunc("DELETE /api/v1/system/aimodel/{id}/ws_mapping", h.auth(h.legacyModelMappingsChange))
	h.mux.HandleFunc("GET /api/v1/system/apikey", h.auth(h.accountAPIKeyList))
	h.mux.HandleFunc("POST /api/v1/system/apikey", h.auth(h.accountAPIKeyCreate))
	h.mux.HandleFunc("PUT /api/v1/system/apikey/status", h.auth(h.accountAPIKeyStatus))
	h.mux.HandleFunc("DELETE /api/v1/system/apikey/{id}", h.auth(h.accountAPIKeyDelete))
	h.mux.HandleFunc("GET /api/v1/agentsql/model", h.auth(h.nativeWorkspaceModel))
	h.mux.HandleFunc("POST /api/v1/agentsql/model", h.auth(h.nativeCreateWorkspaceModel))
	h.mux.HandleFunc("PUT /api/v1/agentsql/model", h.auth(h.nativeUpdateWorkspaceModel))
	h.mux.HandleFunc("POST /api/v1/agentsql/model/check", h.bounded(h.auth(h.nativeCheckWorkspaceModel)))
	h.mux.HandleFunc("GET /api/v1/agentsql/models", h.auth(h.nativeWorkspaceModelList))
	h.mux.HandleFunc("POST /api/v1/agentsql/models", h.auth(h.nativeCreateAdditionalModel))
	h.mux.HandleFunc("GET /api/v1/agentsql/models/{id}", h.auth(h.nativeAdditionalModelRead))
	h.mux.HandleFunc("PUT /api/v1/agentsql/models/{id}", h.auth(h.nativeAdditionalModelUpdate))
	h.mux.HandleFunc("DELETE /api/v1/agentsql/models/{id}", h.auth(h.nativeAdditionalModelDelete))
	h.mux.HandleFunc("PUT /api/v1/agentsql/models/{id}/default", h.auth(h.nativeAdditionalModelSetDefault))
	h.mux.HandleFunc("POST /api/v1/agentsql/models/{id}/check", h.bounded(h.auth(h.nativeAdditionalModelCheck)))
	h.mux.HandleFunc("POST /api/v1/agentsql/login", h.nativeLogin)
	h.mux.HandleFunc("POST /api/v1/login/logout", h.auth(h.logout))
	h.mux.HandleFunc("GET /api/v1/user/info", h.auth(h.userInfo))
	h.mux.HandleFunc("GET /api/v1/user/defaultPwd", h.auth(h.sqlbotDefaultPassword))
	h.mux.HandleFunc("PUT /api/v1/user/pwd", h.auth(h.changeOwnPassword))
	h.mux.HandleFunc("PATCH /api/v1/user/pwd/{id}", h.auth(h.legacyUserResetPassword))
	h.mux.HandleFunc("PUT /api/v1/user/language", h.auth(h.changeOwnLanguage))
	h.mux.HandleFunc("GET /api/v1/user/ws", h.auth(h.userWorkspaces))
	h.mux.HandleFunc("PUT /api/v1/user/ws/{oid}", h.auth(h.switchOwnWorkspace))
	h.mux.HandleFunc("GET /api/v1/user/pager/{page}/{size}", h.auth(h.legacyUserPage))
	h.mux.HandleFunc("GET /api/v1/user/template", h.bounded(h.auth(h.legacyUserImportTemplate)))
	h.mux.HandleFunc("POST /api/v1/user/batchImport", h.bounded(h.auth(h.legacyUserBatchImport)))
	h.mux.HandleFunc("GET /api/v1/user/errorRecord/{id}", h.bounded(h.auth(h.legacyUserImportErrors)))
	h.mux.HandleFunc("POST /api/v1/user", h.auth(h.legacyUserCreate))
	h.mux.HandleFunc("PUT /api/v1/user", h.auth(h.legacyUserUpdate))
	h.mux.HandleFunc("DELETE /api/v1/user", h.auth(h.legacyUserDeleteBatch))
	h.mux.HandleFunc("DELETE /api/v1/user/{id}", h.auth(h.legacyUserDeleteOne))
	h.mux.HandleFunc("GET /api/v1/user/{id}", h.auth(h.legacyUserRead))
	h.mux.HandleFunc("PATCH /api/v1/user/status", h.auth(h.legacyUserStatus))
	h.mux.HandleFunc("GET /api/v1/system/workspace", h.auth(h.legacyWorkspaceList))
	h.mux.HandleFunc("POST /api/v1/system/workspace", h.auth(h.legacyWorkspaceCreate))
	h.mux.HandleFunc("PUT /api/v1/system/workspace", h.auth(h.legacyWorkspaceUpdate))
	h.mux.HandleFunc("DELETE /api/v1/system/workspace/{id}", h.auth(h.legacyWorkspaceDelete))
	h.mux.HandleFunc("GET /api/v1/system/workspace/{id}", h.auth(h.legacyWorkspaceRead))
	h.mux.HandleFunc("GET /api/v1/system/workspace/uws/pager/{page}/{size}", h.auth(h.legacyMemberPage))
	h.mux.HandleFunc("GET /api/v1/system/workspace/uws/option/pager/{page}/{size}", h.auth(h.legacyMemberCandidatePage))
	h.mux.HandleFunc("GET /api/v1/system/workspace/uws/option", h.auth(h.legacyMemberCandidate))
	h.mux.HandleFunc("POST /api/v1/system/workspace/uws", h.auth(h.legacyMemberGrant))
	h.mux.HandleFunc("PUT /api/v1/system/workspace/uws", h.auth(h.legacyMemberWeight))
	h.mux.HandleFunc("DELETE /api/v1/system/workspace/uws", h.auth(h.legacyMemberRevoke))
	h.mux.HandleFunc("POST /api/v1/sys_variable/listAll", h.auth(h.systemVariableList))
	h.mux.HandleFunc("POST /api/v1/sys_variable/listPage/{page}/{size}", h.auth(h.systemVariablePage))
	h.mux.HandleFunc("POST /api/v1/sys_variable/save", h.auth(h.systemVariableSave))
	h.mux.HandleFunc("POST /api/v1/sys_variable/delete", h.auth(h.systemVariableDelete))
	h.mux.HandleFunc("GET /api/v1/datasource/list", h.auth(h.datasources))
	h.mux.HandleFunc("GET /api/v1/datasource/ws/{oid}", h.auth(h.assistantWorkspaceSources))
	h.mux.HandleFunc("GET /api/v1/system/aimodel/list/by_ws", h.auth(h.assistantWorkspaceModels))
	h.mux.HandleFunc("POST /api/v1/datasource/add", h.bounded(h.auth(h.addExcelDatasource)))
	h.mux.HandleFunc("POST /api/v1/datasource/update", h.bounded(h.auth(h.updateDatasourceMetadata)))
	h.mux.HandleFunc("POST /api/v1/datasource/get/{id}", h.bounded(h.auth(h.getDatasource)))
	h.mux.HandleFunc("POST /api/v1/datasource/delete/{id}/{name}", h.bounded(h.auth(h.deleteDatasource)))
	h.mux.HandleFunc("GET /api/v1/datasource/check/{id}", h.bounded(h.auth(h.datasourceCheck)))
	h.mux.HandleFunc("POST /api/v1/datasource/check", h.bounded(h.auth(h.checkBoundDatasource)))
	h.mux.HandleFunc("POST /api/v1/datasource/getSchemaByConf", h.bounded(h.auth(h.schemasByBoundDatasource)))
	h.mux.HandleFunc("POST /api/v1/table_relation/get/{id}", h.bounded(h.auth(h.getTableRelations)))
	h.mux.HandleFunc("POST /api/v1/table_relation/save/{id}", h.bounded(h.auth(h.saveTableRelations)))
	h.mux.HandleFunc("POST /api/v1/datasource/getTables/{id}", h.bounded(h.auth(h.datasourceTables)))
	h.mux.HandleFunc("POST /api/v1/datasource/getTablesByConf", h.bounded(h.auth(h.databaseTablesBySavedBinding)))
	h.mux.HandleFunc("POST /api/v1/datasource/getFields/{id}/{table}", h.bounded(h.auth(h.datasourceFields)))
	h.mux.HandleFunc("POST /api/v1/datasource/tableList/{id}", h.bounded(h.auth(h.datasourceTableList)))
	h.mux.HandleFunc("POST /api/v1/datasource/editTable", h.bounded(h.auth(h.editDatasourceTableComment)))
	h.mux.HandleFunc("POST /api/v1/datasource/chooseTables/{id}", h.bounded(h.auth(h.chooseManagedFileTables)))
	h.mux.HandleFunc("GET /api/v1/agentsql/datasource/fileTables/{id}", h.bounded(h.auth(h.managedFileTableChoices)))
	h.mux.HandleFunc("GET /api/v1/agentsql/datasource/databaseTables/{id}", h.bounded(h.auth(h.databaseTableChoices)))
	h.mux.HandleFunc("POST /api/v1/datasource/fieldList/{id}/{tableID}", h.bounded(h.auth(h.datasourceFieldList)))
	h.mux.HandleFunc("POST /api/v1/datasource/syncFields/{id}/{tableID}", h.bounded(h.auth(h.syncDatasourceFields)))
	h.mux.HandleFunc("POST /api/v1/datasource/editField", h.bounded(h.auth(h.editDatasourceFieldComment)))
	h.mux.HandleFunc("GET /api/v1/datasource/exportDsSchema/{id}", h.bounded(h.auth(h.exportManagedFileRemarks)))
	h.mux.HandleFunc("POST /api/v1/datasource/uploadDsSchema/{id}", h.bounded(h.auth(h.importManagedFileRemarks)))
	h.mux.HandleFunc("POST /api/v1/datasource/previewData/{id}", h.bounded(h.auth(h.datasourcePreview)))
	h.mux.HandleFunc("POST /api/v1/datasource/parseExcel", h.bounded(h.auth(h.parseExcel)))
	h.mux.HandleFunc("POST /api/v1/datasource/importToDb", h.bounded(h.auth(h.importToDb)))
	h.mux.HandleFunc("POST /api/v1/agentsql/datasource/claim-import", h.bounded(h.auth(h.claimNativeFileDatasource)))
	h.mux.HandleFunc("GET /api/v1/recommended_problem/get_datasource_recommended/{id}", h.auth(h.recommendedProblems))
	h.mux.HandleFunc("GET /api/v1/recommended_problem/get_datasource_recommended_base/{id}", h.auth(h.recommendedBase))
	h.mux.HandleFunc("POST /api/v1/recommended_problem/save_recommended_problem", h.auth(h.legacySaveRecommendations))
	h.mux.HandleFunc("GET /api/v1/agentsql/recommendations/{id}", h.auth(h.nativeRecommendationRead))
	h.mux.HandleFunc("PUT /api/v1/agentsql/recommendations/{id}", h.auth(h.nativeRecommendationSave))
	h.mux.HandleFunc("GET /api/v1/system/terminology/page/{page}/{size}", h.auth(h.terminologyPage))
	h.mux.HandleFunc("GET /api/v1/system/terminology/template", h.auth(h.terminologyTemplate))
	h.mux.HandleFunc("GET /api/v1/system/terminology/export", h.auth(h.terminologyExport))
	h.mux.HandleFunc("POST /api/v1/system/terminology/uploadExcel", h.bounded(h.auth(h.terminologyUpload)))
	h.mux.HandleFunc("PUT /api/v1/system/terminology", h.auth(h.saveTerminology))
	h.mux.HandleFunc("DELETE /api/v1/system/terminology", h.auth(h.deleteTerminologies))
	h.mux.HandleFunc("GET /api/v1/system/data-training/page/{page}/{size}", h.auth(h.examplePage))
	h.mux.HandleFunc("GET /api/v1/agentsql/terms", h.auth(h.nativeTerminologies))
	h.mux.HandleFunc("PUT /api/v1/agentsql/terms", h.auth(h.nativeSaveTerminology))
	h.mux.HandleFunc("DELETE /api/v1/agentsql/terms/{id}", h.auth(h.nativeDeleteTerminology))
	h.mux.HandleFunc("POST /api/v1/agentsql/terms/{id}/enabled", h.auth(h.nativeEnableTerminology))
	h.mux.HandleFunc("GET /api/v1/agentsql/examples", h.auth(h.nativeExamples))
	h.mux.HandleFunc("PUT /api/v1/agentsql/examples", h.auth(h.nativeSaveExample))
	h.mux.HandleFunc("DELETE /api/v1/agentsql/examples/{id}", h.auth(h.nativeDeleteExample))
	h.mux.HandleFunc("POST /api/v1/agentsql/examples/{id}/enabled", h.auth(h.nativeEnableExample))
	h.mux.HandleFunc("GET /api/v1/system/data-training/template", h.auth(h.exampleTemplate))
	h.mux.HandleFunc("GET /api/v1/system/data-training/export", h.auth(h.exampleExport))
	h.mux.HandleFunc("POST /api/v1/system/data-training/uploadExcel", h.bounded(h.auth(h.exampleUpload)))
	h.mux.HandleFunc("POST /api/v1/system/download-fail-info", h.bounded(h.auth(h.downloadSemanticError)))
	h.mux.HandleFunc("PUT /api/v1/system/data-training", h.auth(h.saveExample))
	h.mux.HandleFunc("DELETE /api/v1/system/data-training", h.auth(h.deleteExamples))
	h.mux.HandleFunc("GET /api/v1/chat/list", h.auth(h.chats))
	h.mux.HandleFunc("POST /api/v1/chat/start", h.bounded(h.auth(h.start)))
	h.mux.HandleFunc("POST /api/v1/chat/assistant/start", h.bounded(h.auth(h.start)))
	h.mux.HandleFunc("POST /api/v1/chat/question", h.bounded(h.auth(h.question)))
	h.mux.HandleFunc("POST /api/v1/chat/rename", h.auth(h.rename))
	h.mux.HandleFunc("GET /api/v1/chat/record/{id}/data", h.boundedHistory(h.recordData))
	h.mux.HandleFunc("GET /api/v1/chat/record/{id}/data_live", h.boundedHistory(h.recordData))
	h.mux.HandleFunc("GET /api/v1/agentsql/record/{id}/export.csv", h.boundedHistory(h.exportCSV))
	h.mux.HandleFunc("GET /api/v1/chat/record/{id}/excel/export/{chatID}", h.boundedHistory(h.exportSQLBotWorkbook))
	h.mux.HandleFunc("GET /api/v1/chat/record/{id}/usage", h.auth(h.recordUsage))
	h.mux.HandleFunc("GET /api/v1/chat/record/{id}/log", h.auth(h.recordUsage))
	h.mux.HandleFunc("GET /api/v1/agentsql/record/{id}/analysis", h.boundedHistory(h.nativeNarrative))
	h.mux.HandleFunc("GET /api/v1/agentsql/record/{id}/predict", h.boundedHistory(h.nativeNarrative))
	h.mux.HandleFunc("POST /api/v1/chat/record/{id}/analysis", h.boundedHistory(h.legacyNarrative))
	h.mux.HandleFunc("POST /api/v1/chat/record/{id}/predict", h.boundedHistory(h.legacyNarrative))
	h.mux.HandleFunc("GET /api/v1/agentsql/record/{id}/forecast", h.boundedHistory(h.nativeForecast))
	h.mux.HandleFunc("GET /api/v1/chat/record/{id}/predict_data", h.boundedHistory(h.legacyForecastData))
	h.mux.HandleFunc("POST /api/v1/chat/recommend_questions/{id}", h.auth(h.recommendQuestions))
	h.mux.HandleFunc("GET /api/v1/chat/{id}", h.boundedHistory(h.read))
	h.mux.HandleFunc("GET /api/v1/chat/{id}/with_data", h.boundedHistory(h.read))
	h.mux.HandleFunc("DELETE /api/v1/chat/{id}", h.auth(h.remove))
	h.mux.HandleFunc("GET /api/v1/agentsql/dashboards", h.auth(h.dashboardList))
	h.mux.HandleFunc("POST /api/v1/agentsql/dashboards", h.auth(h.dashboardSave))
	h.mux.HandleFunc("GET /api/v1/agentsql/dashboards/{id}", h.boundedHistory(h.dashboardRead))
	h.mux.HandleFunc("PUT /api/v1/agentsql/dashboards/{id}", h.auth(h.dashboardSave))
	h.mux.HandleFunc("DELETE /api/v1/agentsql/dashboards/{id}", h.auth(h.dashboardDelete))
	h.mux.HandleFunc("POST /api/v1/dashboard/list_resource", h.auth(h.legacyDashboardList))
	h.mux.HandleFunc("POST /api/v1/dashboard/check_name", h.auth(h.legacyDashboardName))
	h.mux.HandleFunc("POST /api/v1/dashboard/create_resource", h.auth(h.legacyDashboardCreateFolder))
	h.mux.HandleFunc("POST /api/v1/dashboard/update_resource", h.auth(h.legacyDashboardUpdateResource))
	h.mux.HandleFunc("POST /api/v1/dashboard/create_canvas", h.auth(h.legacyDashboardCreateCanvas))
	h.mux.HandleFunc("POST /api/v1/dashboard/load_resource", h.boundedHistory(h.legacyDashboardLoad))
	h.mux.HandleFunc("POST /api/v1/dashboard/update_canvas", h.auth(h.legacyDashboardUpdateCanvas))
	h.mux.HandleFunc("DELETE /api/v1/dashboard/delete_resource/{id}/{name}", h.auth(h.legacyDashboardDelete))
	h.mux.HandleFunc("GET /api/v1/agentsql/assistants", h.auth(h.assistantList))
	h.mux.HandleFunc("POST /api/v1/agentsql/assistants", h.auth(h.assistantSave))
	h.mux.HandleFunc("GET /api/v1/agentsql/assistants/{id}", h.auth(h.assistantRead))
	h.mux.HandleFunc("PUT /api/v1/agentsql/assistants/{id}", h.auth(h.assistantSave))
	h.mux.HandleFunc("DELETE /api/v1/agentsql/assistants/{id}", h.auth(h.assistantDelete))
	h.mux.HandleFunc("GET /api/v1/system/assistant", h.auth(h.legacyAssistantList))
	h.mux.HandleFunc("GET /api/v1/system/assistant/advanced_application", h.auth(h.legacyAssistantList))
	h.mux.HandleFunc("GET /api/v1/system/assistant/app/{appID}", h.publicEmbeddedApplication)
	h.mux.HandleFunc("OPTIONS /api/v1/system/assistant/app/{appID}", h.publicEmbeddedApplication)
	h.mux.HandleFunc("GET /api/v1/system/assistant/validate/{id}", h.publicAssistantValidate)
	h.mux.HandleFunc("OPTIONS /api/v1/system/assistant/validate/{id}", h.publicAssistantValidate)
	h.mux.HandleFunc("POST /api/v1/system/assistant", h.auth(h.legacyAssistantSave))
	h.mux.HandleFunc("PUT /api/v1/system/assistant", h.auth(h.legacyAssistantSave))
	h.mux.HandleFunc("PATCH /api/v1/system/assistant/ui", h.bounded(h.auth(h.legacyEmbeddedUI)))
	h.mux.HandleFunc("DELETE /api/v1/system/assistant/{id}", h.auth(h.legacyAssistantDelete))
	h.mux.HandleFunc("GET /api/v1/system/embedded/{page}/{size}", h.auth(h.legacyEmbeddedList))
	h.mux.HandleFunc("GET /api/v1/system/embedded/{id}", h.auth(h.legacyEmbeddedRead))
	h.mux.HandleFunc("POST /api/v1/system/embedded", h.auth(h.legacyEmbeddedSave))
	h.mux.HandleFunc("PUT /api/v1/system/embedded", h.auth(h.legacyEmbeddedSave))
	h.mux.HandleFunc("DELETE /api/v1/system/embedded", h.auth(h.legacyEmbeddedDelete))
	h.mux.HandleFunc("PATCH /api/v1/system/embedded/secret/{id}", h.auth(h.legacyEmbeddedRotate))
	assets := http.StripPrefix("/ui/", uiHandler())
	h.mux.HandleFunc("GET /ui/{asset}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("asset") {
		case "app.js", "chart.js", "dashboard.js", "assistants.js", "chat-settings.js", "user-variables.js", "source-relations.js", "app.css", "mark.svg":
			assets.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	h.mux.Handle("GET /{$}", uiHandler())
	return h, nil
}

func (h *Handler) publicLoginKey(w http.ResponseWriter, _ *http.Request) {
	if h.service.Identity.privateKey == nil {
		failure(w, ErrUnavailable)
		return
	}
	der, err := x509.MarshalPKIXPublicKey(&h.service.Identity.privateKey.PublicKey)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	publicKey := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	okay(w, map[string]any{"id": 1, "public_key": publicKey, "salt": h.keySalt,
		"create_time": h.keySince, "update_time": h.keySince})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Enable routes overlap the paged list templates in ServeMux. Parse the
	// literal middle segment before the generic mux gets a chance to dispatch.
	if r.Method == http.MethodGet {
		for _, route := range []struct {
			prefix  string
			handler authorizedHandler
		}{{"/api/v1/system/terminology/", h.enableTerminology}, {"/api/v1/system/data-training/", h.enableExample}} {
			if !strings.HasPrefix(r.URL.Path, route.prefix) {
				continue
			}
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, route.prefix), "/")
			if len(parts) == 3 && parts[1] == "enable" {
				r.SetPathValue("id", parts[0])
				r.SetPathValue("enabled", parts[2])
				h.auth(route.handler)(w, r)
				return
			}
		}
	}
	// This fixed-prefix route overlaps /chat/{id}/with_data in ServeMux's
	// wildcard precedence rules, so dispatch it before the general chat mux.
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/chat/recent_questions/") {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/chat/recent_questions/")
		if !ID(id).Valid() {
			http.NotFound(w, r)
			return
		}
		r.SetPathValue("id", id)
		h.auth(h.recentQuestions)(w, r)
		return
	}
	// No permissive CORS or cookie-based authority. Future embedded origins use
	// explicit application policies; the ordinary Q&A API needs a bearer header.
	h.mux.ServeHTTP(w, r)
}

// Historical reads also perform controlled queries. They share the same
// bounded concurrency and deadline as new questions, including slow clients.
func (h *Handler) bounded(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case h.slots <- struct{}{}:
			defer func() { <-h.slots }()
		default:
			writeJSON(w, 429, "Too many active data requests")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
		defer cancel()
		// ResponseController keeps streaming available while bounding a stalled
		// response writer. A recorder without deadline support is still usable.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(4*time.Minute + 5*time.Second))
		defer func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) }()
		next(w, r.WithContext(ctx))
	}
}

// The original SQLBot page loads several historical result cards at once.
// Admit only authenticated requests to a bounded queue, then use the same
// query slots as new questions. Every queued request has a four-minute total
// deadline and must still have a live session before it reaches the gateway.
func (h *Handler) boundedHistory(next authorizedHandler) http.HandlerFunc {
	return h.auth(func(w http.ResponseWriter, r *http.Request, p Principal, token string) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
		defer cancel()
		select {
		case h.readQueue <- struct{}{}:
			defer func() { <-h.readQueue }()
		default:
			writeJSON(w, http.StatusTooManyRequests, "Too many historical data requests")
			return
		}
		select {
		case h.slots <- struct{}{}:
			defer func() { <-h.slots }()
		case <-ctx.Done():
			writeJSON(w, http.StatusTooManyRequests, "Historical data request timed out")
			return
		}
		if err := h.currentIdentity(ctx, p, token); err != nil {
			failure(w, err)
			return
		}
		deadline, _ := ctx.Deadline()
		_ = http.NewResponseController(w).SetWriteDeadline(deadline.Add(5 * time.Second))
		defer func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) }()
		next(w, r.WithContext(ctx), p, token)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func okay(w http.ResponseWriter, value any) {
	writeJSON(w, 200, map[string]any{"code": 0, "data": value, "msg": nil})
}
func failure(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "service could not complete this request"
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status, message = 401, "Not authenticated"
	case errors.Is(err, ErrInvalidSemantic):
		status, message = 422, "Invalid semantic metadata"
	case errors.Is(err, ErrInvalidDashboard):
		status, message = 422, "Invalid dashboard configuration"
	case errors.Is(err, ErrInvalidAssistant):
		status, message = 422, "Invalid assistant application"
	case errors.Is(err, ErrInvalidAccountChange):
		status, message = 422, "Invalid account change"
	case errors.Is(err, ErrForecastUnavailable):
		status, message = 422, "An ordered numeric or date series is required for forecasting"
	case errors.Is(err, ErrNotFound):
		status, message = 404, "Resource not found"
	case errors.Is(err, ErrWorkspaceNotEmpty):
		status, message = 409, "Workspace still contains resources or members"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrBusy):
		status, message = 409, "Conversation changed or is busy"
	case errors.Is(err, ErrGateway):
		status, message = 403, "Controlled query was not authorized or could not be audited"
	case errors.Is(err, ErrModel):
		status, message = 502, "Model request did not complete"
	}
	writeJSON(w, status, message)
}

func readBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(target) != nil {
		writeJSON(w, 422, "Invalid request body")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, 422, "Invalid request body")
		return false
	}
	return true
}

func requestToken(r *http.Request) (string, error) {
	if len(r.Header.Values("X-SQLBOT-ASSISTANT-TOKEN")) != 0 {
		return "", ErrUnauthenticated
	}
	values := r.Header.Values("X-SQLBOT-TOKEN")
	if len(values) != 1 {
		return "", ErrUnauthenticated
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", ErrUnauthenticated
	}
	return parts[1], nil
}

type authorizedHandler func(http.ResponseWriter, *http.Request, Principal, string)

func (h *Handler) currentIdentity(ctx context.Context, p Principal, token string) error {
	var current Principal
	var err error
	if strings.HasPrefix(token, "Ask ") {
		current, err = h.service.Identity.VerifyAsk(ctx, strings.TrimPrefix(token, "Ask "))
	} else if p.EmbeddedApp != "" {
		if !strings.HasPrefix(token, "Embedded ") {
			return ErrUnauthenticated
		}
		current, err = h.service.Identity.VerifyEmbedded(ctx, strings.TrimPrefix(token, "Embedded "))
	} else {
		current, err = h.service.Identity.Verify(ctx, token)
	}
	if err != nil || current.User.ID != p.User.ID || current.Workspace != p.Workspace || current.EmbeddedApp != p.EmbeddedApp || current.EmbeddedRevision != p.EmbeddedRevision {
		return ErrUnauthenticated
	}
	return nil
}

func embeddedRouteAllowed(r *http.Request) bool {
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/v1/chat/") {
		return r.Method == http.MethodGet || r.Method == http.MethodPost || r.Method == http.MethodDelete
	}
	if r.Method == http.MethodGet {
		switch path {
		case "/api/v1/user/info", "/api/v1/user/ws", "/api/v1/datasource/list", "/api/v1/system/parameter/chat", "/api/v1/system/aimodel/default", "/api/v1/system/license/version":
			return true
		}
		return strings.HasPrefix(path, "/api/v1/datasource/ws/") || strings.HasPrefix(path, "/api/v1/datasource/check/") || strings.HasPrefix(path, "/api/v1/recommended_problem/get_datasource_recommended/") || strings.HasPrefix(path, "/api/v1/recommended_problem/get_datasource_recommended_base/")
	}
	if r.Method == http.MethodPost {
		for _, prefix := range []string{"/api/v1/datasource/get/", "/api/v1/datasource/getTables/", "/api/v1/datasource/getFields/", "/api/v1/datasource/tableList/", "/api/v1/datasource/fieldList/", "/api/v1/datasource/previewData/"} {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
	}
	return false
}

func (h *Handler) auth(next authorizedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if values := r.Header.Values("X-SQLBOT-ASK-TOKEN"); len(values) != 0 {
			if len(values) != 1 || len(r.Header.Values("X-SQLBOT-TOKEN")) != 0 ||
				len(r.Header.Values("X-SQLBOT-ASSISTANT-TOKEN")) != 0 || len(r.Header.Values("Authorization")) != 0 || !askRouteAllowed(r) {
				failure(w, ErrUnauthenticated)
				return
			}
			parts := strings.Fields(values[0])
			if len(parts) != 2 || !strings.EqualFold(parts[0], "sk") {
				failure(w, ErrUnauthenticated)
				return
			}
			p, err := h.service.Identity.VerifyAsk(r.Context(), parts[1])
			if err != nil {
				failure(w, err)
				return
			}
			next(w, r, p, "Ask "+parts[1])
			return
		}
		if values := r.Header.Values("X-SQLBOT-ASSISTANT-TOKEN"); len(values) != 0 {
			if len(values) != 1 || len(r.Header.Values("X-SQLBOT-TOKEN")) != 0 || len(r.Header.Values("Authorization")) != 0 || !embeddedRouteAllowed(r) {
				failure(w, ErrUnauthenticated)
				return
			}
			parts := strings.Fields(values[0])
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Embedded") {
				failure(w, ErrUnauthenticated)
				return
			}
			p, err := h.service.Identity.VerifyEmbedded(r.Context(), parts[1])
			if err != nil {
				failure(w, err)
				return
			}
			if origins := r.Header.Values("X-SQLBOT-HOST-ORIGIN"); len(origins) != 0 {
				if len(origins) != 1 {
					failure(w, ErrUnauthenticated)
					return
				}
				var app AssistantApplication
				if _, err := h.service.Store.get(r.Context(), assistantKey(p.Workspace, p.EmbeddedApp), &app); err != nil || !assistantOriginAllowed(app.Domain, origins[0]) {
					failure(w, ErrUnauthenticated)
					return
				}
			}
			next(w, r, p, "Embedded "+parts[1])
			return
		}
		token, err := requestToken(r)
		if err != nil {
			failure(w, err)
			return
		}
		p, err := h.service.Identity.Verify(r.Context(), token)
		if err != nil {
			failure(w, err)
			return
		}
		next(w, r, p, token)
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.loginLimit.Allow() {
		writeJSON(w, 429, "Too many login requests")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || len(r.PostForm["username"]) != 1 || len(r.PostForm["password"]) != 1 {
		writeJSON(w, 400, "Invalid login")
		return
	}
	account, e1 := h.service.Identity.DecryptLogin(r.PostForm.Get("username"))
	password, e2 := h.service.Identity.DecryptLogin(r.PostForm.Get("password"))
	if e1 != nil || e2 != nil {
		writeJSON(w, 400, "Invalid login")
		return
	}
	h.issueLogin(w, r, account, password)
}

// The native UI uses TLS-protected credentials. The legacy encrypted endpoint
// never falls back to accepting plaintext when an RSA key is unavailable.
func (h *Handler) nativeLogin(w http.ResponseWriter, r *http.Request) {
	if !h.loginLimit.Allow() {
		writeJSON(w, 429, "Too many login requests")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readBody(w, r, &input) {
		return
	}
	h.issueLogin(w, r, input.Username, input.Password)
}

func (h *Handler) issueLogin(w http.ResponseWriter, r *http.Request, account, password string) {
	_, token, err := h.service.Identity.Login(r.Context(), account, password)
	if err != nil {
		writeJSON(w, 400, "Invalid login")
		return
	}
	okay(w, map[string]any{"access_token": token, "token_type": "bearer", "platform_info": nil, "need_change_pwd": false})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, _ Principal, token string) {
	if err := h.service.Identity.Logout(r.Context(), token); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}
func (h *Handler) userInfo(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	weight := 0
	oids := make([]ID, 0, len(p.User.Memberships))
	for _, member := range p.User.Memberships {
		if p.EmbeddedApp == "" || member.Workspace == p.Workspace {
			oids = append(oids, member.Workspace)
		}
		if member.Workspace == p.Workspace {
			weight = member.Weight
		}
	}
	okay(w, map[string]any{"id": p.User.ID, "account": p.User.Account, "name": p.User.Name, "email": p.User.Email, "create_time": p.User.Created, "oid": p.Workspace, "oid_list": oids, "language": p.User.Language, "weight": weight, "isAdmin": p.User.Admin, "status": p.User.Status, "origin": p.User.Origin})
}

func (h *Handler) userWorkspaces(w http.ResponseWriter, _ *http.Request, p Principal, _ string) {
	result := make([]map[string]any, 0, len(p.User.Memberships))
	for _, membership := range p.User.Memberships {
		if p.EmbeddedApp != "" && membership.Workspace != p.Workspace {
			continue
		}
		id, err := strconv.ParseInt(string(membership.Workspace), 10, 64)
		if err != nil || id < 1 || id > maxSQLBotBrowserID {
			failure(w, ErrUnavailable)
			return
		}
		name := membership.Name
		if name == "" {
			name = "Workspace " + string(membership.Workspace)
		}
		result = append(result, map[string]any{"id": id, "name": name})
	}
	okay(w, result)
}
func (h *Handler) datasources(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	items, err := h.service.Store.list(r.Context(), "binding", string(p.Workspace), string(p.User.ID))
	if err != nil {
		failure(w, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		var b Binding
		if json.Unmarshal(raw, &b) != nil || b.Workspace != p.Workspace || b.User != p.User.ID {
			failure(w, ErrUnavailable)
			return
		}
		if err := h.service.embeddedSourceAllowed(r.Context(), p, b.Datasource); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			failure(w, err)
			return
		}
		if b.Type == "excel" {
			item, err := legacyExcelDatasource(b)
			if err != nil {
				failure(w, err)
				return
			}
			result = append(result, item)
			continue
		}
		kind := b.Type
		if kind == "" {
			kind = b.Dialect
		}
		typeName := ""
		switch kind {
		case "pg":
			typeName = "PostgreSQL"
		case "mysql":
			typeName = "MySQL"
		}
		configuration := ""
		if databaseBinding(b) {
			configuration, err = databaseDisplayConfiguration()
			if err != nil {
				failure(w, err)
				return
			}
		}
		result = append(result, map[string]any{"id": b.Datasource, "oid": b.Workspace, "name": b.Name, "type": kind, "type_name": typeName,
			"description": b.Description, "configuration": configuration, "status": "Success"})
	}
	sort.Slice(result, func(i, j int) bool { return result[i]["name"].(string) < result[j]["name"].(string) })
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}

func (h *Handler) recommendedProblems(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	config, err := h.service.Recommendations(r.Context(), p, ID(r.PathValue("id")))
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	datasourceID, err := strconv.ParseInt(string(config.Datasource), 10, 64)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	items := make([]map[string]any, 0, len(config.Questions))
	for _, item := range config.Questions {
		id, err := strconv.ParseInt(string(item.ID), 10, 64)
		if err != nil {
			failure(w, ErrUnavailable)
			return
		}
		items = append(items, map[string]any{"id": id, "datasource_id": datasourceID, "question": item.Question, "remark": item.Remark, "sort": item.Sort, "create_time": nil, "create_by": nil})
	}
	okay(w, items)
}

func (h *Handler) recommendedBase(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	config, err := h.service.Recommendations(r.Context(), p, ID(r.PathValue("id")))
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	items := make([]string, 0, len(config.Questions))
	for _, item := range config.Questions {
		items = append(items, item.Question)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	datasourceID, err := strconv.ParseInt(string(config.Datasource), 10, 64)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	okay(w, map[string]any{"datasource_id": datasourceID, "recommended_config": config.Mode, "questions": string(encoded)})
}
func (h *Handler) chats(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	chats, err := h.service.Chats(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, chats)
}

func (h *Handler) recentQuestions(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	questions, err := h.service.RecentQuestions(r.Context(), p, ID(r.PathValue("id")))
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, questions)
}

func (h *Handler) recordData(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid ID")
		return
	}
	record, err := h.service.Record(r.Context(), p, id, true)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, record.Data)
}
func (h *Handler) start(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		Datasource ID     `json:"datasource"`
		Question   string `json:"question"`
		Origin     int    `json:"origin"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if r.URL.Path == "/api/v1/chat/assistant/start" {
		if p.EmbeddedApp == "" || input.Origin != 2 {
			failure(w, ErrUnauthenticated)
			return
		}
	} else if p.EmbeddedApp != "" || input.Origin != 0 {
		failure(w, ErrUnauthenticated)
		return
	}
	chat, err := h.service.Start(r.Context(), p, input.Datasource, input.Question, input.Origin)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, sqlbotChatView(chat))
}
func (h *Handler) read(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid ID")
		return
	}
	chat, err := h.service.ReadChat(r.Context(), p, id, strings.HasSuffix(r.URL.Path, "/with_data"))
	if err != nil {
		failure(w, err)
		return
	}
	// The unchanged SQLBot browser renders saved analysis and prediction text
	// from this conversation response and does not fetch each child record.
	// Metadata reads intentionally strip that text, so reauthorize and audit
	// only narrative children before including their saved content. A changed
	// result digest leaves the historical text empty.
	if !strings.HasSuffix(r.URL.Path, "/with_data") {
		for i := range chat.Records {
			if chat.Records[i].Action != "analysis" && chat.Records[i].Action != "predict" {
				continue
			}
			fresh, err := h.service.Record(r.Context(), p, chat.Records[i].ID, true)
			if err != nil {
				failure(w, err)
				return
			}
			chat.Records[i].Analysis = fresh.Analysis
			chat.Records[i].PredictText = fresh.PredictText
		}
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, sqlbotChatView(chat))
}
func (h *Handler) rename(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input struct {
		ID    ID     `json:"id"`
		Brief string `json:"brief"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if err := h.service.Rename(r.Context(), p, input.ID, input.Brief); err != nil {
		failure(w, err)
		return
	}
	// The unchanged SQLBot chat list uses the response as its new title.
	okay(w, input.Brief)
}
func (h *Handler) remove(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid ID")
		return
	}
	if err := h.service.Delete(r.Context(), p, id); err != nil {
		failure(w, err)
		return
	}
	okay(w, string(id))
}

func (h *Handler) question(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		Chat        ID     `json:"chat_id"`
		Question    string `json:"question"`
		Datasource  *ID    `json:"datasource_id"`
		CustomModel any    `json:"custom_model"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if input.Datasource != nil || input.CustomModel != nil {
		writeJSON(w, 422, "Select a configured datasource and default model before starting this conversation")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		failure(w, ErrUnavailable)
		return
	}
	started := false
	event := func(value any) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			started = true
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err = w.Write(append(append([]byte("data:"), raw...), []byte("\n\n")...)); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	check := func(ctx context.Context) error {
		return h.currentIdentity(ctx, p, token)
	}
	record, err := h.service.Ask(r.Context(), p, input.Chat, input.Question, check, func(id ID) error { return event(map[string]any{"type": "id", "id": id}) })
	if err != nil {
		if !started {
			failure(w, err)
		} else {
			_ = event(map[string]any{"type": "error", "content": "Request did not complete; no result was released"})
		}
		return
	}
	if err := event(map[string]any{"type": "sql", "content": record.SQL}); err != nil {
		return
	}
	if record.Chart != "" {
		if err := event(map[string]any{"type": "chart", "content": record.Chart}); err != nil {
			return
		}
	}
	if err := event(map[string]any{"type": "sql-data", "content": "execute-success"}); err != nil {
		return
	}
	_ = event(map[string]any{"type": "finish"})
}
