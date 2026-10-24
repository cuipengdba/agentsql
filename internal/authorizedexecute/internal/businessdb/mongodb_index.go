package businessdb

import (
	"context"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
	"go.mongodb.org/mongo-driver/bson"
)

// mongoApprovedIndex requires the exact equality key set approved by an
// administrator. A prefix match could silently scan an unbounded suffix.
func mongoApprovedIndex(control *model.MongoReadControl, request MongoFindRequest) (model.MongoIndexApproval, error) {
	if control == nil {
		return model.MongoIndexApproval{}, nil
	}
	for _, index := range control.Indexes {
		if index.Collection != request.Collection || len(index.Fields) != len(request.Filter) {
			continue
		}
		matched := true
		for _, field := range index.Fields {
			if _, ok := request.Filter[field]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return index, nil
		}
	}
	return model.MongoIndexApproval{}, fmt.Errorf("MongoDB equality filter has no approved index")
}

func mongoPlanMap(value any) (bson.M, bool) {
	switch typed := value.(type) {
	case bson.M:
		return typed, true
	case map[string]any:
		return bson.M(typed), true
	default:
		return nil, false
	}
}

func mongoPlanNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

func mongoPlanIndex(node any, approved model.MongoIndexApproval) (bool, bool) {
	plan, ok := mongoPlanMap(node)
	if !ok {
		return false, false
	}
	if plan["stage"] == "COLLSCAN" {
		return false, true
	}
	found := false
	if plan["stage"] == "IXSCAN" && plan["indexName"] == approved.Name {
		pattern, ok := mongoPlanMap(plan["keyPattern"])
		if !ok || len(pattern) != len(approved.Fields) {
			return false, true
		}
		for _, field := range approved.Fields {
			value, exists := pattern[field]
			direction, numeric := mongoPlanNumber(value)
			if !exists || !numeric || (direction != 1 && direction != -1) {
				return false, true
			}
		}
		found = true
	}
	for key, value := range plan {
		if key == "inputStage" || key == "outerStage" || key == "innerStage" || key == "queryPlan" || key == "winningPlan" {
			childFound, unsafe := mongoPlanIndex(value, approved)
			if unsafe {
				return false, true
			}
			found = found || childFound
		}
		if key == "inputStages" || key == "shards" {
			if children, ok := value.(bson.A); ok {
				for _, child := range children {
					childFound, unsafe := mongoPlanIndex(child, approved)
					if unsafe {
						return false, true
					}
					found = found || childFound
				}
			}
		}
	}
	return found, false
}

func mongoIndexPlanAllowed(explain bson.M, approved model.MongoIndexApproval, limit int) error {
	planner, ok := mongoPlanMap(explain["queryPlanner"])
	if !ok {
		return fmt.Errorf("MongoDB index plan is unavailable")
	}
	found, unsafe := mongoPlanIndex(planner["winningPlan"], approved)
	if unsafe || !found {
		return fmt.Errorf("MongoDB winning plan does not use the approved index")
	}
	stats, ok := mongoPlanMap(explain["executionStats"])
	if !ok {
		return fmt.Errorf("MongoDB execution statistics are unavailable")
	}
	keys, keysOK := mongoPlanNumber(stats["totalKeysExamined"])
	docs, docsOK := mongoPlanNumber(stats["totalDocsExamined"])
	maxExamined := int64(limit * 10)
	if maxExamined > 1000 {
		maxExamined = 1000
	}
	if !keysOK || !docsOK || keys < 0 || docs < 0 || keys > maxExamined || docs > maxExamined {
		return fmt.Errorf("MongoDB index scan exceeds the approved bound")
	}
	return nil
}

func (e *MongoExecutor) checkMongoIndex(ctx context.Context, request MongoFindRequest, filter, projection bson.M) (string, error) {
	approved, err := mongoApprovedIndex(e.readControl, request)
	if err != nil {
		return "", err
	}
	if e.readControl == nil {
		return "", nil
	}
	var explain bson.M
	command := bson.D{
		{Key: "explain", Value: bson.D{
			{Key: "find", Value: request.Collection},
			{Key: "filter", Value: filter},
			{Key: "projection", Value: projection},
			{Key: "hint", Value: approved.Name},
			{Key: "limit", Value: request.Limit},
			{Key: "maxTimeMS", Value: e.statementTimeout.Milliseconds()},
		}},
		{Key: "verbosity", Value: "executionStats"},
	}
	if err := e.client.Database(e.database).RunCommand(ctx, command).Decode(&explain); err != nil {
		return "", fmt.Errorf("MongoDB approved index explain failed: %w", err)
	}
	if err := mongoIndexPlanAllowed(explain, approved, request.Limit); err != nil {
		return "", err
	}
	return approved.Name, nil
}
