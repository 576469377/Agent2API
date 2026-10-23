package adapter

// ConnectionSetup 独立描述接入方式，目录刷新失败也不能使登录入口消失。
type ConnectionSetup struct {
	Method      string `json:"method"`       // device_code / web_oauth / api_key / desktop / anonymous
	AccountMode string `json:"account_mode"` // pool / single
}

func SetupFor(platform string) *ConnectionSetup {
	switch platform {
	case "workbuddy", "workbuddy-global":
		return &ConnectionSetup{Method: "device_code", AccountMode: "pool"}
	case "trae-cn":
		return &ConnectionSetup{Method: "web_oauth", AccountMode: "single"}
	case "kiro":
		return &ConnectionSetup{Method: "cli_oauth", AccountMode: "single"}
	case "openrouter":
		return &ConnectionSetup{Method: "api_key", AccountMode: "single"}
	case "kilo":
		return &ConnectionSetup{Method: "anonymous", AccountMode: "single"}
	case "cursor":
		return &ConnectionSetup{Method: "desktop", AccountMode: "single"}
	default:
		return nil
	}
}
