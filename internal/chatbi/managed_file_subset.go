package chatbi

import (
	"context"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

func (service *Service) schema(ctx context.Context, principal Principal, binding Binding) ([]Column, error) {
	columns, err := service.Gateway.Schema(ctx, principal, binding)
	if err != nil {
		return nil, err
	}
	columns, err = filterManagedFileSchema(binding, columns)
	if err != nil {
		return nil, err
	}
	return filterDatabaseSchema(binding, columns)
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
	if err := authorizeEnabledTablesSQL(binding, sql); err != nil {
		return QueryData{}, err
	}
	if err := authorizeManagedFileSQL(binding, sql); err != nil {
		return QueryData{}, err
	}
	if err := authorizeDatabaseSelectedSQL(binding, sql); err != nil {
		return QueryData{}, err
	}
	return service.Gateway.Query(ctx, principal, binding, sql)
}

// A SQLBot upload creates its tables before the browser chooses which ones to
// include in the datasource. Keep that final choice in the encrypted binding.
// The connector token stays server-side, and both discovery and execution use
// this restriction before reaching the gateway.
func managedFileSelectedTables(binding Binding) (map[string]bool, error) {
	if databaseBinding(binding) {
		return nil, nil
	}
	if binding.SelectedTables == "" {
		return nil, nil
	}
	// An explicit empty choice must not be mistaken for the legacy empty
	// string, which means every imported sheet is selected.
	if binding.SelectedTables == "!" && binding.Type == "excel" && binding.Dialect == "pg" &&
		managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		return map[string]bool{}, nil
	}
	if binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		return nil, ErrGateway
	}
	parts := strings.Split(binding.SelectedTables, ",")
	if len(parts) == 0 || len(parts) > 8 {
		return nil, ErrGateway
	}
	selected := make(map[string]bool, len(parts))
	for _, name := range parts {
		if !managedFileNamePattern.MatchString(name) || selected[name] {
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
