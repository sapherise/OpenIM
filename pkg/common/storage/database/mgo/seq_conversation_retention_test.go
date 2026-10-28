package mgo

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestMallocPreservesRetentionFloor(t *testing.T) {
	// The driver's mock deployment records commands without connecting to MongoDB.
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("reserve the next block without resetting min_seq", func(mt *mtest.T) {
		mt.AddMockResponses(bson.D{
			{Key: "ok", Value: 1},
			{Key: "value", Value: bson.D{{Key: "max_seq", Value: int64(102)}}},
		})
		seqs := &seqConversationMongo{coll: mt.Coll}
		previous, err := seqs.Malloc(context.Background(), "si_117619_836718", 51)
		if err != nil || previous != 51 {
			mt.Fatalf("previous sequence = %d, %v; want 51", previous, err)
		}
		event := mt.GetStartedEvent()
		if event == nil || event.CommandName != "findAndModify" {
			mt.Fatalf("unexpected command: %v", event)
		}
		update := event.Command.Lookup("update").Document()
		if _, err := update.LookupErr("$set"); err == nil {
			mt.Fatal("allocation must not reset an existing retention boundary")
		}
		if got := update.Lookup("$setOnInsert", "min_seq").Int64(); got != 0 {
			mt.Fatalf("new conversation min_seq = %d, want 0", got)
		}
		if got := update.Lookup("$inc", "max_seq").Int64(); got != 51 {
			mt.Fatalf("reservation increment = %d, want 51", got)
		}
		if !event.Command.Lookup("upsert").Boolean() {
			mt.Fatal("new conversations must still be upserted")
		}
	})
}
