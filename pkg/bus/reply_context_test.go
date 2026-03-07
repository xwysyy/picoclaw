package bus

import "testing"

func TestReplyContextBindingLookup(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	want := ReplyContext{SessionKey: "conv:feishu:direct:oc_test"}
	mb.BindReplyContext("feishu", "oc_test", "om_123", want)

	got, ok := mb.LookupReplyContext("feishu", "oc_test", "om_123")
	if !ok {
		t.Fatal("expected reply context binding to be found")
	}
	if got.SessionKey != want.SessionKey {
		t.Fatalf("expected session key %q, got %q", want.SessionKey, got.SessionKey)
	}
}

func TestReplyContextBindingLookupMiss(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	if _, ok := mb.LookupReplyContext("feishu", "oc_test", "missing"); ok {
		t.Fatal("expected reply context lookup miss")
	}
}
