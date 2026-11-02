package util

import (
	"errors"
	"io/fs"
	"os"
)

func EnsureFile(path, content string) (created bool, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil // 已存在，什么都不做
	}
	if err != nil {
		return false, err // 权限、目录不存在等
	}
	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		return false, err
	}
	return true, nil
}
