package messaging

import (
	"errors"
	"fmt"
	"log"

	"github.com/fastclaw-ai/weclaw/ilink"
)

const errCodeInvalidContext = -2

// ErrInvalidContext marks iLink ret=-2 responses so retry loops can stop
// before burning the rest of a freshly received context token.
var ErrInvalidContext = errors.New("invalid ilink context token")

func invalidContextError(operation, errmsg string) error {
	return fmt.Errorf("%w: %s failed: ret=%d errmsg=%s", ErrInvalidContext, operation, errCodeInvalidContext, errmsg)
}

func clearStaleContextToken(client *ilink.Client, toUserID, source string) {
	if client == nil || toUserID == "" {
		return
	}
	if err := ilink.ClearContextToken(client.BotID(), toUserID); err != nil {
		log.Printf("[%s] clear stale context token for %s failed: %v", source, toUserID, err)
		return
	}
	log.Printf("[%s] cleared stale context token for %s after ret=%d", source, toUserID, errCodeInvalidContext)
}
