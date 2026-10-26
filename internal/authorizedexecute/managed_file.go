package authorizedexecute

import (
	"context"
	"errors"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

// ManagedFileProvisioner is a narrow, typed capability for a separately
// configured PostgreSQL provisioning role. Callers cannot supply SQL, a table
// name, a schema, or database credentials to Stage or Drop.
type ManagedFileProvisioner struct {
	readDatasource model.Datasource
	target         model.Datasource
	secret         string
}

type ManagedFileSheet = businessdb.ManagedFileSheet
type ManagedFileColumn = businessdb.ManagedFileColumn
type ManagedFileTable = businessdb.ManagedFileTable

var ErrManagedFileTarget = errors.New("managed file provisioning target is not isolated")

func NewManagedFileProvisioner(readDatasource, provisioner model.Datasource, provisionerSecret string) (*ManagedFileProvisioner, error) {
	if readDatasource.ID == "" || provisioner.ID == "" || readDatasource.ID == provisioner.ID ||
		readDatasource.DBType != "postgres" || provisioner.DBType != "postgres" ||
		readDatasource.Host == "" || readDatasource.Host != provisioner.Host ||
		readDatasource.Port <= 0 || readDatasource.Port != provisioner.Port ||
		readDatasource.Database == "" || readDatasource.Database != provisioner.Database ||
		readDatasource.Username == "" || provisioner.Username == "" || readDatasource.Username == provisioner.Username ||
		provisionerSecret == "" || (readDatasource.TLSMode != "verify-full" && readDatasource.TLSMode != "disable") ||
		readDatasource.TLSMode != provisioner.TLSMode ||
		readDatasource.TLSServerName != provisioner.TLSServerName || readDatasource.TLSCAFile != provisioner.TLSCAFile ||
		readDatasource.TrustServerCertificate || provisioner.TrustServerCertificate {
		return nil, ErrManagedFileTarget
	}
	return &ManagedFileProvisioner{readDatasource: readDatasource, target: provisioner, secret: provisionerSecret}, nil
}

func (p *ManagedFileProvisioner) ReadDatasourceID() string {
	if p == nil {
		return ""
	}
	return p.readDatasource.ID
}

func (p *ManagedFileProvisioner) MatchesReadDatasource(current model.Datasource) bool {
	if p == nil {
		return false
	}
	old := p.readDatasource
	return current.ID == old.ID && current.DBType == old.DBType && current.Host == old.Host &&
		current.Port == old.Port && current.Database == old.Database && current.Username == old.Username &&
		current.TLSMode == old.TLSMode && current.TLSServerName == old.TLSServerName && current.TLSCAFile == old.TLSCAFile
}

func (p *ManagedFileProvisioner) Stage(ctx context.Context, journalID string, sheets []ManagedFileSheet) ([]ManagedFileTable, error) {
	if p == nil {
		return nil, ErrManagedFileTarget
	}
	id, err := businessdb.ParseManagedFileOperationID(journalID)
	if err != nil {
		return nil, err
	}
	return businessdb.StageManagedFiles(ctx, p.target, p.secret, p.readDatasource.Username, id, sheets)
}

func (p *ManagedFileProvisioner) Drop(ctx context.Context, journalID string, sheetCount int) error {
	if p == nil {
		return ErrManagedFileTarget
	}
	id, err := businessdb.ParseManagedFileOperationID(journalID)
	if err != nil {
		return err
	}
	return businessdb.DropManagedFiles(ctx, p.target, p.secret, id, sheetCount)
}

func (p *ManagedFileProvisioner) GrantRead(ctx context.Context, journalID string, sheetCount int) error {
	if p == nil {
		return ErrManagedFileTarget
	}
	id, err := businessdb.ParseManagedFileOperationID(journalID)
	if err != nil {
		return err
	}
	return businessdb.GrantManagedFileRead(ctx, p.target, p.secret, p.readDatasource.Username, id, sheetCount)
}

func (p *ManagedFileProvisioner) ExpectedTableName(journalID string, sheet int) (string, error) {
	if p == nil {
		return "", ErrManagedFileTarget
	}
	id, err := businessdb.ParseManagedFileOperationID(journalID)
	if err != nil {
		return "", err
	}
	return businessdb.ManagedFileName(id, sheet)
}
