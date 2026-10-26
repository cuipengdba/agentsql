package chatbi

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// DashboardFolder is an owner-scoped resource tree node. It never contains
// query results or credentials.
type DashboardFolder struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	ParentID string    `json:"parent_id"`
	Created  time.Time `json:"created_at"`
	Updated  time.Time `json:"updated_at"`
	Revision int64     `json:"revision"`
}

func dashboardFolderKey(p Principal, id string) objectKey {
	return objectKey{"dashboard-folder", string(p.Workspace), string(p.User.ID), id}
}

func (service *Service) DashboardFolders(ctx context.Context, p Principal) ([]DashboardFolder, error) {
	if err := service.live(ctx, p); err != nil {
		return nil, err
	}
	items, err := service.Store.list(ctx, "dashboard-folder", string(p.Workspace), string(p.User.ID))
	if err != nil {
		return nil, err
	}
	result := make([]DashboardFolder, 0, len(items))
	for _, raw := range items {
		var item DashboardFolder
		if json.Unmarshal(raw, &item) != nil || !validDashboardID(item.ID) {
			return nil, ErrUnavailable
		}
		result = append(result, item)
	}
	return result, nil
}

func (service *Service) DashboardFolder(ctx context.Context, p Principal, id string) (DashboardFolder, error) {
	if err := service.live(ctx, p); err != nil {
		return DashboardFolder{}, err
	}
	if !validDashboardID(id) {
		return DashboardFolder{}, ErrNotFound
	}
	var item DashboardFolder
	revision, err := service.Store.get(ctx, dashboardFolderKey(p, id), &item)
	if err != nil {
		return DashboardFolder{}, err
	}
	if item.ID != id {
		return DashboardFolder{}, ErrUnavailable
	}
	item.Revision = revision
	return item, nil
}

func (service *Service) SaveDashboardFolder(ctx context.Context, p Principal, item DashboardFolder) (DashboardFolder, error) {
	if err := service.live(ctx, p); err != nil {
		return DashboardFolder{}, err
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || len([]rune(item.Name)) > 64 || item.Revision < 0 {
		return DashboardFolder{}, ErrInvalidDashboard
	}
	if item.ParentID == "" {
		item.ParentID = "root"
	}
	if item.ParentID != "root" {
		ancestor := item.ParentID
		for depth := 0; depth < 8; depth++ {
			if ancestor == item.ID || ancestor == "root" {
				return DashboardFolder{}, ErrInvalidDashboard
			}
			parent, err := service.DashboardFolder(ctx, p, ancestor)
			if err != nil {
				return DashboardFolder{}, err
			}
			if parent.ParentID == "root" {
				ancestor = "root"
				break
			}
			ancestor = parent.ParentID
		}
		if ancestor != "root" {
			return DashboardFolder{}, ErrInvalidDashboard
		}
	}
	if item.ID == "" {
		var err error
		item.ID, err = newDashboardID()
		if err != nil {
			return DashboardFolder{}, err
		}
		item.Created = time.Now().UTC()
		item.Revision = 0
	} else if !validDashboardID(item.ID) || item.Revision < 1 {
		return DashboardFolder{}, ErrInvalidDashboard
	} else {
		previous, err := service.DashboardFolder(ctx, p, item.ID)
		if err != nil {
			return DashboardFolder{}, err
		}
		if previous.Revision != item.Revision {
			return DashboardFolder{}, ErrConflict
		}
		item.Created = previous.Created
	}
	item.Updated = time.Now().UTC()
	if err := service.Store.put(ctx, dashboardFolderKey(p, item.ID), item, item.Revision); err != nil {
		return DashboardFolder{}, err
	}
	item.Revision++
	return item, nil
}

func (service *Service) DeleteDashboardFolder(ctx context.Context, p Principal, id string) error {
	folder, err := service.DashboardFolder(ctx, p, id)
	if err != nil {
		return err
	}
	folders, err := service.DashboardFolders(ctx, p)
	if err != nil {
		return err
	}
	for _, child := range folders {
		if child.ParentID == id {
			return ErrConflict
		}
	}
	boards, err := service.Dashboards(ctx, p)
	if err != nil {
		return err
	}
	for _, board := range boards {
		if board.ParentID == id {
			return ErrConflict
		}
	}
	return service.Store.delete(ctx, dashboardFolderKey(p, id), folder.Revision)
}
