package agent

// Tools that return images.
//
// A tool that implements ContentTool can return images along with its text,
// such as the screenshot a computer-use tool takes. The agent sends them to
// the model inside the tool result (Anthropic), in the function output
// (OpenAI Responses) or in a user message right after the tool messages
// (OpenAI Chat Completions, whose tool messages can't carry images).
//
// Images are large, so only the most recent few are sent with each model call
// (ToolImagePolicy.KeepRecent, 3 by default); older ones become a short
// placeholder. Context estimates count each image as a fixed 1,600 tokens
// rather than as its base64 text. Callbacks and Result.Messages never carry
// the image data: they get the text and a short description such as
// "[1 image, image/png, 240 KB]".

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/Bradthebrad/tinychain/lc"
)

const (
	// MaxToolImageBytes is the largest image, decoded, a tool result can
	// send to the model.
	MaxToolImageBytes = 5 << 20
	// MaxToolImagesPerResult is how many images one tool result can send.
	MaxToolImagesPerResult = 4
	// DefaultKeepToolImages is how many of the most recent tool images are
	// sent with each model call unless ToolImagePolicy.KeepRecent says
	// otherwise.
	DefaultKeepToolImages = 3
)

// ToolImage is an image a tool returned.
type ToolImage struct {
	// MediaType is image/png, image/jpeg, image/gif or image/webp.
	MediaType string `json:"media_type"`
	// Data is the image, base64 encoded (standard alphabet, padded).
	Data string `json:"data"`
}

// Bytes is the decoded size of the image, worked out from its base64 length.
func (i ToolImage) Bytes() int {
	n := len(i.Data) * 3 / 4
	switch {
	case strings.HasSuffix(i.Data, "=="):
		n -= 2
	case strings.HasSuffix(i.Data, "="):
		n--
	}
	return maxInt(n, 0)
}

// ToolOutput is what a ContentTool returns.
type ToolOutput struct {
	// Text is what the model reads.
	Text string
	// Images are sent to the model after Text. See CheckToolImages for what
	// is accepted.
	Images []ToolImage
	// Meta is an optional summary of the call for activity displays, as
	// MetaTool returns. It is reported even when the call fails.
	Meta map[string]any
}

// ContentTool is implemented by tools that can return images as well as
// text. A wrapper around another tool (for permission checks, logging or
// redaction) should implement it too and forward with CallToolContent, so
// the images get through.
type ContentTool interface {
	Tool
	CallContent(ctx context.Context, args map[string]any) (ToolOutput, error)
}

// CallToolContent calls a tool the richest way it supports: CallContent for
// a ContentTool, CallWithMeta for a MetaTool, and Call otherwise.
func CallToolContent(ctx context.Context, tool Tool, args map[string]any) (ToolOutput, error) {
	switch t := tool.(type) {
	case ContentTool:
		return t.CallContent(ctx, args)
	case MetaTool:
		text, meta, err := t.CallWithMeta(ctx, args)
		return ToolOutput{Text: text, Meta: meta}, err
	default:
		text, err := tool.Call(ctx, args)
		return ToolOutput{Text: text}, err
	}
}

// ToolImagePolicy controls images that tools return.
type ToolImagePolicy struct {
	// Disabled sends no tool images to the model, for models that can't read
	// them. Each becomes a placeholder such as "[image image/png, 240 KB]".
	Disabled bool
	// KeepRecent is how many of the most recent tool images are sent with
	// each model call. Older ones become "[earlier image from <tool>
	// omitted]". nil means DefaultKeepToolImages; 0 sends none, like
	// Disabled. Use new(1) to keep one.
	KeepRecent *int
}

func (p ToolImagePolicy) keep() int {
	switch {
	case p.Disabled:
		return 0
	case p.KeepRecent == nil:
		return DefaultKeepToolImages
	default:
		return maxInt(*p.KeepRecent, 0)
	}
}

// SupportedImageType reports whether models are sent images of this type.
func SupportedImageType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// CheckToolImage checks an image before it is sent to a model and returns it
// cleaned up: a data: URI prefix and whitespace are removed, unpadded base64
// is padded, and the media type is taken from the data itself, because
// providers reject an image whose declared type doesn't match it. It fails
// for data that isn't base64, isn't a PNG, JPEG, GIF or WebP image, or is
// larger than MaxToolImageBytes decoded.
func CheckToolImage(img ToolImage) (ToolImage, error) {
	data := strings.TrimSpace(img.Data)
	if strings.HasPrefix(data, "data:") {
		if comma := strings.IndexByte(data, ','); comma >= 0 {
			data = data[comma+1:]
		}
	}
	data = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, data)
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(data, "="))
		if err != nil {
			return img, errors.New("not valid base64")
		}
		data = base64.StdEncoding.EncodeToString(raw)
	}
	switch {
	case len(raw) == 0:
		return img, errors.New("empty")
	case len(raw) > MaxToolImageBytes:
		return img, fmt.Errorf("larger than %s", formatBytes(MaxToolImageBytes))
	}
	mediaType := sniffImageType(raw)
	if mediaType == "" {
		return img, errors.New("not a PNG, JPEG, GIF or WebP image")
	}
	return ToolImage{MediaType: mediaType, Data: data}, nil
}

// CheckToolImages checks a tool result's images with CheckToolImage and keeps
// at most MaxToolImagesPerResult. It returns the images to send and, for
// each one left out, a placeholder line saying why, for the result's text.
func CheckToolImages(images []ToolImage) ([]ToolImage, []string) {
	var kept []ToolImage
	var notes []string
	for _, img := range images {
		if len(kept) >= MaxToolImagesPerResult {
			notes = append(notes, imageNotice(img, fmt.Sprintf("at most %d images per result", MaxToolImagesPerResult)))
			continue
		}
		checked, err := CheckToolImage(img)
		if err != nil {
			notes = append(notes, imageNotice(img, err.Error()))
			continue
		}
		kept = append(kept, checked)
	}
	return kept, notes
}

// DescribeImages describes images in a few words, such as
// "[2 images, image/png, 480 KB]", for logs, callbacks and saved history.
// It returns "" for no images.
func DescribeImages(images []ToolImage) string {
	if len(images) == 0 {
		return ""
	}
	var types []string
	seen := map[string]bool{}
	total := 0
	for _, img := range images {
		total += img.Bytes()
		mediaType := firstNonEmpty(img.MediaType, "image")
		if !seen[mediaType] {
			seen[mediaType] = true
			types = append(types, mediaType)
		}
	}
	noun := "image"
	if len(images) > 1 {
		noun = "images"
	}
	return fmt.Sprintf("[%d %s, %s, %s]", len(images), noun, strings.Join(types, " + "), formatBytes(total))
}

// imageNotice is the placeholder for an image that isn't sent: the same
// text an MCP image block gets when images can't be sent, plus why, if given.
func imageNotice(img ToolImage, reason string) string {
	mediaType := firstNonEmpty(strings.TrimSpace(img.MediaType), "data")
	if reason == "" {
		return fmt.Sprintf("[image %s, %s]", mediaType, formatBytes(img.Bytes()))
	}
	return fmt.Sprintf("[image %s, %s, not shown: %s]", mediaType, formatBytes(img.Bytes()), reason)
}

func sniffImageType(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(raw, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(raw, []byte("GIF87a")), bytes.HasPrefix(raw, []byte("GIF89a")):
		return "image/gif"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

func formatBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// toolResultImages decides which of a tool's images go into its result. It
// returns the images to send, placeholder lines for the result's text, and a
// summary for callbacks (nil when the tool returned no images).
func (a *Agent) toolResultImages(images []ToolImage) ([]ToolImage, []string, map[string]any) {
	if len(images) == 0 {
		return nil, nil, nil
	}
	kept, notes := CheckToolImages(images)
	info := map[string]any{"count": len(kept)}
	if len(kept) > 0 {
		types := make([]string, len(kept))
		total := 0
		for i, img := range kept {
			types[i] = img.MediaType
			total += img.Bytes()
		}
		info["media_types"] = types
		info["bytes"] = total
		info["summary"] = DescribeImages(kept)
	}
	if len(notes) > 0 {
		info["rejected"] = len(notes)
	}
	if a.toolImages.keep() == 0 {
		for _, img := range kept {
			notes = append(notes, imageNotice(img, ""))
		}
		kept = nil
	}
	info["sent"] = len(kept)
	return kept, notes, info
}

// toolResultContent is the content of a tool message: the text, then the
// images.
func toolResultContent(text string, images []ToolImage) lc.Content {
	if len(images) == 0 {
		return lc.TextContent(text)
	}
	parts := make([]lc.ContentPart, 0, len(images)+1)
	if text != "" {
		parts = append(parts, lc.ContentPart{Type: "text", Text: text})
	}
	for _, img := range images {
		parts = append(parts, lc.ContentPart{Type: "image", Source: &lc.ContentSource{Type: "base64", MediaType: img.MediaType, Data: img.Data}})
	}
	return lc.PartsContent(parts...)
}

// keepRecentToolImages keeps the last keep images of tool messages and
// replaces older ones with "[earlier image from <tool> omitted]". Images in
// other messages, such as a person's attachments, are left alone. It returns
// messages itself when nothing changes and never modifies it.
func keepRecentToolImages(messages []lc.BaseMessage, keep int) []lc.BaseMessage {
	var out []lc.BaseMessage
	var calls map[string]lc.ToolCall
	seen := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Type != lc.RoleTool {
			continue
		}
		n := imageCount(msg.Content)
		if n == 0 {
			continue
		}
		if seen+n <= keep {
			seen += n
			continue
		}
		// Within a message, later images are the more recent.
		drop := n - maxInt(keep-seen, 0)
		seen = keep
		if calls == nil {
			calls = toolCallsByID(messages)
		}
		placeholder := fmt.Sprintf("[earlier image from %s omitted]", firstNonEmpty(calls[msg.ToolCallID].Name, msg.Name, "a tool"))
		index := 0
		next := msg
		next.Content = replaceImageParts(msg.Content, func(lc.ContentPart) (string, bool) {
			index++
			return placeholder, index <= drop
		})
		if out == nil {
			out = append([]lc.BaseMessage(nil), messages...)
		}
		out[i] = next
	}
	if out == nil {
		return messages
	}
	return out
}

// withoutToolImages replaces the images in tool messages with a placeholder
// such as "[image image/png, 240 KB]", for callbacks and results, which must
// not carry image data. It returns messages itself when there are none.
func withoutToolImages(messages []lc.BaseMessage) []lc.BaseMessage {
	var out []lc.BaseMessage
	for i, msg := range messages {
		if msg.Type != lc.RoleTool || imageCount(msg.Content) == 0 {
			continue
		}
		if out == nil {
			out = append([]lc.BaseMessage(nil), messages...)
		}
		out[i].Content = replaceImageParts(msg.Content, func(part lc.ContentPart) (string, bool) {
			return imagePartNotice(part), true
		})
	}
	if out == nil {
		return messages
	}
	return out
}

// replaceImageParts returns content with the image parts for which replace
// says so turned into text. Content left with no images becomes plain text.
func replaceImageParts(content lc.Content, replace func(lc.ContentPart) (string, bool)) lc.Content {
	parts := make([]lc.ContentPart, 0, len(content.Parts))
	images := 0
	for _, part := range content.Parts {
		if isImagePart(part) {
			if text, ok := replace(part); ok {
				parts = append(parts, lc.ContentPart{Type: "text", Text: text})
				continue
			}
			images++
		}
		parts = append(parts, part)
	}
	if images > 0 {
		return lc.PartsContent(parts...)
	}
	for _, part := range parts {
		if part.Type != "text" {
			return lc.PartsContent(parts...)
		}
	}
	return lc.TextContent(contentText(lc.PartsContent(parts...)))
}

func imagePartNotice(part lc.ContentPart) string {
	if part.Source == nil || part.Source.Data == "" {
		return "[image]"
	}
	return imageNotice(ToolImage{MediaType: part.Source.MediaType, Data: part.Source.Data}, "")
}

func imageCount(content lc.Content) int {
	count := 0
	for _, part := range content.Parts {
		if isImagePart(part) {
			count++
		}
	}
	return count
}

func joinLines(lines ...string) string {
	kept := lines[:0:0]
	for _, line := range lines {
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
