package messaging

import (
	"context"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func (h *Handler) SetMediaService(client *MediaServiceClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mediaService = client
}

type incomingRichMedia struct {
	request  *MediaProcessRequest
	userText string
	async    bool
}

func (h *Handler) collectIncomingRichMedia(ctx context.Context, msg ilink.WeixinMessage) (*incomingRichMedia, bool, error) {
	req := &MediaProcessRequest{
		Source: "weclaw_ilink",
		MessageRef: MediaMessageRef{
			ChatID:    msg.FromUserID,
			Sender:    msg.FromUserID,
			MessageID: strconv.FormatInt(msg.MessageID, 10),
			Timestamp: time.Now().Format(time.RFC3339),
		},
		Options: MediaProcessOptions{
			TranscribeVoiceIfMissing: true,
			ExtractFileText:          true,
		},
	}

	for _, item := range msg.ItemList {
		switch item.Type {
		case ilink.ItemTypeText:
			if item.TextItem != nil {
				text := strings.TrimSpace(item.TextItem.Text)
				if text != "" {
					req.Texts = append(req.Texts, text)
					if IsURL(text) {
						req.URLs = append(req.URLs, ExtractURL(text))
					}
				}
			}
		case ilink.ItemTypeVoice:
			if item.VoiceItem == nil {
				continue
			}
			asset := MediaAsset{
				Transcript: item.VoiceItem.Text,
				EncodeType: item.VoiceItem.EncodeType,
				PlaytimeMS: item.VoiceItem.Playtime,
			}
			if item.VoiceItem.Playtime > 0 {
				asset.DurationSec = float64(item.VoiceItem.Playtime) / 1000.0
			}
			localPath, sizeBytes, err := downloadIncomingMedia(ctx, mediaDownloadDir(), "voice", voiceExt(item.VoiceItem.EncodeType), item.VoiceItem.Media, "")
			if err != nil {
				log.Printf("[media] failed to download voice from %s: %v", msg.FromUserID, err)
			} else if localPath != "" {
				asset.LocalPath = localPath
				asset.FileName = filepath.Base(localPath)
				asset.SizeBytes = sizeBytes
				asset.MIMEType = guessMimeTypeFromPath(localPath)
			}
			req.Voices = append(req.Voices, asset)
		case ilink.ItemTypeImage:
			if item.ImageItem == nil {
				continue
			}
			asset := MediaAsset{RemoteURL: item.ImageItem.URL}
			localPath, sizeBytes, err := downloadImageAsset(ctx, item.ImageItem)
			if err != nil {
				log.Printf("[media] failed to download image from %s: %v", msg.FromUserID, err)
			} else if localPath != "" {
				asset.LocalPath = localPath
				asset.FileName = filepath.Base(localPath)
				asset.SizeBytes = sizeBytes
				asset.MIMEType = guessMimeTypeFromPath(localPath)
			}
			req.Images = append(req.Images, asset)
		case ilink.ItemTypeVideo:
			if item.VideoItem == nil {
				continue
			}
			asset := MediaAsset{}
			localPath, sizeBytes, err := downloadIncomingMedia(ctx, mediaDownloadDir(), "video", ".mp4", item.VideoItem.Media, "")
			if err != nil {
				log.Printf("[media] failed to download video from %s: %v", msg.FromUserID, err)
			} else if localPath != "" {
				asset.LocalPath = localPath
				asset.FileName = filepath.Base(localPath)
				asset.SizeBytes = sizeBytes
				asset.MIMEType = guessMimeTypeFromPath(localPath)
			}
			req.Videos = append(req.Videos, asset)
		case ilink.ItemTypeFile:
			if item.FileItem == nil {
				continue
			}
			fileName := strings.TrimSpace(item.FileItem.FileName)
			asset := MediaAsset{FileName: fileName}
			localPath, sizeBytes, err := downloadIncomingMedia(ctx, mediaDownloadDir(), "file", fileExt(fileName), item.FileItem.Media, "")
			if err != nil {
				log.Printf("[media] failed to download file from %s: %v", msg.FromUserID, err)
			} else if localPath != "" {
				asset.LocalPath = localPath
				asset.FileName = filepath.Base(localPath)
				asset.SizeBytes = sizeBytes
				asset.MIMEType = guessMimeTypeFromPath(localPath)
			}
			req.FileCards = append(req.FileCards, asset)
		}
	}

	hasMedia := len(req.Images) > 0 || len(req.Videos) > 0 || len(req.Voices) > 0 || len(req.FileCards) > 0
	if !hasMedia {
		return nil, false, nil
	}

	userText := firstNonEmpty(req.Texts)
	if userText == "" && len(req.Voices) > 0 {
		userText = strings.TrimSpace(req.Voices[0].Transcript)
	}

	return &incomingRichMedia{
		request:  req,
		userText: userText,
		async:    len(req.Videos) > 0 || len(req.FileCards) > 0,
	}, true, nil
}

func (h *Handler) handleRichMediaMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, media *incomingRichMedia) {
	clientID := NewClientID()
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)

	agentNames, message := h.parseCommand(media.userText)
	promptText := media.userText
	if len(agentNames) > 0 {
		if message != "" {
			promptText = message
		} else {
			promptText = ""
		}
	}

	var knownNames []string
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}

	if media.async {
		ack := "已收到富媒体消息，正在处理，稍后回复。"
		if err := SendTextReply(ctx, client, msg.FromUserID, ack, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send ack to %s: %v", msg.FromUserID, err)
		}
		go h.processRichMediaAsync(client, msg, promptText, media.request, knownNames)
		return
	}

	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	if len(knownNames) == 0 {
		h.sendRichMediaToDefaultAgent(ctx, client, msg, promptText, media.request, clientID)
		return
	}
	if len(knownNames) == 1 {
		h.sendRichMediaToNamedAgent(ctx, client, msg, knownNames[0], promptText, media.request, clientID)
		return
	}
	h.broadcastRichMedia(ctx, client, msg, knownNames, promptText, media.request)
}

func (h *Handler) processRichMediaAsync(client *ilink.Client, msg ilink.WeixinMessage, promptText string, req *MediaProcessRequest, knownNames []string) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if len(knownNames) == 0 {
		clientID := NewClientID()
		h.sendRichMediaToDefaultAgent(bgCtx, client, msg, promptText, req, clientID)
		return
	}
	if len(knownNames) == 1 {
		clientID := NewClientID()
		h.sendRichMediaToNamedAgent(bgCtx, client, msg, knownNames[0], promptText, req, clientID)
		return
	}
	h.broadcastRichMedia(bgCtx, client, msg, knownNames, promptText, req)
}

func (h *Handler) sendRichMediaToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, promptText string, req *MediaProcessRequest, clientID string) {
	h.mu.RLock()
	defaultName := h.defaultName
	h.mu.RUnlock()

	reply, err := h.generateRichMediaReply(ctx, defaultName, msg.FromUserID, promptText, req)
	if err != nil {
		reply = fmt.Sprintf("Error: %v", err)
	}
	h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
}

func (h *Handler) sendRichMediaToNamedAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, name, promptText string, req *MediaProcessRequest, clientID string) {
	reply, err := h.generateRichMediaReply(ctx, name, msg.FromUserID, promptText, req)
	if err != nil {
		reply = fmt.Sprintf("Error: %v", err)
	}
	h.sendReplyWithMedia(ctx, client, msg, name, reply, clientID)
}

func (h *Handler) broadcastRichMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, names []string, promptText string, req *MediaProcessRequest) {
	type result struct {
		name  string
		reply string
	}

	ch := make(chan result, len(names))
	for _, name := range names {
		go func(agentName string) {
			reply, err := h.generateRichMediaReply(ctx, agentName, msg.FromUserID, promptText, req)
			if err != nil {
				ch <- result{name: agentName, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			ch <- result{name: agentName, reply: reply}
		}(name)
	}

	for range names {
		r := <-ch
		clientID := NewClientID()
		reply := fmt.Sprintf("[%s] %s", r.name, r.reply)
		h.sendReplyWithMedia(ctx, client, msg, r.name, reply, clientID)
	}
}

func (h *Handler) generateRichMediaReply(ctx context.Context, agentName, userID, promptText string, req *MediaProcessRequest) (string, error) {
	var prompt string
	if h.mediaService != nil {
		resp, err := h.mediaService.Process(ctx, req)
		if err != nil {
			log.Printf("[media] PyWxDump media service failed: %v", err)
			prompt = buildMediaAnalysisPrompt(promptText, req, "PyWxDump 媒体服务调用失败，以下为 WeClaw 已采集的原始媒体元数据。")
		} else {
			prompt = buildMediaAnalysisPrompt(promptText, resp, "")
		}
	} else {
		prompt = buildMediaAnalysisPrompt(promptText, req, "PyWxDump 媒体服务未配置，以下为 WeClaw 已采集的原始媒体元数据。")
	}

	if agentName == "" {
		ag := h.getDefaultAgent()
		if ag == nil {
			return "[echo] " + promptText, nil
		}
		return h.chatWithAgent(ctx, ag, userID, prompt)
	}

	ag, err := h.getAgent(ctx, agentName)
	if err != nil {
		return "", err
	}
	return h.chatWithAgent(ctx, ag, userID, prompt)
}

func mediaDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "weclaw-media-inbox")
	}
	return filepath.Join(home, ".weclaw", "media-inbox")
}

func downloadImageAsset(ctx context.Context, img *ilink.ImageItem) (string, int64, error) {
	var data []byte
	var err error
	var ext string
	var source string

	if img.URL != "" {
		var contentType string
		data, contentType, err = downloadFile(ctx, img.URL)
		ext = filepath.Ext(stripQuery(img.URL))
		if ext == "" {
			ext = extFromMIME(contentType)
		}
		source = img.URL
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
		ext = detectImageExt(data)
		source = img.Media.EncryptQueryParam
	} else {
		return "", 0, fmt.Errorf("image has no downloadable source")
	}
	if err != nil {
		return "", 0, err
	}
	if ext == "" {
		ext = detectImageExt(data)
	}
	return saveIncomingMediaFile("image", ext, data, source)
}

func downloadIncomingMedia(ctx context.Context, baseDir, prefix, ext string, media *ilink.MediaInfo, directURL string) (string, int64, error) {
	var data []byte
	var err error
	var source string

	if directURL != "" {
		var contentType string
		data, contentType, err = downloadFile(ctx, directURL)
		if ext == "" {
			ext = extFromMIME(contentType)
		}
		source = directURL
	} else if media != nil && media.EncryptQueryParam != "" {
		data, err = DownloadFileFromCDN(ctx, media.EncryptQueryParam, media.AESKey)
		source = media.EncryptQueryParam
	} else {
		return "", 0, fmt.Errorf("%s has no downloadable source", prefix)
	}
	if err != nil {
		return "", 0, err
	}
	if ext == "" {
		ext = ".bin"
	}
	return saveIncomingMediaFile(prefix, ext, data, source)
}

func saveIncomingMediaFile(prefix, ext string, data []byte, source string) (string, int64, error) {
	dir := filepath.Join(mediaDownloadDir(), time.Now().Format("20060102"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("create media dir: %w", err)
	}
	fileName := fmt.Sprintf("%s-%d%s", prefix, time.Now().UnixNano(), ext)
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", 0, fmt.Errorf("write media file: %w", err)
	}
	log.Printf("[media] saved incoming %s to %s (%d bytes)", source, path, len(data))
	return path, int64(len(data)), nil
}

func guessMimeTypeFromPath(path string) string {
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		return ct
	}
	return inferContentType(path)
}

func voiceExt(encodeType int) string {
	switch encodeType {
	case 5:
		return ".amr"
	case 6:
		return ".silk"
	case 7:
		return ".mp3"
	default:
		return ".wav"
	}
}

func fileExt(fileName string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(fileName)))
	if ext == "" {
		return ".bin"
	}
	return ext
}

func extFromMIME(contentType string) string {
	if contentType == "" {
		return ""
	}
	exts, err := mime.ExtensionsByType(contentType)
	if err != nil || len(exts) == 0 {
		return ""
	}
	return exts[0]
}

func firstNonEmpty(items []string) string {
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
