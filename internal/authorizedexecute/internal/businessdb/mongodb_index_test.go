package businessdb

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"go.mongodb.org/mongo-driver/bson"
)

func TestMongoApprovedIndexAndPlan(t *testing.T) {
	control := &model.MongoReadControl{Indexes: []model.MongoIndexApproval{{Collection: "customers", Name: "customer_id_1", Fields: []string{"customer_id"}}}}
	request := MongoFindRequest{Collection: "customers", Filter: map[string]string{"customer_id": "1"}, Limit: 10}
	approved, err := mongoApprovedIndex(control, request)
	if err != nil || approved.Name != "customer_id_1" {
		t.Fatalf("approved index: %+v %v", approved, err)
	}
	request.Filter["status"] = "active"
	if _, err := mongoApprovedIndex(control, request); err == nil {
		t.Fatal("unapproved filter accepted")
	}
	request.Filter = map[string]string{"customer_id": "1"}
	explain := bson.M{
		"queryPlanner":   bson.M{"winningPlan": bson.M{"stage": "FETCH", "inputStage": bson.M{"stage": "IXSCAN", "indexName": "customer_id_1", "keyPattern": bson.M{"customer_id": int32(1)}}}},
		"executionStats": bson.M{"totalKeysExamined": int64(2), "totalDocsExamined": int64(1)},
	}
	if err := mongoIndexPlanAllowed(explain, approved, 10); err != nil {
		t.Fatal(err)
	}
	validPlan := explain["queryPlanner"]
	for _, change := range []func(){
		func() { explain["queryPlanner"] = bson.M{"winningPlan": bson.M{"stage": "COLLSCAN"}} },
		func() {
			explain["queryPlanner"] = bson.M{"winningPlan": bson.M{"stage": "IXSCAN", "indexName": "other", "keyPattern": bson.M{"customer_id": int32(1)}}}
		},
		func() {
			explain["executionStats"] = bson.M{"totalKeysExamined": int64(101), "totalDocsExamined": int64(101)}
		},
	} {
		explain["queryPlanner"] = validPlan
		explain["executionStats"] = bson.M{"totalKeysExamined": int64(2), "totalDocsExamined": int64(1)}
		change()
		if err := mongoIndexPlanAllowed(explain, approved, 10); err == nil {
			t.Fatal("unsafe index plan accepted")
		}
	}
}
