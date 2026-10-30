package chatbi

import (
	"context"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/rowfilter"
)

func (service *Service) schema(ctx context.Context, principal Principal, binding Binding) ([]Column, error) {
	columns, snapshot, err := service.gatewaySchemaWithPermissionSnapshot(ctx, principal, binding)
	if err != nil {
		return nil, err
	}
	columns, err = filterManagedFileSchema(binding, columns)
	if err != nil {
		return nil, err
	}
	visible, err := filterDatabaseSchema(binding, columns)
	if err != nil {
		return nil, err
	}
	if err := service.dataPermissionUnchanged(ctx, principal.Workspace, snapshot); err != nil {
		return nil, err
	}
	return visible, nil
}

// gatewaySchema also covers schema discovery paths that deliberately bypass
// selected-table filtering. No such path may expose metadata while an active
// compatibility rule lacks an enforcement engine.
func (service *Service) gatewaySchema(ctx context.Context, principal Principal, binding Binding) ([]Column, error) {
	columns, _, err := service.gatewaySchemaWithPermissionSnapshot(ctx, principal, binding)
	return columns, err
}

func (service *Service) gatewaySchemaWithPermissionSnapshot(ctx context.Context, principal Principal, binding Binding) ([]Column, []DataPermissionRule, error) {
	snapshot, err := service.dataPermissionSnapshot(ctx, principal.Workspace)
	if err != nil {
		return nil, nil, err
	}
	items, err := applicableRowPermissions(snapshot, principal, binding)
	if err != nil {
		return nil, nil, err
	}
	columns, err := service.Gateway.Schema(ctx, principal, binding)
	if err != nil {
		return nil, nil, err
	}
	if len(items) != 0 {
		if _, _, _, err := compileApplicableRowPermissions(items, binding.Datasource, columns); err != nil {
			return nil, nil, err
		}
	}
	if err := service.dataPermissionUnchanged(ctx, principal.Workspace, snapshot); err != nil {
		return nil, nil, err
	}
	return columns, snapshot, nil
}

func (service *Service) semanticSchema(ctx context.Context, principal Principal, binding Binding) ([]Column, error) {
	columns, err := service.schema(ctx, principal, binding)
	if err != nil {
		return nil, err
	}
	columns, err = filterDisabledTables(binding, columns)
	if err != nil {
		return nil, err
	}
	return filterDisabledFields(binding, columns)
}

func (service *Service) query(ctx context.Context, principal Principal, binding Binding, sql string) (QueryData, error) {
	snapshot, err := service.dataPermissionSnapshot(ctx, principal.Workspace)
	if err != nil {
		return QueryData{}, err
	}
	items, err := applicableRowPermissions(snapshot, principal, binding)
	if err != nil {
		return QueryData{}, err
	}
	if len(items) != 0 {
		columns, checked, err := service.gatewaySchemaWithPermissionSnapshot(ctx, principal, binding)
		if err != nil || !sameDataPermissionSnapshot(snapshot, checked) {
			return QueryData{}, ErrGateway
		}
		schema, table, filters, err := compileApplicableRowPermissions(items, binding.Datasource, columns)
		if err != nil {
			return QueryData{}, err
		}
		sql, _, err = rowfilter.RewriteSingleTable(sql, schema, table, filters)
		if err != nil {
			return QueryData{}, ErrGateway
		}
	}
	if err := authorizeEnabledTablesSQL(binding, sql); err != nil {
		return QueryData{}, err
	}
	if err := authorizeEnabledFieldsSQL(binding, sql); err != nil {
		return QueryData{}, err
	}
	if err := authorizeManagedFileSQL(binding, sql); err != nil {
		return QueryData{}, err
	}
	if err := authorizeDatabaseSelectedSQL(binding, sql); err != nil {
		return QueryData{}, err
	}
	data, err := service.Gateway.Query(ctx, principal, binding, sql)
	if err != nil {
		return QueryData{}, err
	}
	if err := service.dataPermissionUnchanged(ctx, principal.Workspace, snapshot); err != nil {
		return QueryData{}, err
	}
	return data, nil
}

// File imports share a physical PostgreSQL database. The server-held datasource
// configuration is the outer table boundary, even when every imported sheet
// is selected. A later browser selection can only narrow that boundary.
func managedFileSelectedTables(binding Binding) (map[string]bool, error) {
	if databaseBinding(binding) {
		return nil, nil
	}
	if binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		return nil, ErrGateway
	}
	_, sheets, err := decodeSQLBotExcelConfig(binding.Configuration)
	if err != nil {
		return nil, ErrGateway
	}
	imported := make(map[string]bool, len(sheets))
	for _, sheet := range sheets {
		if imported[sheet.TableName] {
			return nil, ErrGateway
		}
		imported[sheet.TableName] = true
	}
	if binding.SelectedTables == "" {
		return imported, nil
	}
	// An explicit empty choice must not be mistaken for the legacy empty
	// string, which means every imported sheet is selected.
	if binding.SelectedTables == "!" {
		return map[string]bool{}, nil
	}
	parts := strings.Split(binding.SelectedTables, ",")
	if len(parts) == 0 || len(parts) > 8 {
		return nil, ErrGateway
	}
	selected := make(map[string]bool, len(parts))
	for _, name := range parts {
		if !managedFileNamePattern.MatchString(name) || !imported[name] || selected[name] {
			return nil, ErrGateway
		}
		selected[name] = true
	}
	return selected, nil
}

func filterManagedFileSchema(binding Binding, columns []Column) ([]Column, error) {
	selected, err := managedFileSelectedTables(binding)
	if err != nil || selected == nil {
		return columns, err
	}
	visible := make([]Column, 0, len(columns))
	found := make(map[string]bool, len(selected))
	for _, column := range columns {
		if column.Schema == "agentsql_files" && selected[column.Table] {
			visible = append(visible, column)
			found[column.Table] = true
		}
	}
	if len(found) != len(selected) {
		return nil, ErrGateway
	}
	return visible, nil
}

func authorizeManagedFileSQL(binding Binding, sql string) error {
	selected, err := managedFileSelectedTables(binding)
	if err != nil || selected == nil {
		return err
	}
	query, err := parser.ParsePostgresClosedSelect(sql)
	if err != nil || len(query.Relations) == 0 {
		return ErrGateway
	}
	for _, relation := range query.Relations {
		if relation.Schema != "agentsql_files" || !selected[relation.Name] {
			return ErrGateway
		}
	}
	return nil
}

// Ask uses the current gateway-visible schema as its exact model grounding.
// This check reports a model failure for a hallucinated managed-file relation
// without executing it or disclosing a guessed table name to the client.
func validateGeneratedManagedFileSQL(binding Binding, columns []Column, sql string) error {
	if binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		return nil
	}
	postgres, err := parser.NewParser(model.DialectPostgres)
	if err != nil {
		return ErrModel
	}
	query, err := postgres.Parse(sql)
	if err != nil || query.IsMulti || query.StmtType != "SELECT" || len(query.Tables) == 0 {
		return ErrModel
	}
	visible := make(map[string]bool)
	for _, column := range columns {
		if column.Schema == "agentsql_files" {
			visible[column.Table] = true
		}
	}
	for _, relation := range query.Tables {
		if relation.Schema != "agentsql_files" || !visible[relation.Table] {
			return ErrModel
		}
	}
	return nil
}
