package messaging

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

const defaultContextKeepAliveInterval = 5 * time.Minute

// StartContextKeepAliveFromEnv starts the experimental context token probe loop.
// It is disabled by default. Set WECLAW_CONTEXT_KEEPALIVE=1 to enable getconfig
// probes, and WECLAW_CONTEXT_KEEPALIVE_TYPING=1 to additionally send a typing
// cancel probe when iLink returns a typing ticket.
func StartContextKeepAliveFromEnv(ctx context.Context, clients []*ilink.Client) {
	if os.Getenv("WECLAW_CONTEXT_KEEPALIVE") != "1" {
		return
	}
	interval := contextKeepAliveIntervalFromEnv()
	sendTyping := os.Getenv("WECLAW_CONTEXT_KEEPALIVE_TYPING") == "1"
	for _, client := range clients {
		if client == nil {
			continue
		}
		go runContextKeepAlive(ctx, client, interval, sendTyping)
	}
	log.Printf("[keepalive] enabled interval=%s typing=%t", interval, sendTyping)
}

func contextKeepAliveIntervalFromEnv() time.Duration {
	raw := os.Getenv("WECLAW_CONTEXT_KEEPALIVE_INTERVAL")
	if raw == "" {
		return defaultContextKeepAliveInterval
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < time.Minute {
		log.Printf("[keepalive] invalid WECLAW_CONTEXT_KEEPALIVE_INTERVAL=%q, using %s", raw, defaultContextKeepAliveInterval)
		return defaultContextKeepAliveInterval
	}
	return interval
}

func runContextKeepAlive(ctx context.Context, client *ilink.Client, interval time.Duration, sendTyping bool) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			keepAliveContextsOnce(ctx, client, sendTyping)
			timer.Reset(interval)
		}
	}
}

func keepAliveContextsOnce(ctx context.Context, client *ilink.Client, sendTyping bool) {
	if client == nil {
		return
	}
	tokens, err := ilink.LoadContextTokens(client.BotID())
	if err != nil {
		log.Printf("[keepalive] load context tokens for %s failed: %v", client.BotID(), err)
		return
	}
	if len(tokens) == 0 {
		return
	}
	for userID, token := range tokens {
		configResp, err := client.GetConfig(ctx, userID, token)
		if err != nil {
			log.Printf("[keepalive] getconfig user=%s failed: %v", userID, err)
			continue
		}
		if configResp.Ret == errCodeInvalidContext {
			clearStaleContextToken(client, userID, "keepalive")
			continue
		}
		if configResp.Ret != 0 {
			log.Printf("[keepalive] getconfig user=%s ret=%d errmsg=%s", userID, configResp.Ret, configResp.ErrMsg)
			continue
		}
		log.Printf("[keepalive] getconfig ok user=%s", userID)
		if sendTyping && configResp.TypingTicket != "" {
			if err := client.SendTyping(ctx, userID, configResp.TypingTicket, ilink.TypingStatusCancel); err != nil {
				log.Printf("[keepalive] sendtyping cancel user=%s failed: %v", userID, err)
			} else {
				log.Printf("[keepalive] sendtyping cancel ok user=%s", userID)
			}
		}
	}
}
