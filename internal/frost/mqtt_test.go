package frost

import (
	"context"
	"errors"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// fakeToken satisfies mqtt.Token; only WaitTimeout/Error are exercised.
type fakeToken struct {
	mqtt.Token
	completed bool
	err       error
}

func (t *fakeToken) WaitTimeout(time.Duration) bool { return t.completed }
func (t *fakeToken) Error() error                   { return t.err }

// fakeClient satisfies mqtt.Client; only IsConnected/Publish are used.
type fakeClient struct {
	mqtt.Client
	connected bool
	lastTopic string
	returnTok mqtt.Token
}

func (c *fakeClient) IsConnected() bool { return c.connected }
func (c *fakeClient) Publish(topic string, _ byte, _ bool, _ interface{}) mqtt.Token {
	c.lastTopic = topic
	return c.returnTok
}

func newTestPublisher(c mqtt.Client) *MQTTPublisher {
	return &MQTTPublisher{
		client:         c,
		qos:            1,
		topicPrefix:    "v1.1",
		publishTimeout: time.Second,
	}
}

func TestMQTTTopic(t *testing.T) {
	p := newTestPublisher(nil)
	got := p.topic(42)
	want := "v1.1/Datastreams(42)/Observations"
	if got != want {
		t.Fatalf("topic = %q, want %q", got, want)
	}
}

func TestPublishObservationSuccess(t *testing.T) {
	fc := &fakeClient{connected: true, returnTok: &fakeToken{completed: true}}
	p := newTestPublisher(fc)

	err := p.PublishObservation(context.Background(), 7, map[string]any{"result": 12.5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fc.lastTopic != "v1.1/Datastreams(7)/Observations" {
		t.Fatalf("published to %q", fc.lastTopic)
	}
}

func TestPublishObservationNotConnectedIsTransient(t *testing.T) {
	fc := &fakeClient{connected: false}
	p := newTestPublisher(fc)

	err := p.PublishObservation(context.Background(), 1, map[string]any{"result": 1})
	if !IsTransient(err) {
		t.Fatalf("want transient error, got %v (transient=%v)", err, IsTransient(err))
	}
}

func TestPublishObservationBrokerErrorIsTransient(t *testing.T) {
	fc := &fakeClient{connected: true, returnTok: &fakeToken{completed: true, err: errors.New("broker refused")}}
	p := newTestPublisher(fc)

	err := p.PublishObservation(context.Background(), 1, map[string]any{"result": 1})
	if !IsTransient(err) {
		t.Fatalf("want transient error, got %v", err)
	}
}

func TestPublishObservationTimeoutIsTransient(t *testing.T) {
	fc := &fakeClient{connected: true, returnTok: &fakeToken{completed: false}}
	p := newTestPublisher(fc)

	err := p.PublishObservation(context.Background(), 1, map[string]any{"result": 1})
	if !IsTransient(err) {
		t.Fatalf("want transient error, got %v", err)
	}
}

func TestPublishObservationUnmarshalableIsPermanent(t *testing.T) {
	fc := &fakeClient{connected: true, returnTok: &fakeToken{completed: true}}
	p := newTestPublisher(fc)

	// A channel cannot be JSON-marshaled → permanent (retry won't help).
	err := p.PublishObservation(context.Background(), 1, make(chan int))
	if !IsPermanent(err) {
		t.Fatalf("want permanent error, got %v", err)
	}
}
