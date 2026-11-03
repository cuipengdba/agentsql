package chatbi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type catalogFixtureEmbedding struct{ fail bool }

func (model catalogFixtureEmbedding) Encode(_ context.Context, texts []string) ([][]float32, error) {
	if model.fail {
		return nil, errors.New("synthetic embedding failure")
	}
	vectors := make([][]float32, len(texts))
	for index, value := range texts {
		vector := make([]float32, 768)
		if strings.Contains(strings.ToLower(value), "weather") {
			vector[1] = 1
		} else {
			vector[0] = 1
		}
		vectors[index] = vector
	}
	return vectors, nil
}

type catalogChangingEmbedding struct{ change func() }

func (model *catalogChangingEmbedding) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	if model.change != nil {
		model.change()
		model.change = nil
	}
	return catalogFixtureEmbedding{}.Encode(ctx, texts)
}

func TestAssistantCatalogPreselectionRanksOnlyRegisteredEntries(t *testing.T) {
	entries := []assistantCatalogEntry{
		{CatalogID: "81", Datasource: "10", Name: "Sales"},
		{CatalogID: "82", Datasource: "11", Name: "Weather"},
		{CatalogID: "83", Datasource: "12", Name: "Inventory"},
	}
	chosen, err := selectAssistantCatalogSources(context.Background(), catalogFixtureEmbedding{}, "Weather forecast", entries, 1)
	if err != nil || len(chosen) != 1 || chosen[0].CatalogID != "82" {
		t.Fatalf("unexpected ranked source: %#v %v", chosen, err)
	}
	chosen, err = selectAssistantCatalogSources(context.Background(), catalogFixtureEmbedding{fail: true}, "Weather forecast", entries, 1)
	if err != nil || len(chosen) != len(entries) {
		t.Fatalf("inference failure removed registered choices: %#v %v", chosen, err)
	}
	chosen, err = selectAssistantCatalogSources(context.Background(), catalogFixtureEmbedding{}, "Weather forecast", entries, 10)
	if err != nil || len(chosen) != len(entries) {
		t.Fatalf("candidate count above set size removed choices: %#v %v", chosen, err)
	}
}

func TestEmbeddedDatasourcePreselectionUsesVisibleContentAndFallsBackWhenIncomplete(t *testing.T) {
	entries := []assistantCatalogEntry{
		{CatalogID: "81", Datasource: "10", Name: "Opaque A", selectionText: "public.orders (amount:int)"},
		{CatalogID: "82", Datasource: "11", Name: "Opaque B", selectionText: "public.weather (forecast:text)"},
	}
	chosen, err := selectAssistantCatalogSources(context.Background(), catalogFixtureEmbedding{}, "weather forecast", entries, 1)
	if err != nil || len(chosen) != 1 || chosen[0].CatalogID != "82" {
		t.Fatalf("visible content did not select source: %#v %v", chosen, err)
	}
	entries[1].selectionIncomplete = true
	chosen, err = selectAssistantCatalogSources(context.Background(), catalogFixtureEmbedding{}, "weather forecast", entries, 1)
	if err != nil || len(chosen) != len(entries) {
		t.Fatalf("incomplete source content narrowed choices: %#v %v", chosen, err)
	}
	entries[1].selectionIncomplete = false
	entries[1].selectionText = strings.Repeat("weather ", 9000)
	chosen, err = selectAssistantCatalogSources(context.Background(), catalogFixtureEmbedding{}, "weather forecast", entries, 1)
	if err != nil || len(chosen) != len(entries) {
		t.Fatalf("oversized source content narrowed choices: %#v %v", chosen, err)
	}
}
