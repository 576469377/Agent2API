package adapter

import (
	"time"
)

// affinityTTL 是绑定关系的存活时长。会话通常持续几十分钟到几小时，
// 1 小时 + 滑动续期能覆盖绝大多数会话，又不至于让废弃绑定长期占内存。
const affinityTTL = time.Hour

// affinityMaxKeys 是绑定表容量上限（LRU 语义由时间戳淘汰近似实现）。
// 单用户本地网关远用不满；设上限只为防御异常客户端制造海量 key。
const affinityMaxKeys = 4096

type affinityEntry struct {
	label     string
	expiresAt time.Time
}

// affinityTable 保存会话键 → 账号标签的绑定。
// 由 Pool 持有（选号时查询），生命周期与 Pool 相同。
type affinityTable struct {
	m map[string]affinityEntry
}

func newAffinityTable() *affinityTable {
	return &affinityTable{m: map[string]affinityEntry{}}
}

// get 返回仍有效的绑定；过期即清除（读时判定，无后台清理任务）。
func (t *affinityTable) get(key string) (string, bool) {
	e, ok := t.m[key]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiresAt) {
		delete(t.m, key)
		return "", false
	}
	// 滑动续期：命中的会话大概率还在活跃。
	e.expiresAt = time.Now().Add(affinityTTL)
	t.m[key] = e
	return e.label, true
}

// set 记录绑定。容量满时先清过期，仍满则放弃本次绑定（亲和是优化，
// 不值得为之做完整的 LRU 链表）。
func (t *affinityTable) set(key, label string) {
	if len(t.m) >= affinityMaxKeys {
		now := time.Now()
		for k, e := range t.m {
			if now.After(e.expiresAt) {
				delete(t.m, k)
			}
		}
		if len(t.m) >= affinityMaxKeys {
			return
		}
	}
	t.m[key] = affinityEntry{label: label, expiresAt: time.Now().Add(affinityTTL)}
}

// delete 移除绑定（绑定账号不可用时调用——旧绑定已无意义）。
func (t *affinityTable) delete(key string) {
	delete(t.m, key)
}
