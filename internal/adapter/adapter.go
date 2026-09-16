// Package adapter 定义上游平台接缝。
//
// Adapter 只有两个方法，这是刻意的「深模块」设计：接口越小，假实现越便宜
// （测试里造一个 Adapter 只要十行），新增平台的边际成本越低。
package adapter

import (
	"context"

	"agent2api/internal/llm"
)

// ModelInfo 是平台无关的模型能力目录。
//
// 有了它，/v1/models 的输出与本地能力校验都不需要知道具体平台。
type ModelInfo struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description,omitempty"`
	ContextTokens    int    `json:"context_tokens"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	SupportsImages   bool   `json:"supports_images"`
	SupportsTools    bool   `json:"supports_tools"`
	SupportsThinking bool   `json:"supports_thinking"`
	IsDefault        bool   `json:"is_default"`
}

// Adapter 把供应商无关的请求转换为具体平台调用，并返回有序响应流。
type Adapter interface {
	// Stream 开始一次流式生成。返回的流用尽后必须读到 ErrStreamDone。
	Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error)

	// ListModels 返回当前账号可用的模型目录。
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Name 返回平台标识，用于日志与路由。
	Name() string
}

// Describer 是适配器**可选**实现的接口，用于向控制台暴露运行时信息。
//
// 单独成一个接口而不是塞进 Adapter，是为了让后续新增平台可以按需实现：
// 不实现的平台在控制台上只显示基础信息，不会被强制补齐。
type Describer interface {
	Describe() Description
}

// Description 是平台的运行时描述。
type Description struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Account string `json:"account,omitempty"`
	// Status: active / degraded / error
	Status string `json:"status"`
	Models int    `json:"models,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

// Configurable 是适配器可选实现的运行时配置接口。
type Configurable interface {
	// SetSanitize 开关内容脱敏。
	SetSanitize(bool)
	// Sanitize 返回当前脱敏开关状态。
	Sanitize() bool
	// InvalidateModels 让模型清单缓存失效，下次请求重新拉取。
	InvalidateModels()
}
