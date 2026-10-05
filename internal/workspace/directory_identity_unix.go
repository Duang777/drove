//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"os"
	"reflect"
)

func openedDirectoryIdentity(root *os.Root) (result string, resultErr error) {
	directory, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer func() {
		resultErr = errors.Join(resultErr, directory.Close())
	}()
	info, err := directory.Stat()
	if err != nil {
		return "", err
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", errors.New("workspace: unsupported Unix file identity")
	}
	device, ok := reflectedUint(value.FieldByName("Dev"))
	if !ok {
		return "", errors.New("workspace: Unix file identity has no device")
	}
	inode, ok := reflectedUint(value.FieldByName("Ino"))
	if !ok {
		return "", errors.New("workspace: Unix file identity has no inode")
	}
	return fmt.Sprintf("unix:%x:%x", device, inode), nil
}

func reflectedUint(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(value.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint(), true
	default:
		return 0, false
	}
}
