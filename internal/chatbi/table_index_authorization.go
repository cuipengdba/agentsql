package chatbi

import (
	"context"
	"slices"
)

// tableIndexValidator binds a vector build to one immutable authorized schema.
// Both a foreground question and a background prewarm use this callback before
// each inference batch and immediately before any index write.
func (service *Service) tableIndexValidator(p Principal, binding Binding, revision int64, digest [32]byte,
	rules []DataPermissionRule, columns []Column, remarks []RemarkHint, model ModelConfig, lease modelLease,
	fingerprint string, check func(context.Context) error) func(context.Context) error {
	columns = append([]Column(nil), columns...)
	remarks = append([]RemarkHint(nil), remarks...)
	return func(ctx context.Context) error {
		if check == nil {
			return ErrUnauthenticated
		}
		if err := check(ctx); err != nil {
			return err
		}
		if err := service.bindingUnchanged(ctx, p, binding, revision, digest); err != nil {
			return err
		}
		if err := service.dataPermissionUnchanged(ctx, p.Workspace, rules); err != nil {
			return err
		}
		if err := service.modelUnchanged(ctx, p.Workspace, model, lease); err != nil {
			return err
		}
		if err := service.modelContextUnchanged(ctx, p, digest, rules, fingerprint); err != nil {
			return err
		}
		currentColumns, currentRules, err := service.semanticSchemaWithPermissionSnapshot(ctx, p, binding)
		if err != nil || !slices.Equal(currentColumns, columns) || !sameDataPermissionSnapshot(currentRules, rules) {
			return ErrGateway
		}
		currentRemarks, err := managedFileRemarkHints(binding, currentColumns)
		if databaseBinding(binding) {
			currentRemarks, err = databaseRemarkHints(binding, currentColumns)
		}
		if err != nil || !slices.Equal(currentRemarks, remarks) {
			return ErrGateway
		}
		return check(ctx)
	}
}
