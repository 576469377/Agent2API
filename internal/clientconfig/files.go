package clientconfig

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ReadFile 将缺失文件与空文件区分开，供首次创建时的并发比对使用。
func ReadFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("无法安全读取客户端配置，未修改文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("客户端配置不可读，请检查权限")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 16*1024*1024+1))
	if err != nil || len(raw) > 16*1024*1024 {
		return nil, fmt.Errorf("客户端配置读取失败或文件过大")
	}
	return raw, nil
}

func WriteFile(path string, source, candidate []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("无法创建客户端配置目录")
	}
	lock, err := os.OpenFile(path+".agent2api.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("客户端配置正在同步或不可写")
	}
	defer func() { lock.Close(); os.Remove(lock.Name()) }()
	check := func() error {
		now, err := ReadFile(path)
		if err != nil || (now == nil) != (source == nil) || !bytes.Equal(now, source) {
			return fmt.Errorf("同步期间客户端配置已被修改，请重新同步；原文件未被覆盖")
		}
		return nil
	}
	if err := check(); err != nil {
		return "", err
	}
	if bytes.Equal(source, candidate) {
		return "", nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent2api-client-*.tmp")
	if err != nil {
		return "", fmt.Errorf("无法创建客户端配置临时文件")
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
		return "", fmt.Errorf("写入临时配置失败，原文件已保留")
	}
	backupPath := ""
	if source != nil {
		backup, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".bak-"+time.Now().Format("20060102-150405")+"-*")
		if err != nil {
			return "", fmt.Errorf("无法备份客户端配置，原文件已保留")
		}
		backupPath = backup.Name()
		if err = backup.Chmod(0o600); err == nil {
			_, err = backup.Write(source)
		}
		if err == nil {
			err = backup.Sync()
		}
		closeErr = backup.Close()
		if err != nil || closeErr != nil {
			os.Remove(backupPath)
			return "", fmt.Errorf("备份客户端配置失败，原文件已保留")
		}
	}
	if err := check(); err != nil {
		return "", err
	}
	if source == nil {
		// Link 以独占方式发布首次配置，其他进程同时创建文件时不覆盖对方。
		err = os.Link(f.Name(), path)
	} else {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return "", fmt.Errorf("替换客户端配置失败，原文件和备份已保留")
	}
	return backupPath, nil
}
