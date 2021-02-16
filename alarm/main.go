package main

import (
	"fmt"
	"syscall"
	"time"
)

func alarm(t int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_ALARM, uintptr(t), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func main() {
	for {
		fmt.Printf("setting alarm...\n")
		err := alarm(2)
		if err != nil {
			panic(err)
		}
		time.Sleep(1 * time.Second)
	}
}