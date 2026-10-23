package trae

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
)

type loginDevice struct {
	DeviceID  string `json:"device_id"`
	MachineID string `json:"machine_id"`
}

func desktopLoginDevice(path string) (loginDevice, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return loginDevice{}, err
	}
	defer clear(raw)
	var stored struct {
		DeviceID  string `json:"device_id"`
		MachineID string `json:"telemetry.machineId"`
	}
	if len(raw) > 16*1024*1024 || json.Unmarshal(raw, &stored) != nil || stored.DeviceID == "" || stored.MachineID == "" {
		return loginDevice{}, errors.New("TRAE 设备信息不可读")
	}
	return loginDevice{stored.DeviceID, stored.MachineID}, nil
}

func loadLoginDevice() (loginDevice, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return loginDevice{}, errors.New("无法定位 TRAE 授权设备信息")
	}
	root := filepath.Join(home, ".config")
	switch runtime.GOOS {
	case "windows":
		root = os.Getenv("APPDATA")
	case "darwin":
		root = filepath.Join(home, "Library", "Application Support")
	default:
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
			root = x
		}
	}
	for _, app := range []string{"Trae CN", "TRAE SOLO CN"} {
		if d, err := desktopLoginDevice(filepath.Join(root, app, "User", "globalStorage", "storage.json")); err == nil {
			return d, nil
		}
	}
	return persistentLoginDevice(filepath.Join(home, ".agent2api", "trae-cn.device.json"))
}

// 没有桌面安装时创建一次本机安装标识；以后登录复用，拒绝或限额不触发重置。
func persistentLoginDevice(path string) (loginDevice, error) {
	read := func() (loginDevice, error) {
		var d loginDevice
		raw, err := os.ReadFile(path)
		if err != nil {
			return d, err
		}
		if json.Unmarshal(raw, &d) != nil || d.DeviceID == "" || d.MachineID == "" {
			return loginDevice{}, errors.New("TRAE 安装标识不可读，请检查文件")
		}
		return d, nil
	}
	if d, err := read(); err == nil {
		return d, nil
	} else if !os.IsNotExist(err) {
		return loginDevice{}, err
	}
	var machine [32]byte
	if _, err := rand.Read(machine[:]); err != nil {
		return loginDevice{}, errors.New("无法创建 TRAE 安装标识")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(900000000000000))
	if err != nil {
		return loginDevice{}, errors.New("无法创建 TRAE 安装标识")
	}
	n.Add(n, big.NewInt(100000000000000))
	d := loginDevice{DeviceID: n.String(), MachineID: hex.EncodeToString(machine[:])}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return loginDevice{}, errors.New("无法创建 TRAE 安装标识目录")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return read()
	}
	if err != nil {
		return loginDevice{}, errors.New("无法保存 TRAE 安装标识")
	}
	raw, _ := json.Marshal(d)
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return loginDevice{}, errors.New("无法写入 TRAE 安装标识；保留现有文件以免重置")
	}
	return d, nil
}
