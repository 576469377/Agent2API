// Package zcode 将已核验的免费模型合并到本机 ZCode 配置，不接触会话与统计文件。
package zcode

import (
	"github.com/576469377/Agent2API/internal/clientconfig"
	"time"
)

type Model = clientconfig.Model

func Eligible(m Model, now time.Time) bool { return clientconfig.Eligible(m, now) }
func route(m Model) string                 { return clientconfig.Route(m) }
func outputLimit(m Model) int              { return clientconfig.OutputLimit(m) }
