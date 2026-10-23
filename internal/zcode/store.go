package zcode

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".zcode", "v2", "provider_config.json")
}

func Read(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("未检测到本机 ZCode 配置，请先启动 ZCode 一次")
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("无法安全读取本机 ZCode 配置，未修改文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取本机 ZCode 配置，请检查文件权限")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 16*1024*1024+1))
	if err != nil || len(raw) > 16*1024*1024 {
		return nil, fmt.Errorf("ZCode 配置读取失败或文件过大，未修改文件")
	}
	return raw, nil
}

// Write 比对获取目录前的原文件；无变化时保持字节和修改时间，有变化时先备份再替换。
func Write(path string, source, candidate []byte) (string, error) {
	check := func() error {
		current, err := Read(path)
		if err != nil || !bytes.Equal(current, source) {
			return fmt.Errorf("同步期间 ZCode 配置已被修改，请重新点击同步；原配置未被覆盖")
		}
		return nil
	}
	lock, err := os.OpenFile(path+".agent2api.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("ZCode 配置正在同步或不可写，请稍后重试")
	}
	defer func() { lock.Close(); os.Remove(lock.Name()) }()
	if err := check(); err != nil {
		return "", err
	}
	if bytes.Equal(source, candidate) {
		return "", nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent2api-zcode-*.tmp")
	if err != nil {
		return "", fmt.Errorf("无法创建 ZCode 配置临时文件，原配置已保留")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(candidate)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return "", fmt.Errorf("写入 ZCode 临时配置失败，原配置已保留")
	}
	if err := check(); err != nil {
		return "", err
	}
	backup, err := os.CreateTemp(filepath.Dir(path), "provider_config.json.bak-"+time.Now().Format("20060102-150405")+"-*")
	if err != nil {
		return "", fmt.Errorf("无法备份 ZCode 配置，原配置已保留")
	}
	if err = backup.Chmod(0o600); err == nil {
		_, err = backup.Write(source)
	}
	if err == nil {
		err = backup.Sync()
	}
	backupCloseErr := backup.Close()
	if err != nil || backupCloseErr != nil {
		os.Remove(backup.Name())
		return "", fmt.Errorf("备份 ZCode 配置失败，原配置已保留")
	}
	if err := check(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", fmt.Errorf("替换 ZCode 配置失败，原配置和备份已保留")
	}
	return backup.Name(), nil
}
