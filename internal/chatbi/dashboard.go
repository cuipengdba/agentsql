package chatbi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrInvalidDashboard = errors.New("invalid dashboard configuration")

// Dashboard cards retain only text and references to owned query records.
// Query rows are never persisted with the canvas.
type DashboardCard struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Text     string `json:"text,omitempty"`
	RecordID ID     `json:"record_id,omitempty"`
}

type Dashboard struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Cards    []DashboardCard `json:"cards"`
	Created  time.Time       `json:"created_at"`
	Updated  time.Time       `json:"updated_at"`
	Revision int64           `json:"revision"`
}

type DashboardView struct {
	Dashboard
	Results map[string]QueryData `json:"results"`
}

func dashboardKey(p Principal, id string) objectKey {
	return objectKey{"dashboard", string(p.Workspace), string(p.User.ID), id}
}

func validDashboardID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

func (service *Service) dashboardCardsValid(ctx context.Context, p Principal, cards []DashboardCard) error {
	if len(cards) > 32 {
		return ErrInvalidDashboard
	}
	seen := make(map[string]bool, len(cards))
	for _, card := range cards {
		if !validDashboardID(card.ID) || seen[card.ID] || len([]rune(card.Title)) > 120 || len([]rune(card.Text)) > 4000 {
			return ErrInvalidDashboard
		}
		seen[card.ID] = true
		switch card.Kind {
		case "text":
			if card.RecordID != "" {
				return ErrInvalidDashboard
			}
		case "table", "bar", "line", "pie":
			if !card.RecordID.Valid() || card.Text != "" {
				return ErrInvalidDashboard
			}
			// The referenced record must be owned by this user in this workspace
			// and the source must still be available. No result is kept here.
			record, err := service.Record(ctx, p, card.RecordID, false)
			if err != nil {
				return err
			}
			if _, err := service.binding(ctx, p, record.Datasource); err != nil {
				return err
			}
		default:
			return ErrInvalidDashboard
		}
	}
	return nil
}

func (service *Service) Dashboards(ctx context.Context, p Principal) ([]Dashboard, error) {
	if err := service.live(ctx, p); err != nil {
		return nil, err
	}
	items, err := service.Store.list(ctx, "dashboard", string(p.Workspace), string(p.User.ID))
	if err != nil {
		return nil, err
	}
	result := make([]Dashboard, 0, len(items))
	for _, raw := range items {
		var item Dashboard
		if json.Unmarshal(raw, &item) != nil || !validDashboardID(item.ID) {
			return nil, ErrUnavailable
		}
		item.Cards = nil // The list is metadata only.
		result = append(result, item)
	}
	return result, nil
}

func (service *Service) Dashboard(ctx context.Context, p Principal, id string, withData bool) (DashboardView, error) {
	if err := service.live(ctx, p); err != nil {
		return DashboardView{}, err
	}
	if !validDashboardID(id) {
		return DashboardView{}, ErrNotFound
	}
	var item Dashboard
	revision, err := service.Store.get(ctx, dashboardKey(p, id), &item)
	if err != nil {
		return DashboardView{}, err
	}
	if item.ID != id {
		return DashboardView{}, ErrUnavailable
	}
	item.Revision = revision
	view := DashboardView{Dashboard: item, Results: map[string]QueryData{}}
	if withData {
		for _, card := range item.Cards {
			if card.RecordID == "" {
				continue
			}
			record, err := service.Record(ctx, p, card.RecordID, true)
			if err != nil {
				return DashboardView{}, err
			}
			if record.Data == nil || record.Data.AuditID <= 0 {
				return DashboardView{}, ErrGateway
			}
			view.Results[card.ID] = *record.Data
		}
	}
	if err := service.live(ctx, p); err != nil {
		return DashboardView{}, err
	}
	return view, nil
}

func (service *Service) SaveDashboard(ctx context.Context, p Principal, item Dashboard) (Dashboard, error) {
	if err := service.live(ctx, p); err != nil {
		return Dashboard{}, err
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || len([]rune(item.Name)) > 120 || item.Revision < 0 {
		return Dashboard{}, ErrInvalidDashboard
	}
	if err := service.dashboardCardsValid(ctx, p, item.Cards); err != nil {
		return Dashboard{}, err
	}
	if item.ID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return Dashboard{}, ErrUnavailable
		}
		item.ID = hex.EncodeToString(raw[:])
		item.Created = time.Now().UTC()
		item.Revision = 0
	} else if !validDashboardID(item.ID) || item.Revision < 1 {
		return Dashboard{}, ErrInvalidDashboard
	} else {
		var previous Dashboard
		revision, err := service.Store.get(ctx, dashboardKey(p, item.ID), &previous)
		if err != nil {
			return Dashboard{}, err
		}
		if revision != item.Revision {
			return Dashboard{}, ErrConflict
		}
		item.Created = previous.Created
	}
	item.Updated = time.Now().UTC()
	if err := service.live(ctx, p); err != nil {
		return Dashboard{}, err
	}
	if err := service.Store.put(ctx, dashboardKey(p, item.ID), item, item.Revision); err != nil {
		return Dashboard{}, err
	}
	item.Revision++
	return item, nil
}

func (service *Service) DeleteDashboard(ctx context.Context, p Principal, id string, revision int64) error {
	if err := service.live(ctx, p); err != nil {
		return err
	}
	if !validDashboardID(id) || revision < 1 {
		return ErrInvalidDashboard
	}
	return service.Store.delete(ctx, dashboardKey(p, id), revision)
}
