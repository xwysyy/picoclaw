package session

import (
	"reflect"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/providers"
)

func (sm *SessionManager) ensureSessionLocked(key string) *Session {
	if session, ok := sm.sessions[key]; ok && session != nil {
		return session
	}

	now := time.Now()
	session := &Session{
		Key:      key,
		Messages: []providers.Message{},
		Created:  now,
		Updated:  now,
	}
	sm.sessions[key] = session
	return session
}

func cloneMessages(history []providers.Message) []providers.Message {
	if len(history) == 0 {
		return []providers.Message{}
	}
	msgs := make([]providers.Message, len(history))
	for i := range history {
		msgs[i] = cloneMessage(history[i])
	}
	return msgs
}

func cloneMessage(msg providers.Message) providers.Message {
	cloned := msg
	if len(msg.Media) > 0 {
		cloned.Media = append([]string(nil), msg.Media...)
	} else if msg.Media != nil {
		cloned.Media = []string{}
	}
	if len(msg.SystemParts) > 0 {
		cloned.SystemParts = make([]providers.ContentBlock, len(msg.SystemParts))
		for i := range msg.SystemParts {
			cloned.SystemParts[i] = msg.SystemParts[i]
			if msg.SystemParts[i].CacheControl != nil {
				cacheControl := *msg.SystemParts[i].CacheControl
				cloned.SystemParts[i].CacheControl = &cacheControl
			}
		}
	} else if msg.SystemParts != nil {
		cloned.SystemParts = []providers.ContentBlock{}
	}
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = make([]providers.ToolCall, len(msg.ToolCalls))
		for i := range msg.ToolCalls {
			cloned.ToolCalls[i] = cloneToolCall(msg.ToolCalls[i])
		}
	} else if msg.ToolCalls != nil {
		cloned.ToolCalls = []providers.ToolCall{}
	}
	return cloned
}

func cloneToolCall(call providers.ToolCall) providers.ToolCall {
	cloned := call
	if call.Function != nil {
		function := *call.Function
		cloned.Function = &function
	}
	if call.Arguments != nil {
		cloned.Arguments = cloneDynamicMap(call.Arguments)
	}
	if call.ExtraContent != nil {
		extra := *call.ExtraContent
		if call.ExtraContent.Google != nil {
			google := *call.ExtraContent.Google
			extra.Google = &google
		}
		cloned.ExtraContent = &extra
	}
	return cloned
}

func cloneDynamicMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	cloned := make(map[string]any, len(src))
	for key, value := range src {
		cloned[key] = cloneDynamicValue(value)
	}
	return cloned
}

func cloneDynamicValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneReflectValue(reflect.ValueOf(value)).Interface()
}

func cloneReflectValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		inner := cloneReflectValue(value.Elem())
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(inner)
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneReflectValue(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneReflectValue(value.Index(i)))
		}
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type().Elem())
		cloned.Elem().Set(cloneReflectValue(value.Elem()))
		return cloned
	default:
		return value
	}
}

func (sm *SessionManager) newEventLocked(now time.Time, key string, session *Session, typ EventType) SessionEvent {
	return SessionEvent{
		Type:       typ,
		ID:         newEventID(),
		ParentID:   strings.TrimSpace(session.LastEventID),
		TS:         now.UTC().Format(time.RFC3339Nano),
		TSMS:       now.UnixMilli(),
		SessionKey: key,
	}
}

func (sm *SessionManager) persistEventAndMetaLocked(key string, session *Session, ev SessionEvent) {
	if sm.storage == "" {
		return
	}
	if path := sm.eventsPath(key); path != "" {
		if err := appendJSONLEvent(path, ev); err == nil {
			session.LastEventID = ev.ID
		}
	}
	sm.persistMetaLocked(key, session)
}

func (sm *SessionManager) persistMetaLocked(key string, session *Session) {
	if sm.storage == "" {
		return
	}
	if path := sm.metaPath(key); path != "" {
		_ = writeMetaFile(path, buildSessionMeta(session))
	}
}
