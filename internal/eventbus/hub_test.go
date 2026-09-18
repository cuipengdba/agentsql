package eventbus

import (
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestHubFansOutAndReplaysHistoryBeforeLiveEvents(t *testing.T) {
	hub, err := New(Options{HistorySize: 3, SubscriberBuffer: 2})
	require.NoError(t, err)
	for id := int64(1); id <= 4; id++ {
		hub.Publish(eventWithID(id))
	}

	first, cancelFirst := hub.Subscribe()
	defer cancelFirst()
	second, cancelSecond := hub.Subscribe()
	defer cancelSecond()
	hub.Publish(eventWithID(5))

	for _, subscriber := range []<-chan Event{first, second} {
		for _, want := range []int64{2, 3, 4, 5} {
			select {
			case got := <-subscriber:
				require.Equal(t, want, got.Audit.ID)
			case <-time.After(time.Second):
				t.Fatalf("timed out waiting for event %d", want)
			}
		}
	}
}

func TestHubDefaultsKeepLatestTwoHundredEvents(t *testing.T) {
	hub, err := New(Options{})
	require.NoError(t, err)
	for id := int64(1); id <= 250; id++ {
		hub.Publish(eventWithID(id))
	}
	subscriber, cancel := hub.Subscribe()
	defer cancel()
	for want := int64(51); want <= 250; want++ {
		require.Equal(t, want, (<-subscriber).Audit.ID)
	}
}

func TestHubSubscribeLiveDoesNotReplayHistory(t *testing.T) {
	hub, err := New(Options{HistorySize: 3, SubscriberBuffer: 2})
	require.NoError(t, err)
	hub.Publish(eventWithID(1))
	hub.Publish(eventWithID(2))

	events, cancel := hub.SubscribeLive()
	defer cancel()
	select {
	case event := <-events:
		t.Fatalf("SubscribeLive replayed historical event %d", event.Audit.ID)
	default:
	}
	hub.Publish(eventWithID(3))
	require.Equal(t, int64(3), requireReceive(t, events).Audit.ID)
}

func TestHubSubscribeLiveCancelIsIdempotent(t *testing.T) {
	hub, err := New(Options{HistorySize: 1, SubscriberBuffer: 1})
	require.NoError(t, err)
	events, cancel := hub.SubscribeLive()
	require.NotPanics(t, func() {
		cancel()
		cancel()
	})
	_, open := <-events
	require.False(t, open)
	require.Zero(t, hub.SubscriberCount())
}

func TestHubSlowLiveSubscriberUsesHubRemovalSemantics(t *testing.T) {
	hub, err := New(Options{HistorySize: 1, SubscriberBuffer: 2})
	require.NoError(t, err)
	slow, cancel := hub.SubscribeLive()
	defer cancel()

	start := time.Now()
	hub.Publish(eventWithID(1))
	hub.Publish(eventWithID(2))
	hub.Publish(eventWithID(3))
	require.Less(t, time.Since(start), time.Second)
	require.Zero(t, hub.SubscriberCount())
	require.Equal(t, int64(1), (<-slow).Audit.ID)
	require.Equal(t, int64(2), (<-slow).Audit.ID)
	_, open := <-slow
	require.False(t, open)
}

func TestHubSlowSubscriberIsRemovedWithoutBlockingPublish(t *testing.T) {
	hub, err := New(Options{HistorySize: 1, SubscriberBuffer: 2})
	require.NoError(t, err)
	slow, cancelSlow := hub.Subscribe()
	defer cancelSlow()
	healthy, cancelHealthy := hub.Subscribe()
	defer cancelHealthy()
	started := time.Now()
	for id := int64(1); id <= 3; id++ {
		hub.Publish(eventWithID(id))
		require.Equal(t, id, (<-healthy).Audit.ID)
	}
	require.Less(t, time.Since(started), time.Second)
	require.Eventually(t, func() bool { return hub.SubscriberCount() == 1 }, time.Second, time.Millisecond)
	_, open := <-slow
	require.True(t, open) // buffered event
	_, open = <-slow
	require.True(t, open) // buffered event
	_, open = <-slow
	require.False(t, open)
	hub.Close()
	_, open = <-healthy
	require.False(t, open)
}

func TestHubCancelAndCloseAreIdempotentAndConcurrentSafe(t *testing.T) {
	hub, err := New(Options{HistorySize: 8, SubscriberBuffer: 8})
	require.NoError(t, err)
	const workers = 16
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			for index := int64(0); index < 100; index++ {
				channel, cancel := hub.Subscribe()
				hub.Publish(eventWithID(offset*100 + index))
				cancel()
				cancel()
				for range channel {
				}
			}
		}(int64(worker))
	}
	go hub.Close()
	wg.Wait()
	hub.Close()
	hub.Publish(eventWithID(9999))
	channel, cancel := hub.Subscribe()
	cancel()
	_, open := <-channel
	require.False(t, open)
	require.Zero(t, hub.SubscriberCount())
}

func TestHubRejectsNegativeOptions(t *testing.T) {
	_, err := New(Options{HistorySize: -1})
	require.Error(t, err)
	_, err = New(Options{SubscriberBuffer: -1})
	require.Error(t, err)
}

func TestHubCopiesPointerBackedAuditFields(t *testing.T) {
	hub, err := New(Options{HistorySize: 1, SubscriberBuffer: 1})
	require.NoError(t, err)
	defer hub.Close()
	agentID := "before"
	hub.Publish(Event{Audit: model.AuditLog{ID: 1, AgentID: &agentID}})
	agentID = "after"
	events, cancel := hub.Subscribe()
	defer cancel()
	require.Equal(t, "before", *requireReceive(t, events).Audit.AgentID)
}

func requireReceive(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func eventWithID(id int64) Event {
	return Event{Audit: model.AuditLog{ID: id, Decision: "allow"}}
}
