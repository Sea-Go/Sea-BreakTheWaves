package content

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/IBM/sarama"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/catalog"
	"gorm.io/gorm"
	"testing"
)

type sessionFixture struct {
	ctx  context.Context
	acks int
}

func (s *sessionFixture) Context() context.Context                    { return s.ctx }
func (*sessionFixture) Claims() map[string][]int32                    { return nil }
func (*sessionFixture) MemberID() string                              { return "test" }
func (*sessionFixture) GenerationID() int32                           { return 1 }
func (*sessionFixture) MarkOffset(string, int32, int64, string)       {}
func (*sessionFixture) Commit()                                       {}
func (*sessionFixture) ResetOffset(string, int32, int64, string)      {}
func (s *sessionFixture) MarkMessage(*sarama.ConsumerMessage, string) { s.acks++ }

type claimFixture struct{ messages chan *sarama.ConsumerMessage }

func (*claimFixture) Topic() string                              { return "test" }
func (*claimFixture) Partition() int32                           { return 0 }
func (*claimFixture) InitialOffset() int64                       { return 0 }
func (*claimFixture) HighWaterMarkOffset() int64                 { return 100 }
func (c *claimFixture) Messages() <-chan *sarama.ConsumerMessage { return c.messages }
func claim(data []byte) *claimFixture {
	ch := make(chan *sarama.ConsumerMessage, 1)
	ch <- &sarama.ConsumerMessage{Topic: "test", Offset: 1, Value: data}
	close(ch)
	return &claimFixture{ch}
}
func TestConsumerNeverAcknowledgesFailedStorageCommit(t *testing.T) {
	s, _ := fixture(t)
	data, _ := json.Marshal(event("rollback", 1, "durable original"))
	if e := s.store.DB.Callback().Create().Before("gorm:create").Register("fail_event_commit", func(tx *gorm.DB) {
		if tx.Statement.Table == "btw_content_events" {
			tx.AddError(errors.New("forced commit failure"))
		}
	}); e != nil {
		t.Fatal(e)
	}
	defer s.store.DB.Callback().Create().Remove("fail_event_commit")
	session := &sessionFixture{ctx: context.Background()}
	e := (contentConsumer{s}).ConsumeClaim(session, claim(data))
	if e == nil || session.acks != 0 {
		t.Fatal("failed commit was acknowledged")
	}
	var count int64
	if e = s.store.DB.Model(&catalog.Revision{}).Count(&count).Error; e != nil {
		t.Fatal(e)
	}
	if count != 0 {
		t.Fatal("partial metadata transaction committed")
	}
}
func TestConsumerReplayAndMalformedEventRetention(t *testing.T) {
	s, _ := fixture(t)
	consumer := contentConsumer{s}
	data, _ := json.Marshal(event("replay", 1, "source"))
	session := &sessionFixture{ctx: context.Background()}
	for i := 0; i < 2; i++ {
		if e := consumer.ConsumeClaim(session, claim(data)); e != nil {
			t.Fatal(e)
		}
	}
	var count int64
	s.store.DB.Model(&catalog.Revision{}).Count(&count)
	if count != 1 || session.acks != 2 {
		t.Fatal("redelivery duplicated revision")
	}
	if e := consumer.ConsumeClaim(session, claim([]byte("{"))); e != nil {
		t.Fatal(e)
	}
	s.store.DB.Model(&catalog.Rejected{}).Count(&count)
	if count != 1 || session.acks != 3 {
		t.Fatal("malformed event not durably retained before ack")
	}
}
