package controller

import (
	"context"
	"strings"
)

// RepairExpiredReadSeq advances only across the confirmed history-cleanup boundary.
// Missing payloads and unused reservation slots are not evidence of expiration.
func (db *commonMsgDatabase) RepairExpiredReadSeq(ctx context.Context, userID, conversationID string, readSeq, maxSeq int64) (int64, error) {
	if !strings.HasPrefix(conversationID, "si_") || readSeq >= maxSeq {
		return readSeq, nil
	}
	minSeq, err := db.seqConversation.GetMinSeq(ctx, conversationID)
	if err != nil {
		return readSeq, err
	}
	target := min(maxSeq, minSeq-1)
	if target <= readSeq {
		return readSeq, nil
	}
	if err := db.seqUser.SetUserReadSeqToDB(ctx, conversationID, userID, target); err != nil {
		return readSeq, err
	}
	if err := db.seqUser.SetUserReadSeq(ctx, conversationID, userID, target); err != nil {
		return readSeq, err
	}
	return db.seqUser.GetUserReadSeq(ctx, conversationID, userID)
}
