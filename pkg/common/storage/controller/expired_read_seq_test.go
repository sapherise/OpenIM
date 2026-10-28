package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache"
)

type expiredConversationSeq struct {
	cache.SeqConversationCache
	min int64
	err error
}

func (s *expiredConversationSeq) GetMinSeq(context.Context, string) (int64, error) {
	return s.min, s.err
}

type expiredUserSeq struct {
	cache.SeqUser
	read, persisted int64
	writes          int
}

func (s *expiredUserSeq) SetUserReadSeqToDB(_ context.Context, _, _ string, n int64) error {
	s.persisted = max(s.persisted, n)
	s.writes++
	return nil
}
func (s *expiredUserSeq) SetUserReadSeq(_ context.Context, _, _ string, n int64) error {
	s.read = max(s.read, n)
	return nil
}
func (s *expiredUserSeq) GetUserReadSeq(context.Context, string, string) (int64, error) {
	return s.read, nil
}

func TestExpiredReadRepair(t *testing.T) {
	for _, test := range []struct {
		name                 string
		max, min, read, want int64
	}{
		{name: "confirmed expired prefix", max: 51, min: 2, read: 0, want: 1},
		{name: "unknown reservation tail stays unread", max: 51, min: 2, read: 1, want: 1},
		{name: "pending Mongo message above boundary stays unread", max: 2, min: 2, read: 1, want: 1},
		{name: "retained message after expired prefix stays unread", max: 52, min: 52, read: 1, want: 51},
		{name: "missing cleanup boundary is left alone", max: 51, read: 1, want: 1},
		{name: "large confirmed prefix needs no payload scan", max: 1000050, min: 1000001, read: 1, want: 1000000},
		{name: "fully expired conversation", max: 51, min: 52, read: 1, want: 51},
		{name: "cleanup boundary cannot advance beyond max", max: 51, min: 100, read: 1, want: 51},
		{name: "already read cursor is left alone", max: 51, min: 52, read: 51, want: 51},
	} {
		t.Run(test.name, func(t *testing.T) {
			user := &expiredUserSeq{read: test.read, persisted: test.read}
			db := &commonMsgDatabase{seqUser: user, seqConversation: &expiredConversationSeq{min: test.min}}
			got, err := db.RepairExpiredReadSeq(context.Background(), "1002", "si_1001_1002", test.read, test.max)
			if err != nil || got != test.want {
				t.Fatalf("read=%d, error=%v; want %d", got, err, test.want)
			}
			if user.persisted != test.want {
				t.Fatalf("persisted=%d, want %d", user.persisted, test.want)
			}
			if test.want == test.read && user.writes != 0 {
				t.Fatal("unconfirmed or already-read range was modified")
			}
		})
	}
}
func TestExpiredReadRepairPreservesConcurrentRead(t *testing.T) {
	user := &expiredUserSeq{read: 52, persisted: 52}
	db := &commonMsgDatabase{seqUser: user, seqConversation: &expiredConversationSeq{min: 52}}
	got, err := db.RepairExpiredReadSeq(context.Background(), "1002", "si_1001_1002", 1, 52)
	if err != nil || got != 52 || user.persisted != 52 {
		t.Fatalf("concurrent read regressed: %d, %v", got, err)
	}
}
func TestExpiredReadRepairDoesNotWriteOnLookupError(t *testing.T) {
	user := &expiredUserSeq{read: 1, persisted: 1}
	cause := errors.New("cleanup boundary unavailable")
	db := &commonMsgDatabase{seqUser: user, seqConversation: &expiredConversationSeq{err: cause}}
	_, err := db.RepairExpiredReadSeq(context.Background(), "1002", "si_1001_1002", 1, 51)
	if !errors.Is(err, cause) || user.writes != 0 {
		t.Fatal("lookup failure allowed a read correction")
	}
}
func TestExpiredReadRepairLeavesGroupAlone(t *testing.T) {
	db := &commonMsgDatabase{}
	got, err := db.RepairExpiredReadSeq(context.Background(), "1002", "sg_1001", 1, 51)
	if err != nil || got != 1 {
		t.Fatalf("group cursor changed: %d, %v", got, err)
	}
}
