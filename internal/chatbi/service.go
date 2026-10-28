package chatbi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrBusy = errors.New("this conversation already has a running question")

type Chat struct {
	ID               ID        `json:"id"`
	Workspace        ID        `json:"oid"`
	Creator          ID        `json:"create_by"`
	Created          time.Time `json:"create_time"`
	Latest           time.Time `json:"latest_record_time"`
	Brief            string    `json:"brief"`
	Type             string    `json:"chat_type"`
	Datasource       ID        `json:"datasource,omitempty"`
	Engine           string    `json:"engine_type"`
	DSType           string    `json:"ds_type"`
	DatasourceName   string    `json:"datasource_name"`
	DatasourceExists bool      `json:"datasource_exists"`
	Origin           int       `json:"origin"`
	EmbeddedApp      ID        `json:"embedded_app,omitempty"`
	Records          []Record  `json:"records"`
	LeaseUntil       time.Time `json:"lease_until,omitempty"`
}

type Record struct {
	ID          ID         `json:"id"`
	Chat        ID         `json:"chat_id"`
	Datasource  ID         `json:"datasource"`
	Question    string     `json:"question"`
	Created     time.Time  `json:"create_time"`
	FinishedAt  *time.Time `json:"finish_time"`
	SQL         string     `json:"sql"`
	SQLAnswer   string     `json:"sql_answer"`
	Chart       string     `json:"chart,omitempty"`
	Analysis    string     `json:"analysis,omitempty"`
	PredictText string     `json:"predict_content,omitempty"`
	AnalysisOf  ID         `json:"analysis_record_id,omitempty"`
	PredictOf   ID         `json:"predict_record_id,omitempty"`
	Action      string     `json:"action,omitempty"`
	DataDigest  string     `json:"data_digest,omitempty"`
	Data        *QueryData `json:"data,omitempty"`
	Finish      bool       `json:"finish"`
	FirstChat   bool       `json:"first_chat,omitempty"`
	Error       string     `json:"error"`
	ErrorStage  string     `json:"error_stage,omitempty"`
	TotalTokens int        `json:"total_tokens"`
	Duration    float64    `json:"duration"`
}

type Service struct {
	Store                 *Store
	Identity              *Identity
	Gateway               Gateway
	Model                 Generator
	ManagedFileDatasource ID
}

func newID() (ID, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrUnavailable
	}
	// SQLBot clients send IDs through JavaScript numbers. Keep new IDs exactly
	// representable while retaining 53 bits of random collision resistance.
	n := binary.BigEndian.Uint64(raw[:]) & maxSQLBotBrowserID
	if n == 0 {
		return newID()
	}
	return ID(strconv.FormatUint(n, 10)), nil
}

func chatKey(p Principal, id ID) objectKey {
	return objectKey{"chat", string(p.Workspace), string(p.User.ID), string(id)}
}
func bindingKey(p Principal, id ID) objectKey {
	return objectKey{"binding", string(p.Workspace), string(p.User.ID), string(id)}
}

func recordKey(p Principal, id ID) objectKey {
	return objectKey{"record-index", string(p.Workspace), string(p.User.ID), string(id)}
}

// A table is the conservative visualization of an authorized query result.
// Its field labels come from the gateway response; rows never enter this
// persistent chart description.
func tableChartConfig(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	columns := make([]map[string]string, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, map[string]string{"name": field, "value": field})
	}
	raw, err := json.Marshal(map[string]any{
		"type": "table", "title": "", "columns": columns, "axis": map[string]any{},
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

func (service *Service) Record(ctx context.Context, p Principal, id ID, withData bool) (Record, error) {
	if err := service.live(ctx, p); err != nil {
		return Record{}, err
	}
	var chatID ID
	if _, err := service.Store.get(ctx, recordKey(p, id), &chatID); err != nil {
		return Record{}, err
	}
	chat, _, err := service.chat(ctx, p, chatID)
	if err != nil {
		return Record{}, err
	}
	for _, record := range chat.Records {
		if record.ID == id {
			record.Data = nil
			if !withData {
				record.Analysis, record.PredictText = "", ""
			}
			if withData && record.Finish && record.Error == "" {
				binding, revision, digest, err := service.bindingSnapshot(ctx, p, chat.Datasource)
				if err != nil {
					return Record{}, err
				}
				data, err := service.query(ctx, p, binding, record.SQL)
				if err != nil {
					return Record{}, err
				}
				if data.AuditID <= 0 {
					return Record{}, ErrGateway
				}
				if err := service.bindingUnchanged(ctx, p, binding, revision, digest); err != nil {
					return Record{}, err
				}
				record.Data = &data
				if record.Action != "" && record.DataDigest != resultDigest(data) {
					record.Data = nil
					record.Analysis, record.PredictText = "", ""
				}
			}
			record.DataDigest = ""
			if err := service.live(ctx, p); err != nil {
				return Record{}, err
			}
			return record, nil
		}
	}
	return Record{}, ErrNotFound
}

func (service *Service) live(ctx context.Context, p Principal) error {
	user, _, err := service.Store.User(ctx, p.User.ID)
	if err != nil || user.AuthRevision != p.User.AuthRevision || !user.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	if p.EmbeddedApp != "" {
		if !p.EmbeddedApp.Valid() || p.EmbeddedRevision < 1 || user.Admin {
			return ErrUnauthenticated
		}
		var app AssistantApplication
		revision, err := service.Store.get(ctx, assistantKey(p.Workspace, p.EmbeddedApp), &app)
		if err != nil || revision != p.EmbeddedRevision || app.ID != p.EmbeddedApp || app.Workspace != p.Workspace || app.Type != 4 || !app.Enabled {
			return ErrUnauthenticated
		}
	}
	return nil
}

func (service *Service) binding(ctx context.Context, p Principal, id ID) (Binding, error) {
	binding, _, _, err := service.bindingSnapshot(ctx, p, id)
	return binding, err
}

func (service *Service) bindingSnapshot(ctx context.Context, p Principal, id ID) (Binding, int64, [32]byte, error) {
	if err := service.live(ctx, p); err != nil {
		return Binding{}, 0, [32]byte{}, err
	}
	if err := service.embeddedSourceAllowed(ctx, p, id); err != nil {
		return Binding{}, 0, [32]byte{}, err
	}
	var binding Binding
	revision, digest, err := service.Store.getWithDigest(ctx, bindingKey(p, id), &binding)
	if err != nil {
		return Binding{}, 0, [32]byte{}, err
	}
	if binding.User != p.User.ID || binding.Workspace != p.Workspace || binding.Datasource != id {
		return Binding{}, 0, [32]byte{}, ErrUnavailable
	}
	return binding, revision, digest, nil
}

// A result may be released only while the encrypted local binding used for
// its schema/query still matches the current object and revision. This also
// catches credential or datasource replacement at the same external ID.
func (service *Service) bindingUnchanged(ctx context.Context, p Principal, binding Binding, revision int64, digest [32]byte) error {
	current, currentRevision, currentDigest, err := service.bindingSnapshot(ctx, p, binding.Datasource)
	if err != nil {
		return err
	}
	if currentRevision != revision || currentDigest != digest || current != binding {
		return ErrGateway
	}
	return nil
}

func (service *Service) Start(ctx context.Context, p Principal, datasource ID, question string, origin int) (Chat, error) {
	if len(question) > 16000 || (origin != 0 && origin != 2) || (origin == 2) != (p.EmbeddedApp != "") {
		return Chat{}, errors.New("unsupported chat request")
	}
	binding, bindingRevision, bindingDigest, err := service.bindingSnapshot(ctx, p, datasource)
	if err != nil {
		return Chat{}, err
	}
	if _, err := service.schema(ctx, p, binding); err != nil {
		return Chat{}, err
	}
	if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
		return Chat{}, err
	}
	if err := service.live(ctx, p); err != nil {
		return Chat{}, err
	}
	id, err := newID()
	if err != nil {
		return Chat{}, err
	}
	brief := []rune(strings.TrimSpace(question))
	if len(brief) > 64 {
		brief = brief[:64]
	}
	if len(brief) == 0 {
		brief = []rune(binding.Name)
	}
	now := time.Now().UTC()
	chat := Chat{ID: id, Workspace: p.Workspace, Creator: p.User.ID, Created: now, Latest: now, Brief: string(brief), Type: "chat", Datasource: datasource, Engine: binding.Dialect, DSType: binding.Dialect, DatasourceName: binding.Name, DatasourceExists: true, Origin: origin, EmbeddedApp: p.EmbeddedApp, Records: []Record{}}
	if err := service.Store.put(ctx, chatKey(p, id), chat, 0); err != nil {
		return Chat{}, err
	}
	return chat, nil
}

// StartMCP records a source-less original MCP session. The first question
// must select a live, owned datasource before any schema or model call.
func (service *Service) StartMCP(ctx context.Context, p Principal) (Chat, error) {
	if p.EmbeddedApp != "" {
		return Chat{}, ErrUnauthenticated
	}
	if err := service.live(ctx, p); err != nil {
		return Chat{}, err
	}
	id, err := newID()
	if err != nil {
		return Chat{}, err
	}
	now := time.Now().UTC()
	chat := Chat{ID: id, Workspace: p.Workspace, Creator: p.User.ID, Created: now, Latest: now,
		Brief: "New conversation", Type: "chat", Origin: 1, Records: []Record{}}
	if err := service.Store.put(ctx, chatKey(p, id), chat, 0); err != nil {
		return Chat{}, err
	}
	return chat, nil
}

// BindMCPSource uses the same authorization and schema gate as ordinary
// conversations. A source cannot change after a question has been recorded.
func (service *Service) BindMCPSource(ctx context.Context, p Principal, chatID, datasource ID) error {
	chat, revision, err := service.chat(ctx, p, chatID)
	if err != nil {
		return err
	}
	if chat.Origin != 1 || chat.EmbeddedApp != "" || len(chat.Records) != 0 || chat.LeaseUntil.After(time.Now()) {
		return ErrConflict
	}
	if chat.Datasource != "" {
		if chat.Datasource == datasource {
			return nil
		}
		return ErrConflict
	}
	binding, bindingRevision, bindingDigest, err := service.bindingSnapshot(ctx, p, datasource)
	if err != nil {
		return err
	}
	if _, err := service.schema(ctx, p, binding); err != nil {
		return err
	}
	if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
		return err
	}
	chat.Datasource, chat.DatasourceName, chat.DatasourceExists = datasource, binding.Name, true
	chat.Engine, chat.DSType = binding.Dialect, binding.Dialect
	return service.Store.put(ctx, chatKey(p, chatID), chat, revision)
}

func (service *Service) chat(ctx context.Context, p Principal, id ID) (Chat, int64, error) {
	if err := service.live(ctx, p); err != nil {
		return Chat{}, 0, err
	}
	var chat Chat
	rev, err := service.Store.get(ctx, chatKey(p, id), &chat)
	if err != nil {
		return Chat{}, 0, err
	}
	if chat.ID != id || chat.Workspace != p.Workspace || chat.Creator != p.User.ID || chat.EmbeddedApp != p.EmbeddedApp {
		return Chat{}, 0, ErrUnavailable
	}
	return chat, rev, nil
}

func (service *Service) Chats(ctx context.Context, p Principal) ([]Chat, error) {
	if err := service.live(ctx, p); err != nil {
		return nil, err
	}
	raw, err := service.Store.list(ctx, "chat", string(p.Workspace), string(p.User.ID))
	if err != nil {
		return nil, err
	}
	chats := make([]Chat, 0, len(raw))
	for _, entry := range raw {
		var chat Chat
		if json.Unmarshal(entry, &chat) != nil || chat.Creator != p.User.ID || chat.Workspace != p.Workspace {
			return nil, ErrUnavailable
		}
		if chat.EmbeddedApp != p.EmbeddedApp {
			continue
		}
		chat.Records = []Record{}
		chat.LeaseUntil = time.Time{}
		chats = append(chats, chat)
	}
	return chats, nil
}

// RecentQuestions returns only this user's questions for a currently bound
// datasource. Historical result rows never enter this suggestion list.
func (service *Service) RecentQuestions(ctx context.Context, p Principal, datasource ID) ([]string, error) {
	binding, bindingRevision, bindingDigest, err := service.bindingSnapshot(ctx, p, datasource)
	if err != nil {
		return nil, err
	}
	items, err := service.Store.list(ctx, "chat", string(p.Workspace), string(p.User.ID))
	if err != nil {
		return nil, err
	}
	type recent struct {
		question string
		created  time.Time
	}
	unique := map[string]time.Time{}
	for _, raw := range items {
		var chat Chat
		if json.Unmarshal(raw, &chat) != nil || chat.Workspace != p.Workspace || chat.Creator != p.User.ID {
			return nil, ErrUnavailable
		}
		if chat.EmbeddedApp != p.EmbeddedApp {
			continue
		}
		if chat.Datasource != datasource {
			continue
		}
		for _, record := range chat.Records {
			if record.Datasource != datasource || strings.TrimSpace(record.Question) == "" {
				continue
			}
			if previous, found := unique[record.Question]; !found || record.Created.After(previous) {
				unique[record.Question] = record.Created
			}
		}
	}
	ordered := make([]recent, 0, len(unique))
	for question, created := range unique {
		ordered = append(ordered, recent{question, created})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].created.Equal(ordered[j].created) {
			return ordered[i].question < ordered[j].question
		}
		return ordered[i].created.After(ordered[j].created)
	})
	if len(ordered) > 10 {
		ordered = ordered[:10]
	}
	questions := make([]string, 0, len(ordered))
	for _, item := range ordered {
		questions = append(questions, item.question)
	}
	if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
		return nil, err
	}
	return questions, nil
}

// ReadChat never replays persisted rows under stale permissions. Until gateway
// policy snapshot attestations are available, requesting data reexecutes each
// read through the current live authorization/masking/audit boundary.
func (service *Service) ReadChat(ctx context.Context, p Principal, id ID, withData bool) (Chat, error) {
	chat, _, err := service.chat(ctx, p, id)
	if err != nil {
		return Chat{}, err
	}
	if chat.Origin == 1 && chat.Datasource == "" && len(chat.Records) == 0 {
		if err := service.live(ctx, p); err != nil {
			return Chat{}, err
		}
		chat.LeaseUntil = time.Time{}
		return chat, nil
	}
	binding, bindingRevision, bindingDigest, err := service.bindingSnapshot(ctx, p, chat.Datasource)
	if err != nil {
		return Chat{}, err
	}
	if _, err := service.schema(ctx, p, binding); err != nil {
		return Chat{}, err
	}
	for i := range chat.Records {
		record := &chat.Records[i]
		record.Data = nil
		if !withData {
			record.Analysis, record.PredictText = "", ""
		}
		if withData && record.Finish && record.Error == "" && record.SQL != "" {
			data, err := service.query(ctx, p, binding, record.SQL)
			if err != nil {
				return Chat{}, err
			}
			if data.AuditID <= 0 {
				return Chat{}, ErrGateway
			}
			record.Data = &data
			if record.Action != "" && record.DataDigest != resultDigest(data) {
				record.Data = nil
				record.Analysis, record.PredictText = "", ""
			}
		}
		record.DataDigest = ""
	}
	if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
		return Chat{}, err
	}
	if err := service.live(ctx, p); err != nil {
		return Chat{}, err
	}
	chat.LeaseUntil = time.Time{}
	return chat, nil
}

func (service *Service) Rename(ctx context.Context, p Principal, id ID, brief string) error {
	if strings.TrimSpace(brief) == "" || len([]rune(brief)) > 64 {
		return errors.New("invalid conversation name")
	}
	chat, rev, err := service.chat(ctx, p, id)
	if err != nil {
		return err
	}
	if chat.LeaseUntil.After(time.Now()) {
		return ErrBusy
	}
	chat.Brief = brief
	return service.Store.put(ctx, chatKey(p, id), chat, rev)
}

func (service *Service) Delete(ctx context.Context, p Principal, id ID) error {
	chat, rev, err := service.chat(ctx, p, id)
	if err != nil {
		return err
	}
	if chat.LeaseUntil.After(time.Now()) {
		return ErrBusy
	}
	return service.Store.delete(ctx, chatKey(p, id), rev)
}

// Ask commits both start and terminal state. Results are delivered only after
// successful gateway audit, local persistence and a final identity check.
func (service *Service) Ask(ctx context.Context, p Principal, id ID, question string, check func(context.Context) error, onStart func(ID) error) (Record, error) {
	ctx, cancelRequest := context.WithTimeout(ctx, 4*time.Minute)
	defer cancelRequest()
	if check == nil {
		return Record{}, ErrUnauthenticated
	}
	if len(strings.TrimSpace(question)) == 0 || len(question) > 16000 {
		return Record{}, errors.New("invalid question")
	}
	if err := check(ctx); err != nil {
		return Record{}, err
	}
	chat, revision, err := service.chat(ctx, p, id)
	if err != nil {
		return Record{}, err
	}
	if chat.LeaseUntil.After(time.Now()) {
		return Record{}, ErrBusy
	}
	if len(chat.Records) >= 200 {
		return Record{}, errors.New("conversation record limit reached")
	}
	// The chat is scoped to the authenticated principal. Send only successful
	// questions and SQL from this same datasource; never send result rows or
	// failed/interrupted attempts back to the model.
	settings, err := service.Store.ChatSettings(ctx)
	if err != nil {
		return Record{}, err
	}
	history := make([]TurnContext, 0, settings.ContextRecords)
	historyBytes := 0
	for i := len(chat.Records) - 1; i >= 0 && len(history) < settings.ContextRecords; i-- {
		prior := chat.Records[i]
		if !prior.Finish || prior.Action != "" || prior.Error != "" || prior.Datasource != chat.Datasource || prior.Question == "" || prior.SQL == "" {
			continue
		}
		length := len(prior.Question) + len(prior.SQL)
		if length > 4000 || historyBytes+length > 10000 {
			continue
		}
		history = append(history, TurnContext{Question: prior.Question, SQL: prior.SQL})
		historyBytes += length
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	binding, bindingRevision, bindingDigest, err := service.bindingSnapshot(ctx, p, chat.Datasource)
	if err != nil {
		return Record{}, err
	}
	var model ModelConfig
	if _, err := service.Store.get(ctx, objectKey{"model", string(p.Workspace), "workspace", "default"}, &model); err != nil {
		return Record{}, err
	}
	recordID, err := newID()
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	for i := range chat.Records {
		if !chat.Records[i].Finish {
			chat.Records[i].Finish = true
			chat.Records[i].Error = "interrupted before completion"
			chat.Records[i].FinishedAt = &now
			chat.Records[i].Data = nil
		}
	}
	record := Record{ID: recordID, Chat: id, Datasource: chat.Datasource, Question: question, Created: now}
	chat.Records = append(chat.Records, record)
	chat.LeaseUntil = now.Add(5 * time.Minute)
	chat.Latest = now
	if err := service.Store.put(ctx, recordKey(p, recordID), id, 0); err != nil {
		return Record{}, err
	}
	if err := service.Store.put(ctx, chatKey(p, id), chat, revision); err != nil {
		_ = service.Store.delete(context.WithoutCancel(ctx), recordKey(p, recordID), 1)
		return Record{}, err
	}
	revision++
	stage := "metadata"
	run := func() error {
		if onStart != nil {
			if err := onStart(recordID); err != nil {
				return err
			}
		}
		columns, err := service.schema(ctx, p, binding)
		if err != nil {
			return err
		}
		if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		hints, err := service.Vocabulary(ctx, p, chat.Datasource, question)
		if err != nil {
			return err
		}
		var examples []ExampleHint
		if _, ok := service.Model.(GroundedGenerator); ok {
			examples, err = service.SafeExamples(ctx, p, chat.Datasource, binding.Dialect, columns, question)
			if err != nil {
				return err
			}
		}
		remarks, err := managedFileRemarkHints(binding, columns)
		if err != nil {
			return err
		}
		stage = "model"
		var generated SQLGeneration
		if described, ok := service.Model.(DescribedGenerator); ok {
			generated, err = described.GenerateWithDescriptions(ctx, model, binding.Dialect, question, columns, history, hints, examples, remarks)
		} else if grounded, ok := service.Model.(GroundedGenerator); ok {
			generated, err = grounded.GenerateWithGrounding(ctx, model, binding.Dialect, question, columns, history, hints, examples)
		} else if semantic, ok := service.Model.(SemanticGenerator); ok {
			generated, err = semantic.GenerateWithSemantics(ctx, model, binding.Dialect, question, columns, history, hints)
		} else if contextual, ok := service.Model.(ContextualGenerator); ok {
			generated, err = contextual.GenerateWithHistory(ctx, model, binding.Dialect, question, columns, history)
		} else {
			generated, err = service.Model.Generate(ctx, model, binding.Dialect, question, columns)
		}
		if err != nil {
			return err
		}
		// A model can miss one character in a newly imported table name. Reject
		// that SQL before it reaches the gateway; never guess or repair a target.
		if err := validateGeneratedManagedFileSQL(binding, columns, generated.SQL); err != nil {
			return err
		}
		record.SQL, record.SQLAnswer, record.TotalTokens = generated.SQL, generated.SQL, generated.TotalTokens
		if err := check(ctx); err != nil {
			return err
		}
		if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
			return err
		}
		stage = "controlled_query"
		data, err := service.query(ctx, p, binding, generated.SQL)
		if err != nil {
			return err
		}
		if data.AuditID <= 0 {
			return ErrGateway
		}
		if err := check(ctx); err != nil {
			return err
		}
		if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
			return err
		}
		record.Data = &data
		record.Chart = tableChartConfig(data.Fields)
		return nil
	}
	runErr := run()
	finished := time.Now().UTC()
	record.Finish = true
	record.FinishedAt = &finished
	record.Duration = finished.Sub(now).Seconds()
	if runErr != nil {
		record.Data = nil
		record.Error = "request did not complete; no result was released"
		record.ErrorStage = stage
		record.SQL = ""
		record.SQLAnswer = ""
		record.Chart = ""
	}
	chat.Records[len(chat.Records)-1] = record
	chat.LeaseUntil = time.Time{}
	chat.Latest = finished
	// A cancelled client still gets a durable interrupted state, bounded to 5s.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := service.Store.put(saveCtx, chatKey(p, id), chat, revision); err != nil {
		return Record{}, err
	}
	if runErr != nil {
		return record, runErr
	}
	if err := check(ctx); err != nil {
		return Record{}, err
	}
	if err := service.bindingUnchanged(ctx, p, binding, bindingRevision, bindingDigest); err != nil {
		return Record{}, err
	}
	return record, nil
}
