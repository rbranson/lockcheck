package main

import (
	"fmt"
	"syscall"
	"time"
)

const NICE_SYSCALL = 42069

func updateKillTs(t time.Time) {
	r1, r2, _ := syscall.Syscall(uintptr(NICE_SYSCALL), uintptr(t.Unix()), 0, 0)
	fmt.Printf("updateKillTs r1=%v r2=%v\n", r1, r2)
}

func main() {
	for i := 0; i < 10; i++ {
		go func(id int) {
			cnt := 0

			for {
				fmt.Printf("t:%d i:%d\n", id, cnt)
				time.Sleep(1 * time.Second)
				cnt++
				fmt.Printf("done sleeping\n")
				updateKillTs(time.Now().Add(2 * time.Second))
			}

		}(i)
	}

	ch := make(chan struct{})
	<-ch // GO TA SLEEP
}