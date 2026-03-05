//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestFeishuCleanTextMentions_WithBotID_StripsBotAndReplacesOthers(t *testing.T) {
	botKey := "@_user_1"
	botName := "X-Claw"
	botOpenID := "ou_bot_123"

	userKey := "@_user_2"
	userName := "Alice"
	userOpenID := "ou_user_456"

	mentions := []*larkim.MentionEvent{
		{
			Key:  &botKey,
			Name: &botName,
			Id:   &larkim.UserId{OpenId: &botOpenID},
		},
		{
			Key:  &userKey,
			Name: &userName,
			Id:   &larkim.UserId{OpenId: &userOpenID},
		},
	}

	cleaned, mentioned := feishuCleanTextMentions(botKey+" hello "+userKey, mentions, botOpenID)
	if !mentioned {
		t.Fatalf("expected mentioned=true")
	}
	if cleaned != "hello @Alice" {
		t.Fatalf("unexpected cleaned content: %q", cleaned)
	}
}

func TestFeishuCleanTextMentions_WithoutBotID_OnlyStripsLeadingMention(t *testing.T) {
	key := "@_user_1"
	name := "Someone"
	openID := "ou_someone"

	mentions := []*larkim.MentionEvent{
		{
			Key:  &key,
			Name: &name,
			Id:   &larkim.UserId{OpenId: &openID},
		},
	}

	cleaned, mentioned := feishuCleanTextMentions(key+" hello", mentions, "")
	if !mentioned {
		t.Fatalf("expected mentioned=true for leading mention without bot_id")
	}
	if cleaned != "hello" {
		t.Fatalf("unexpected cleaned content: %q", cleaned)
	}

	cleaned, mentioned = feishuCleanTextMentions("hello "+key, mentions, "")
	if mentioned {
		t.Fatalf("expected mentioned=false for mid-text mention without bot_id")
	}
	if cleaned != "hello @Someone" {
		t.Fatalf("unexpected cleaned content: %q", cleaned)
	}
}

func TestFeishuCleanTextMentions_WithoutBotID_StripsMultipleLeadingMentions(t *testing.T) {
	k1 := "@_user_1"
	n1 := "A"
	id1 := "ou_a"
	k2 := "@_user_2"
	n2 := "B"
	id2 := "ou_b"

	mentions := []*larkim.MentionEvent{
		{Key: &k1, Name: &n1, Id: &larkim.UserId{OpenId: &id1}},
		{Key: &k2, Name: &n2, Id: &larkim.UserId{OpenId: &id2}},
	}

	cleaned, mentioned := feishuCleanTextMentions(k1+" "+k2+" hi", mentions, "")
	if !mentioned {
		t.Fatalf("expected mentioned=true")
	}
	if cleaned != "hi" {
		t.Fatalf("unexpected cleaned content: %q", cleaned)
	}
}

func TestFeishuDetectAndStripBotMention_NonTextMessages(t *testing.T) {
	key := "@_user_1"
	name := "Bot"
	openID := "ou_bot_123"

	mentions := []*larkim.MentionEvent{
		{
			Key:  &key,
			Name: &name,
			Id:   &larkim.UserId{OpenId: &openID},
		},
	}

	msgType := "post"
	message := &larkim.EventMessage{
		MessageType: &msgType,
		Mentions:    mentions,
	}

	mentioned, cleaned := feishuDetectAndStripBotMention(message, " hello ", "")
	if !mentioned {
		t.Fatalf("expected mentioned=true when bot_id is empty and mentions exist")
	}
	if cleaned != "hello" {
		t.Fatalf("unexpected cleaned content: %q", cleaned)
	}

	mentioned, cleaned = feishuDetectAndStripBotMention(message, " hi ", openID)
	if !mentioned {
		t.Fatalf("expected mentioned=true when mention matches bot_id")
	}
	if cleaned != "hi" {
		t.Fatalf("unexpected cleaned content: %q", cleaned)
	}

	mentioned, cleaned = feishuDetectAndStripBotMention(message, "hi", "ou_other")
	if mentioned {
		t.Fatalf("expected mentioned=false when no mention matches configured bot_id")
	}
	if cleaned != "hi" {
		t.Fatalf("unexpected cleaned content: %q", cleaned)
	}
}

func TestFeishuUserIDMatches(t *testing.T) {
	userID := "u_1"
	openID := "ou_1"
	unionID := "un_1"
	id := &larkim.UserId{
		UserId:  &userID,
		OpenId:  &openID,
		UnionId: &unionID,
	}

	if !feishuUserIDMatches(id, userID) {
		t.Fatalf("expected user_id match")
	}
	if !feishuUserIDMatches(id, openID) {
		t.Fatalf("expected open_id match")
	}
	if !feishuUserIDMatches(id, unionID) {
		t.Fatalf("expected union_id match")
	}
	if feishuUserIDMatches(id, "other") {
		t.Fatalf("did not expect mismatch to return true")
	}
	if feishuUserIDMatches(nil, openID) {
		t.Fatalf("nil id should not match")
	}
	if feishuUserIDMatches(id, "") {
		t.Fatalf("empty expected value should not match")
	}
}
