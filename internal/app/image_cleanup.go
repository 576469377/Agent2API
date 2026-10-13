package app

import (
	"fmt"

	"github.com/576469377/Agent2API/internal/llm"
)

const omittedImageNote = "[较旧的图片已在本次转发中省略，以满足当前模型的图片数量上限。]"

// trimOldestRequestImages prepares an independent request for this one route.
// It replaces oldest image blocks with explicit text markers, never deletes a
// message/tool result, and never mutates input slices, pointers or client history.
func trimOldestRequestImages(req llm.RequestMessages, keep int) (llm.RequestMessages, int) {
	remove := decodedImageCount(req) - keep
	if remove <= 0 {
		return req, 0
	}
	remaining := remove
	var trimBlocks func([]llm.Content) []llm.Content
	trimBlocks = func(input []llm.Content) []llm.Content {
		output := make([]llm.Content, len(input))
		for index, block := range input {
			output[index] = block
			if block.Type == llm.ContentImage && remaining > 0 {
				output[index] = llm.Content{Type: llm.ContentText, Text: omittedImageNote}
				remaining--
			} else if block.Type == llm.ContentToolResult && block.ToolResult != nil {
				before := remaining
				result := *block.ToolResult
				result.Blocks = trimBlocks(result.Blocks)
				if removed := before - remaining; removed > 0 {
					if result.Content == "" {
						for _, original := range block.ToolResult.Blocks {
							if original.Type == llm.ContentText {
								result.Content += original.Text
							}
						}
					}
					// The WorkBuddy adapter reads Content when no image remains.
					// Keep its original text and add a marker rather than an empty result.
					result.Content += fmt.Sprintf("\n[本工具结果中有 %d 张较旧图片在本次转发中省略。]", removed)
				}
				output[index].ToolResult = &result
			}
		}
		return output
	}
	prepared := req
	prepared.Messages = make([]llm.Message, len(req.Messages))
	for index, message := range req.Messages {
		prepared.Messages[index] = message
		prepared.Messages[index].Content = trimBlocks(message.Content)
	}
	forwarded := decodedImageCount(prepared)
	prepared.InputImageCount = &forwarded
	return prepared, remove - remaining
}
