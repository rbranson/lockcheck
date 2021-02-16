package main

import (
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"
)

const SIG_DFL = 0

func alarm(t int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_ALARM, uintptr(t), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

type sigaction_t struct {
	handler uintptr
	sigaction uintptr
	mask uintptr
	flags uintptr
	restorer uintptr
}

func sigaction(sig syscall.Signal, sa sigaction_t) error {
	_, _, errno := syscall.Syscall6(
		syscall.SYS_RT_SIGACTION,
		uintptr(sig),
		uintptr(unsafe.Pointer(&sa)),
		0,
		unsafe.Sizeof(uintptr(0)),
		0,
		0)
	if errno != 0 {
		return errno
	}
	return nil
}

func main() {
	// set the SIGALRM handler back to default
	err := sigaction(syscall.SIGALRM, sigaction_t{handler: SIG_DFL})
	if err != nil {
		log.Fatal(err)
	}

	for {
		fmt.Printf("setting alarm...\n")
		err := alarm(2)
		if err != nil {
			panic(err)
		}
		time.Sleep(1 * time.Second)
	}
}